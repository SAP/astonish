package entstore

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type fileFirstInventoryKey struct{}
type fileFirstRow struct {
	table string
	row   map[string]any
}
type fileFirstBot struct {
	Bot       store.Bot               `json:"bot"`
	Selection botContentHomeSelection `json:"selection"`
	Stage     store.BotContentScope   `json:"stage"`
}
type fileFirstSQLInventory struct {
	scope     backup.Scope
	rows      []fileFirstRow
	bots      map[uuid.UUID]fileFirstBot
	sessions  map[string]uuid.UUID
	inventory *botBackupInventory
	records   map[uuid.UUID][]botHomeConfigurationRecord
}

func newFileFirstSQLInventory(scope backup.Scope) *fileFirstSQLInventory {
	return &fileFirstSQLInventory{scope: scope, bots: map[uuid.UUID]fileFirstBot{}, sessions: map[string]uuid.UUID{}, records: map[uuid.UUID][]botHomeConfigurationRecord{}}
}
func validateBotExportRow(ctx context.Context, table string, row map[string]any) error {
	inv, _ := ctx.Value(fileFirstInventoryKey{}).(*fileFirstSQLInventory)
	if inv == nil {
		return validateBotArchiveRow(table, row)
	}
	if table == "team_audit_log" && strings.HasPrefix(backupString(row, "action"), "bot") {
		inv.rows = append(inv.rows, fileFirstRow{table, row})
		return nil
	}
	// Session events have no bot_id. Defer them until session ownership is known.
	if table == "sessions" || table == "session_events" || table == "bots" || strings.HasPrefix(table, "bot_") {
		inv.rows = append(inv.rows, fileFirstRow{table, row})
		return nil
	}
	return validateBotArchiveRow(table, row)
}
func backupString(row map[string]any, key string) string {
	if row[key] == nil {
		return ""
	}
	return fmt.Sprint(row[key])
}
func backupInt(row map[string]any, key string) int64 {
	n, _ := strconv.ParseInt(backupString(row, key), 10, 64)
	return n
}
func backupUUID(row map[string]any, key string) uuid.UUID {
	id, _ := uuid.Parse(backupString(row, key))
	return id
}
func backupDocument(row map[string]any, key string) (map[string]any, error) {
	return restoredBotDocument(row[key])
}

// finish is shared by export and offline verification. The archive index cannot
// confer ownership: scopes and roots are reconstructed from physical SQL rows.
func (i *fileFirstSQLInventory) finish() error {
	scopes := []store.BotContentScope{}
	for _, r := range i.rows {
		if r.table != "bots" {
			continue
		}
		row := r.row
		id, owner := backupUUID(row, "id"), backupUUID(row, "owner_id")
		status := store.BotStatus(backupString(row, "status"))
		if id == uuid.Nil || owner == uuid.Nil || (status != store.BotPaused && status != store.BotStopped) || row["active_run_id"] != nil || row["lease_expires_at"] != nil || row["deleted_at"] != nil || row["purge_after"] != nil {
			return fmt.Errorf("Bot must be paused/stopped and quiescent: %w", store.ErrBotConflict)
		}
		if _, exists := i.bots[id]; exists {
			return store.ErrBotInvalid
		}
		metadata, err := backupDocument(row, "data")
		if err != nil {
			return err
		}
		if err := validateFileFirstRootMetadata(metadata); err != nil {
			return err
		}
		// Never export a cleanup/provisioning claim or a permanent-delete candidate.
		for _, key := range []string{botHomeCleanupClaimKey, "cleanup_claim", "permanent_delete", "home_provisioning_claim"} {
			if v, ok := metadata[key]; ok && v != false && v != nil {
				return store.ErrBotConflict
			}
		}
		homeData, _, err := restoredBotStoredDocument(row["home_binding"])
		if err != nil {
			return err
		}
		var home store.BotHomeBinding
		if err = botBackupDecodeValue(homeData, &home); err != nil {
			return err
		}
		var selection botContentHomeSelection
		if err = botBackupDecodeValue(metadata[botContentHomeSelectionKey], &selection); err != nil {
			return err
		}
		scope := store.BotScope(i.scope.Kind)
		b := store.Bot{ID: id, OwnerID: owner, Scope: scope, Status: status, Revision: backupInt(row, "revision"), Version: backupInt(row, "version"), Home: home, HomeSessionID: backupUUID(row, "home_session_id")}
		binding := selection.Binding
		if home.ID == uuid.Nil || home.BotID != id || home.Scope != scope || home.Revision < 1 || b.Revision < 1 || b.Version < 1 || b.HomeSessionID == uuid.Nil || selection.Home != home || binding.Validate() != nil || binding.Backend != sandbox.BackendKindDocker || home.StorageKey != binding.Ref || home.Backend != string(binding.Backend) || binding.Scope.OrgID != i.scope.OrgSlug || binding.Scope.UserID != owner.String() || binding.Scope.BotID != id.String() {
			return fmt.Errorf("Home inventory binding mismatch: home=%+v binding=%+v bot=%+v: %w", home, binding, b, store.ErrBotInvalid)
		}
		stage := store.BotContentScope{OrgID: i.scope.OrgSlug, TeamID: i.scope.TeamSlug, OwnerID: owner.String(), BotID: id}
		if scope == store.BotScopePersonal {
			if i.scope.UserID != owner.String() {
				return store.ErrBotForbidden
			}
			stage.TeamID = personalSchemaName(owner.String())
		} else if scope != store.BotScopeTeam || binding.Scope.TeamID != i.scope.TeamSlug {
			return store.ErrBotForbidden
		}
		i.bots[id] = fileFirstBot{Bot: b, Selection: selection, Stage: stage}
		// Database runtime scopes pin owner/Bot identity, independently of the
		// workspace's staging/retained org and team namespaces.
		scopes = append(scopes, store.BotContentScope{OwnerID: owner.String(), BotID: id}, stage, store.BotContentScope{OrgID: binding.Scope.OrgID, TeamID: binding.Scope.TeamID, OwnerID: owner.String(), BotID: id})
	}
	for _, r := range i.rows {
		switch r.table {
		case "sessions":
			if r.row["bot_id"] == nil {
				continue
			}
			id := backupUUID(r.row, "bot_id")
			b, ok := i.bots[id]
			if !ok || backupUUID(r.row, "user_id") != b.Bot.OwnerID {
				return store.ErrBotForbidden
			}
			i.sessions[backupString(r.row, "id")] = id
		case "bot_templates", "team_audit_log":
			if i.scope.Kind != "team" {
				return store.ErrBotForbidden
			}
			scopes = append(scopes, store.BotContentScope{OrgID: i.scope.OrgSlug, TeamID: i.scope.TeamSlug})
		case "bot_intakes":
			owner, id := backupUUID(r.row, "owner_id"), backupUUID(r.row, "id")
			team := i.scope.TeamSlug
			if i.scope.Kind == "personal" {
				if owner.String() != i.scope.UserID {
					return store.ErrBotForbidden
				}
				team = personalSchemaName(owner.String())
			}
			scopes = append(scopes, store.BotContentScope{OrgID: i.scope.OrgSlug, TeamID: team, OwnerID: owner.String(), IntakeID: id})
		}
	}
	// An ordinary scope may have empty Bot tables and ordinary session rows.
	if len(scopes) == 0 {
		for _, r := range i.rows {
			if r.table != "session_events" {
				if err := validateBotArchiveRow(r.table, r.row); err != nil {
					return err
				}
			}
		}
		return nil
	}
	inv, err := newBotBackupInventory(scopes)
	if err != nil {
		return err
	}
	i.inventory = inv
	for _, r := range i.rows {
		if err := i.collect(r); err != nil {
			return fmt.Errorf("%s: %w", r.table, err)
		}
	}
	for id, b := range i.bots {
		if i.sessions[b.Bot.HomeSessionID.String()] != id {
			return store.ErrBotInvalid
		}
		versions := 0
		for _, r := range i.records[id] {
			if r.kind == botHomeVersion && r.revision == b.Bot.Version {
				versions++
			}
		}
		if versions != 1 {
			return store.ErrBotInvalid
		}
	}
	return nil
}
func (i *fileFirstSQLInventory) collect(r fileFirstRow) error {
	row, table := r.row, r.table
	id := backupUUID(row, "bot_id")
	if table == "bots" {
		metadata, e := backupDocument(row, "data")
		if e != nil {
			return e
		}
		return i.inventory.rootsFromMemory(backupUUID(row, "id"), metadata)
	}
	if table == "bot_schema_version" {
		return nil
	}
	if table == "team_audit_log" {
		if backupString(row, "action") != "bot_template.publish" {
			return store.ErrBotUnsupported
		}
		data, err := backupDocument(row, "detail")
		if err != nil {
			return err
		}
		var detail struct {
			Revision   string         `json:"revision"`
			Definition map[string]any `json:"definition"`
		}
		if err = botBackupDecodeValue(data, &detail); err != nil {
			return err
		}
		revision, err := strconv.ParseInt(detail.Revision, 10, 64)
		if err != nil {
			return store.ErrBotInvalid
		}
		return i.inventory.rootFromIndependent(backupUUID(row, "resource"), store.BotContentScope{OrgID: i.scope.OrgSlug, TeamID: i.scope.TeamSlug}, store.BotContentTemplate, revision, detail.Definition)
	}
	if table == "bot_templates" || table == "bot_intakes" {
		field, kind := "definition", store.BotContentTemplate
		scope := store.BotContentScope{OrgID: i.scope.OrgSlug, TeamID: i.scope.TeamSlug}
		if table == "bot_intakes" {
			field, kind = "content", store.BotContentIntake
			scope.OwnerID = backupUUID(row, "owner_id").String()
			scope.IntakeID = backupUUID(row, "id")
			if i.scope.Kind == "personal" {
				scope.TeamID = personalSchemaName(scope.OwnerID)
			}
			if row[field] == nil && row["discarded"] == true {
				return nil
			}
		}
		data, err := backupDocument(row, field)
		if err != nil {
			return err
		}
		return i.inventory.rootFromIndependent(backupUUID(row, "id"), scope, kind, backupInt(row, "revision"), data)
	}
	if table == "sessions" {
		if row["bot_id"] == nil {
			return nil
		}
		data, err := backupDocument(row, "metadata")
		if err != nil {
			return err
		}
		return i.inventory.rootsFromSession(id, backupString(row, "id"), data)
	}
	if table == "session_events" {
		id = i.sessions[backupString(row, "session_id")]
		if id == uuid.Nil {
			return nil
		}
	}
	if _, ok := i.bots[id]; !ok && table != "bot_tracked_items" {
		return store.ErrBotForbidden
	}
	kind, column := botHomeConfigurationKind(0), "data"
	switch table {
	case "bot_versions":
		kind, column = botHomeVersion, "definition"
	case "bot_goals":
		kind = botHomeGoal
	case "bot_runs":
		kind = botHomeRun
		switch store.RunStatus(backupString(row, "status")) {
		case store.RunSucceeded, store.RunPartial, store.RunFailed, store.RunSkipped, store.RunCancelled:
		default:
			return store.ErrBotConflict
		}
		if row["lease_expires_at"] != nil {
			return store.ErrBotConflict
		}
	case "bot_task_revisions":
		kind = botHomeTask
	case "bot_skill_revisions":
		kind = botHomeSkill
	case "bot_skills":
		kind = botHomeSkillRoot
	case "bot_approvals":
		kind = botHomeApproval
	case "bot_outbox":
		kind, column = botHomeActivity, "payload"
	case "bot_knowledge_extractions":
		kind = botHomeKnowledge
	case "bot_tracked_items":
		kind = botHomeTracked
		goal := backupUUID(row, "goal_id")
		for _, g := range i.rows {
			if g.table == "bot_goals" && backupUUID(g.row, "id") == goal {
				id = backupUUID(g.row, "bot_id")
			}
		}
		if _, ok := i.bots[id]; !ok {
			return store.ErrBotForbidden
		}
	case "session_events":
		kind, column = botHomeSessionEvent, "event_data"
	case "bot_tasks":
		return nil // typed scalar registry only; content is in revisions
	default:
		return store.ErrBotUnsupported // unknown coordination/content cannot silently disappear
	}
	data, err := backupDocument(row, column)
	if err != nil {
		return err
	}
	rev := backupInt(row, "revision")
	if kind == botHomeVersion || kind == botHomeRun || kind == botHomeApproval {
		rev = backupInt(row, "version")
	}
	if kind == botHomeActivity {
		rev = backupInt(row, "sequence")
	}
	created, _ := time.Parse(time.RFC3339Nano, backupString(row, "created_at"))
	record := botHomeConfigurationRecord{kind: kind, id: backupUUID(row, "id"), revision: rev, data: data, sessionID: backupString(row, "session_id"), activityKind: store.BotActivityKind(backupString(row, "kind")), createdAt: created}
	if kind == botHomeRun {
		var run botRunRecord
		if err = decodeBotRecord(data, &run); err != nil {
			return err
		}
		c := run.Control
		if c.ID != record.id || c.BotID != id || c.Version != rev || c.SessionID.String() != record.sessionID || c.Status != store.RunStatus(backupString(row, "status")) || c.ProgressPending != 0 || c.ProgressUncertain || (c.Work != nil && (c.Work.EmployeeReviewPending || c.Work.EmployeeRepairPending)) {
			return store.ErrBotConflict
		}
	}
	if err = i.inventory.rootsFromHome(id, []botHomeConfigurationRecord{record}); err != nil {
		return err
	}
	i.records[id] = append(i.records[id], record)
	return nil
}
func validateFileFirstRootMetadata(metadata map[string]any) error {
	// Unknown coordination (including operation/recovery claims) cannot be
	// certified quiescent by this exporter. Fail closed rather than copying an
	// unexamined nested body or an executable pending operation into an archive.
	for key, value := range metadata {
		switch key {
		case "restore_reconciliation_required":
			// Re-export may carry the restore hold, never an assertion that
			// recovery has reauthorized execution.
			if required, ok := value.(bool); !ok || !required {
				return store.ErrBotInvalid
			}
		case botProvisioningTracked, botProvisioningAttempted:
			// Historical allocation markers are not live claims. finish separately
			// requires a settled, exact registered Home and no active attachment.
			if _, ok := value.(bool); !ok {
				return store.ErrBotInvalid
			}
		case botContentHomeSelectionKey, botMemoryHeadKey:
			// Strict typed decoders below validate these reference-only records.
		case "previous_status":
			v, ok := value.(string)
			if !ok || (v != "" && v != string(store.BotActive) && v != string(store.BotPaused) && v != string(store.BotStopped)) {
				return store.ErrBotInvalid
			}
		case "permanent_delete":
			if value != false {
				return store.ErrBotConflict
			}
		case "create_hash":
			v, ok := value.(string)
			if !ok || !skillRootDigest(v) {
				return store.ErrBotInvalid
			}
		case "read_cursors":
			var cursors map[string]string
			if err := botBackupDecodeValue(value, &cursors); err != nil {
				return err
			}
			for viewer, cursor := range cursors {
				if _, err := uuid.Parse(viewer); err != nil {
					return store.ErrBotInvalid
				}
				if n, err := strconv.ParseInt(cursor, 10, 64); err != nil || n < 0 {
					return store.ErrBotInvalid
				}
			}
		default:
			return fmt.Errorf("unverified Bot root coordination %q: %w", key, store.ErrBotUnsupported)
		}
	}
	return nil
}

func (i *fileFirstSQLInventory) orderedBots() []fileFirstBot {
	out := make([]fileFirstBot, 0, len(i.bots))
	for _, b := range i.bots {
		out = append(out, b)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Bot.ID.String() < out[b].Bot.ID.String() })
	return out
}
