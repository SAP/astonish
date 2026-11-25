package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Mapping is an explicit graph transformation, not string substitution. The
// verified archive supplies the graph; only typed scope fields and artifact
// edges change. In particular, skill provenance and action arguments are opaque.
type fileFirstMapping struct {
	scopes map[store.BotContentScope]store.BotContentScope
	refs   map[store.BotContentRef]store.BotContentRef
	paths  map[store.BotContentRef]string
}

func newFileFirstMapping() *fileFirstMapping {
	return &fileFirstMapping{scopes: map[store.BotContentScope]store.BotContentScope{}, refs: map[store.BotContentRef]store.BotContentRef{}, paths: map[store.BotContentRef]string{}}
}

func fileFirstMappedOwner(org, owner string, opts PlatformRestoreOptions) string {
	if target := opts.MapUser[org+"/"+owner]; target != "" {
		_, user, ok := strings.Cut(target, "/")
		if ok {
			return user
		}
	}
	return owner
}

func mapFileFirstDescriptor(d fileFirstScopeArchive, opts PlatformRestoreOptions, mapping *fileFirstMapping) (fileFirstScopeArchive, error) {
	target := mappedRestoreScope(d.Scope, opts)
	for n := range d.Homes {
		h := &d.Homes[n]
		b := &h.Bot
		oldStage := b.Stage
		oldHome := store.BotContentScope{OrgID: b.Selection.Binding.Scope.OrgID, TeamID: b.Selection.Binding.Scope.TeamID, OwnerID: b.Bot.OwnerID.String(), BotID: b.Bot.ID}
		owner, err := uuid.Parse(fileFirstMappedOwner(d.Scope.OrgSlug, b.Bot.OwnerID.String(), opts))
		if err != nil {
			return d, err
		}
		homeScope := b.Selection.Binding.Scope
		homeScope.OrgID = target.OrgSlug
		homeScope.UserID = owner.String()
		// A personal Home still has an independently selected team binding.
		team := mappedRestoreScope(backup.Scope{Kind: "team", OrgSlug: d.Scope.OrgSlug, TeamSlug: homeScope.TeamID}, opts)
		if team.OrgSlug != target.OrgSlug {
			return d, store.ErrBotForbidden
		}
		homeScope.TeamID = team.TeamSlug
		b.Bot.OwnerID = owner
		b.Stage.OrgID, b.Stage.OwnerID = target.OrgSlug, owner.String()
		b.Stage.TeamID = target.TeamSlug
		if b.Bot.Scope == store.BotScopePersonal {
			b.Stage.TeamID = personalSchemaName(owner.String())
		}
		b.Selection.Binding.Scope = homeScope
		h.Manifest.Binding = homeScope.Key()
		mapping.scopes[oldStage] = b.Stage
		mapping.scopes[oldHome] = store.BotContentScope{OrgID: homeScope.OrgID, TeamID: homeScope.TeamID, OwnerID: homeScope.UserID, BotID: b.Bot.ID}
	}
	d.Scope = target
	return d, nil
}

// mapFileFirstContent spools once, then installs children before parents. Raw
// leaves are streamed without a size ceiling; only typed containers are decoded.
func mapFileFirstContent(ctx context.Context, archive, root, spool string, state *fileFirstRestoreState, backend BotRestoreBackend, scope backup.Scope, opts PlatformRestoreOptions) error {
	reader, err := backup.OpenReader(archive, backup.ReaderOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()
	var index botBackupFileIndex
	if err = reader.ForEachFile(func(p string, r io.Reader) error {
		if p == botBackupIndexPath {
			return botBackupDecode(io.LimitReader(r, backup.MaxMetadataBytes+1), &index)
		}
		return nil
	}); err != nil {
		return err
	}
	objects := map[store.BotContentRef]botBackupObject{}
	byPath := map[string]botBackupObject{}
	for _, o := range index.Objects {
		objects[o.Ref] = o
		byPath[botBackupObjectPath(o)] = o
	}
	mapping := state.mapping
	for _, o := range index.Objects {
		if _, ok := mapping.scopes[o.Ref.Scope]; ok {
			continue
		}
		// Independent team templates and personal/team intakes have no Home.
		old := o.Ref.Scope
		if old.BotID != uuid.Nil || old.Validate() != nil || old.OrgID != scope.OrgSlug {
			return store.ErrBotForbidden
		}
		// A nil BotID is not permission to import another tenant's template
		// or intake. Match the independently owned source namespace exactly.
		switch scope.Kind {
		case "team":
			if old.TeamID != scope.TeamSlug {
				return store.ErrBotForbidden
			}
		case "personal":
			if old.IntakeID == uuid.Nil || old.OwnerID != scope.UserID || old.TeamID != personalSchemaName(scope.UserID) {
				return store.ErrBotForbidden
			}
		default:
			return store.ErrBotForbidden
		}
		next := old
		target := mappedRestoreScope(scope, opts)
		next.OrgID, next.TeamID = target.OrgSlug, target.TeamSlug
		if old.OwnerID != "" {
			next.OwnerID = fileFirstMappedOwner(scope.OrgSlug, old.OwnerID, opts)
		}
		if scope.Kind == "personal" {
			next.TeamID = personalSchemaName(next.OwnerID)
		}
		if next.Validate() != nil {
			return store.ErrBotInvalid
		}
		mapping.scopes[old] = next
	}
	if err = reader.ForEachFile(func(p string, r io.Reader) error {
		o, ok := byPath[p]
		if !ok {
			return nil
		}
		f, err := os.CreateTemp(spool, "mapped-object-*")
		if err != nil {
			return err
		}
		_, e := io.CopyBuffer(f, botBackupContextReader{ctx, r}, make([]byte, 32<<10))
		if err = errors.Join(e, f.Close()); err != nil {
			return err
		}
		mapping.paths[o.Ref] = f.Name()
		return nil
	}); err != nil {
		return err
	}
	allowed := make([]store.BotContentScope, 0, len(mapping.scopes))
	for source := range mapping.scopes {
		allowed = append(allowed, source)
	}
	visiting := map[store.BotContentRef]bool{}
	var install func(store.BotContentRef) error
	install = func(old store.BotContentRef) error {
		if _, ok := mapping.refs[old]; ok {
			return nil
		}
		if visiting[old] {
			return store.ErrBotInvalid
		}
		visiting[old] = true
		o, ok := objects[old]
		if !ok {
			return store.ErrBotInvalid
		}
		path := mapping.paths[old]
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		inv, err := newBotBackupInventory(allowed)
		if err != nil {
			_ = f.Close()
			return err
		}
		err = errors.Join(inv.children(o, f), f.Close())
		if err != nil {
			return err
		}
		for _, child := range inv.objects {
			if err = install(child.Ref); err != nil {
				return err
			}
		}
		next := old
		next.Scope = mapping.scopes[old.Scope]
		if o.Codec == botBackupRun || o.Codec == botBackupActivity {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			updated, err := mapFileFirstBody(o, raw, mapping.refs)
			if err != nil {
				return err
			}
			if !bytes.Equal(raw, updated) {
				sum := sha256.Sum256(updated)
				next.SHA256 = hex.EncodeToString(sum[:])
				next.SizeBytes = int64(len(updated))
				f, err := os.CreateTemp(spool, "mapped-parent-*")
				if err != nil {
					return err
				}
				_, e := f.Write(updated)
				if err = errors.Join(e, f.Close()); err != nil {
					return err
				}
				path = f.Name()
			}
		}
		f, err = os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if strings.HasPrefix(old.Key, "staged-v1/") {
			disk, err := newBotDiskContent(root, next.Scope)
			if err != nil {
				return err
			}
			sr, e := disk.scopeRoot(true)
			if e != nil {
				_ = disk.Close()
				return e
			}
			if e = sr.Close(); e != nil {
				_ = disk.Close()
				return e
			}
			err = errors.Join(installBotBackupObject(ctx, disk, next, f), disk.Close())
			if err != nil {
				return err
			}
		} else {
			b, ok := state.homes[old.Scope.BotID]
			if !ok {
				return store.ErrBotForbidden
			}
			if next.SHA256 != old.SHA256 {
				ref, e := backend.PutBotContent(ctx, &b.Selection.Binding, next.Kind, next.SizeBytes, f, next.Revision, func(commit func() error) error { return commit() })
				if e != nil {
					return e
				}
				if ref == nil || ref.Scope != next.Scope || ref.Kind != next.Kind || ref.Revision != next.Revision || ref.SHA256 != next.SHA256 || ref.SizeBytes != next.SizeBytes {
					return store.ErrBotConflict
				}
				next = *ref
				if _, e = f.Seek(0, io.SeekStart); e != nil {
					return e
				}
			}
			stream, e := backend.OpenBotContent(ctx, &b.Selection.Binding, next, store.BotFileRange{})
			if e != nil {
				return e
			}
			if e = errors.Join(compareFileFirstStreams(ctx, stream, f, next.SizeBytes), stream.Close()); e != nil {
				return e
			}
		}
		mapping.refs[old] = next
		mapping.paths[next] = path
		visiting[old] = false
		return nil
	}
	for _, o := range index.Objects {
		if err = install(o.Ref); err != nil {
			return fmt.Errorf("map %s: %w", o.label(), err)
		}
	}
	return nil
}

func mapFileFirstArtifacts(artifacts []store.BotArtifact, refs map[store.BotContentRef]store.BotContentRef) error {
	for n := range artifacts {
		a := &artifacts[n]
		if a.ContentRef == nil {
			return store.ErrBotInvalid
		}
		next, ok := refs[*a.ContentRef]
		if !ok {
			return store.ErrBotInvalid
		}
		a.ContentRef = &next
	}
	return nil
}
func mapFileFirstBody(o botBackupObject, raw []byte, refs map[store.BotContentRef]store.BotContentRef) ([]byte, error) {
	switch o.Codec {
	case botBackupRun:
		var body botRunContent
		if err := botBackupDecode(bytes.NewReader(raw), &body); err != nil {
			return nil, err
		}
		// This typed container is historical content, not transferable authority.
		// Scrub it even when neither scopes nor artifact edges change.
		v := &body.Stored
		authority := v.Run.MandateRef != nil || v.Run.CallerCeiling != nil || v.ExternalAdmissionValidated || v.SkillAdmissionOwner != nil || v.ProgressOwnerContinuation != nil || v.ProgressQuota != nil || v.Run.Lease.WorkerID != "" || !v.Run.Lease.ExpiresAt.IsZero()
		v.Run.MandateRef, v.Run.CallerCeiling = nil, nil
		v.ExternalAdmissionValidated, v.SkillAdmissionOwner = false, nil
		v.ProgressOwnerContinuation, v.ProgressQuota = nil, nil
		v.Run.Lease.WorkerID, v.Run.Lease.ExpiresAt = "", time.Time{}
		if !authority && len(body.Stored.Run.Report.Artifacts) == 0 && (body.Stored.Work == nil || body.Stored.Work.Candidate == nil || len(body.Stored.Work.Candidate.Artifacts) == 0) {
			return raw, nil
		}
		if err := mapFileFirstArtifacts(body.Stored.Run.Report.Artifacts, refs); err != nil {
			return nil, err
		}
		if w := body.Stored.Work; w != nil && w.Candidate != nil {
			if err := mapFileFirstArtifacts(w.Candidate.Artifacts, refs); err != nil {
				return nil, err
			}
		}
		return json.Marshal(body)
	case botBackupActivity:
		var body botActivityContent
		if err := botBackupDecode(bytes.NewReader(raw), &body); err != nil {
			return nil, err
		}
		if body.Activity.Kind != store.BotActivityReport {
			return raw, nil
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(body.Activity.Payload, &payload); err != nil {
			return nil, err
		}
		changed := false
		if data, ok := payload["artifacts"]; ok {
			var a []store.BotArtifact
			if err := json.Unmarshal(data, &a); err != nil {
				return nil, err
			}
			if len(a) > 0 {
				if err := mapFileFirstArtifacts(a, refs); err != nil {
					return nil, err
				}
				payload["artifacts"], _ = json.Marshal(a)
				changed = true
			}
		}
		if data, ok := payload["report"]; ok {
			var report store.BotReport
			if err := json.Unmarshal(data, &report); err != nil {
				return nil, err
			}
			if len(report.Artifacts) > 0 {
				if err := mapFileFirstArtifacts(report.Artifacts, refs); err != nil {
					return nil, err
				}
				payload["report"], _ = json.Marshal(report)
				changed = true
			}
		}
		if !changed {
			return raw, nil
		}
		body.Activity.Payload, _ = json.Marshal(payload)
		return json.Marshal(body)
	}
	return raw, nil
}

// Keep SQL references as typed envelopes. No document text, map keys, historical
// actor, review digest or action target is interpreted as an identity mapping.
func mapFileFirstRow(table string, row map[string]any, state *fileFirstRestoreState) error {
	m := state.mapping
	if m == nil {
		return store.ErrBotInvalid
	}
	replace := func(ref *store.BotContentRef) error {
		next, ok := m.refs[*ref]
		if !ok {
			return store.ErrBotInvalid
		}
		*ref = next
		return nil
	}
	kind, column := botHomeConfigurationKind(0), "data"
	switch table {
	case "bots":
		data, err := backupDocument(row, "data")
		if err != nil {
			return err
		}
		if v, ok := data[botMemoryHeadKey]; ok {
			var ref store.BotContentRef
			if err = botBackupDecodeValue(v, &ref); err != nil {
				return err
			}
			if err = replace(&ref); err != nil {
				return err
			}
			data[botMemoryHeadKey] = ref
		}
		row["data"] = data
		return nil
	case "bot_versions":
		kind, column = botHomeVersion, "definition"
	case "bot_goals":
		kind = botHomeGoal
	case "bot_runs":
		kind = botHomeRun
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
	case "session_events":
		data, err := backupDocument(row, "event_data")
		if err != nil {
			return err
		}
		if _, ok := data["bot_record_v2"]; !ok {
			return nil
		}
		kind, column = botHomeSessionEvent, "event_data"
	case "sessions":
		if row["bot_id"] == nil {
			return nil
		}
		data, err := backupDocument(row, "metadata")
		if err != nil {
			return err
		}
		if v, ok := data["bot_initial_state"]; ok {
			var record map[string]any
			if err = botBackupDecodeValue(v, &record); err != nil {
				return err
			}
			nested := map[string]any{"event_data": record}
			if err = mapFileFirstRow("session_events", nested, state); err != nil {
				return err
			}
			data["bot_initial_state"] = nested["event_data"]
		}
		row["metadata"] = data
		return nil
	case "bot_templates", "bot_intakes":
		column = "definition"
		if table == "bot_intakes" {
			column = "content"
			if row[column] == nil {
				return nil
			}
		}
		data, err := backupDocument(row, column)
		if err != nil {
			return err
		}
		var r botIndependentRecord
		if err = decodeBotRecord(data, &r); err != nil {
			return err
		}
		if err = replace(&r.Content); err != nil {
			return err
		}
		raw, err := json.Marshal(r)
		row[column] = map[string]any{"bot_record_v2": string(raw)}
		return err
	case "team_audit_log":
		if backupString(row, "action") != "bot_template.publish" {
			return nil
		}
		data, err := backupDocument(row, "detail")
		if err != nil {
			return err
		}
		nested := map[string]any{"definition": data["definition"]}
		if err = mapFileFirstRow("bot_templates", nested, state); err != nil {
			return err
		}
		data["definition"] = nested["definition"]
		row["detail"] = data
		return nil
	default:
		return nil
	}
	data, err := backupDocument(row, column)
	if err != nil {
		return err
	}
	record, ref, finish, err := homeRecordEnvelope(botHomeConfigurationRecord{kind: kind, data: data})
	if err != nil {
		return err
	}
	if ref == nil && kind != botHomeSkillRoot {
		if err := mapFileFirstInline(record, m); err != nil {
			return err
		}
		mapped, err := encodeRuntimeRecord(record)
		if err != nil {
			return err
		}
		row[column] = mapped
		return nil
	}
	// Preserve the original SQL envelope bytes when the typed transformation is
	// an identity. Re-encoding an unchanged record needlessly changes historical
	// JSON field ordering and optional fields, despite retaining the same hash.
	before, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if ref != nil {
		if err = replace(ref); err != nil {
			return err
		}
	}
	if err = finish(); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if bytes.Equal(before, raw) {
		return nil
	}
	row[column] = map[string]any{"bot_record_v2": string(raw)}
	return nil
}

func mapFileFirstInline(record any, m *fileFirstMapping) error {
	var in *botRuntimeInline
	var id store.BotID
	var codec botBackupCodec
	switch v := record.(type) {
	case *botRunRecord:
		in, id, codec = &v.botRuntimeInline, v.Control.BotID, botBackupRun
	case *botActivityRecord:
		in, id, codec = &v.botRuntimeInline, v.BotID, botBackupActivity
	case *botApprovalRecord:
		in, id = &v.botRuntimeInline, v.BotID
	case *botSessionEventRecord:
		in, id = &v.botRuntimeInline, v.BotID
	default:
		return store.ErrBotUnsupported
	}
	next, ok := m.scopes[in.Scope]
	if !ok || next.BotID != id {
		return store.ErrBotForbidden
	}
	if codec != "" {
		raw, err := mapFileFirstBody(botBackupObject{Codec: codec}, in.Inline, m.refs)
		if err != nil {
			return err
		}
		*in = runtimeInline(raw)
	}
	in.Scope = next
	if run, ok := record.(*botRunRecord); ok {
		var body botRunContent
		if err := botBackupDecode(bytes.NewReader(in.Inline), &body); err != nil {
			return err
		}
		v := body.Stored
		if body.PreparedPlans != nil {
			v.PreparedPlans = map[string]store.BotPreparedPlan{}
			for k, p := range body.PreparedPlans {
				v.PreparedPlans[k] = p.plan()
			}
		}
		if v.SkillRequests == nil && run.Control.SkillRequestHashes != nil && len(run.Control.SkillRequestHashes) == 0 {
			v.SkillRequests = map[string]botSkillReplay{}
		}
		v.Run.Checkpoint = json.RawMessage(body.Checkpoint)
		if v.Task != nil && body.TaskParameters != nil {
			v.Task.Manifest.Parameters = map[string]json.RawMessage{}
			for k, p := range body.TaskParameters {
				v.Task.Manifest.Parameters[k] = json.RawMessage(p)
			}
		}
		c := &run.Control
		c.MandateRef, c.CallerCeiling = nil, nil
		c.ExternalAdmissionValidated, c.SkillAdmissionOwner = false, nil
		c.ProgressOwnerContinuation, c.ProgressQuota = nil, nil
		c.Lease.WorkerID, c.Lease.ExpiresAt = "", time.Time{}
		applyRunControl(&v, *c)
		var err error
		run.LogicalDigest, err = runContentDigest(v)
		return err
	}
	return nil
}

func validateFileFirstMappingKeys(scopes []backup.Scope, opts PlatformRestoreOptions) error {
	orgs, teams := map[string]bool{}, map[string]bool{}
	for _, s := range scopes {
		if s.OrgSlug != "" {
			orgs[s.OrgSlug] = true
		}
		if s.Kind == "team" {
			teams[s.OrgSlug+"/"+s.TeamSlug] = true
		}
	}
	for from, to := range opts.MapOrg {
		if !orgs[from] || to == "" {
			return fmt.Errorf("unknown or empty organization mapping %q", from)
		}
	}
	for from, to := range opts.MapTeam {
		org, _, ok := strings.Cut(to, "/")
		source, _, _ := strings.Cut(from, "/")
		target := mappedRestoreScope(backup.Scope{Kind: "org", OrgSlug: source}, opts)
		if !teams[from] || !ok || org != target.OrgSlug {
			return fmt.Errorf("inconsistent team mapping %q", from)
		}
	}
	for from, to := range opts.MapUser {
		org, user, ok := strings.Cut(from, "/")
		dest, id, valid := strings.Cut(to, "/")
		target := mappedRestoreScope(backup.Scope{Kind: "org", OrgSlug: org}, opts)
		_, e1 := uuid.Parse(user)
		_, e2 := uuid.Parse(id)
		if !orgs[org] || !ok || !valid || dest != target.OrgSlug || e1 != nil || e2 != nil {
			return fmt.Errorf("inconsistent user mapping %q", from)
		}
	}
	return nil
}
