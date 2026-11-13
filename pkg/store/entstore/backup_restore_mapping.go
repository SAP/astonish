package entstore

import (
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/backup"
)

func validateRestoreMappings(scopes []backup.Scope, opts PlatformRestoreOptions) error {
	seenTargets := make(map[backup.Scope]backup.Scope, len(scopes))
	for _, scope := range scopes {
		target := mappedRestoreScope(scope, opts)
		if err := target.Validate(); err != nil {
			return fmt.Errorf("invalid restore mapping for %s: %w", backupScopeKey(scope), err)
		}
		if prior, ok := seenTargets[target]; ok && prior != scope {
			return fmt.Errorf("restore mappings merge %s and %s into %s; merging scopes is not supported", backupScopeKey(prior), backupScopeKey(scope), backupScopeKey(target))
		}
		seenTargets[target] = scope
	}
	return nil
}

func mappedRestoreScope(scope backup.Scope, opts PlatformRestoreOptions) backup.Scope {
	target := scope
	if scope.OrgSlug != "" {
		if mapped := opts.MapOrg[scope.OrgSlug]; mapped != "" {
			target.OrgSlug = mapped
		}
	}
	if scope.Kind == "team" {
		if mapped := opts.MapTeam[scope.OrgSlug+"/"+scope.TeamSlug]; mapped != "" {
			orgSlug, teamSlug, ok := strings.Cut(mapped, "/")
			if ok {
				target.OrgSlug = orgSlug
				target.TeamSlug = teamSlug
			}
		}
	}
	if scope.Kind == "personal" {
		if mapped := opts.MapUser[scope.OrgSlug+"/"+scope.UserID]; mapped != "" {
			orgSlug, userID, ok := strings.Cut(mapped, "/")
			if ok {
				target.OrgSlug = orgSlug
				target.UserID = userID
			}
		}
	}
	return target
}

func mappedManifestForRestore(manifest backup.Manifest, opts PlatformRestoreOptions) backup.Manifest {
	out := manifest
	out.Scopes = make([]backup.Scope, 0, len(manifest.Scopes))
	seenScopes := make(map[backup.Scope]struct{}, len(manifest.Scopes))
	for _, scope := range manifest.Scopes {
		target := mappedRestoreScope(scope, opts)
		if _, ok := seenScopes[target]; ok {
			continue
		}
		seenScopes[target] = struct{}{}
		out.Scopes = append(out.Scopes, target)
	}
	out.Entries = make([]backup.Entry, len(manifest.Entries))
	for i, entry := range manifest.Entries {
		entry.Scope = mappedRestoreScope(entry.Scope, opts)
		out.Entries[i] = entry
	}
	if len(manifest.SchemaVersions) > 0 {
		out.SchemaVersions = make(map[string]backup.SchemaVersion, len(manifest.SchemaVersions))
		for _, scope := range manifest.Scopes {
			archiveVersion, ok := manifest.SchemaVersions[backupScopeKey(scope)]
			if !ok {
				continue
			}
			archiveVersion.Scope = backupScopeKey(mappedRestoreScope(scope, opts))
			out.SchemaVersions[archiveVersion.Scope] = archiveVersion
		}
	}
	return out
}

func remapRestoreRow(scope backup.Scope, targetScope backup.Scope, table string, row map[string]any, opts PlatformRestoreOptions) map[string]any {
	if len(opts.MapOrg) == 0 && len(opts.MapTeam) == 0 && len(opts.MapUser) == 0 {
		return row
	}
	if isBotRestoreRow(table, row) {
		return remapBotRestoreRow(scope, targetScope, table, row, opts)
	}
	out := make(map[string]any, len(row))
	for key, value := range row {
		out[key] = remapRestoreValue(value, opts)
	}

	switch table {
	case "organizations":
		if scope.Kind == "org" || targetScope.OrgSlug != "" {
			out["slug"] = targetScope.OrgSlug
			out["db_name"] = ""
		}
	case "teams":
		teamSlug := targetScope.TeamSlug
		if teamSlug == "" {
			teamSlug = fmt.Sprint(row["slug"])
			if mapped := opts.MapTeam[scope.OrgSlug+"/"+teamSlug]; mapped != "" {
				_, mappedTeam, ok := strings.Cut(mapped, "/")
				if ok {
					teamSlug = mappedTeam
				}
			}
		}
		if teamSlug != "" {
			out["slug"] = teamSlug
			out["schema_name"] = teamSchemaName(teamSlug)
		}
	}
	if scope.Kind == "personal" && targetScope.UserID != "" {
		for _, column := range []string{"user_id", "created_by", "updated_by", "owner_id"} {
			if _, ok := out[column]; ok {
				out[column] = targetScope.UserID
			}
		}
	}
	return out
}

// Bot documents contain reviewed content and historical identities, not generic
// scope strings. Never recursively substitute names, evidence, action arguments,
// hashes, storage keys or attribution inside these documents.
func isBotRestoreRow(table string, row map[string]any) bool {
	switch table {
	case "bots", "bot_versions", "bot_goals", "bot_tracked_items", "bot_runs", "bot_approvals", "bot_outbox", "bot_templates", "bot_skills", "bot_skill_revisions":
		return true
	case "sessions":
		id, _ := row["bot_id"].(string)
		return id != ""
	case "scheduled_jobs":
		return row["mode"] == "bot_trigger"
	}
	return false
}

func remapBotRestoreRow(scope, target backup.Scope, table string, row map[string]any, opts PlatformRestoreOptions) map[string]any {
	out := make(map[string]any, len(row))
	for key, value := range row {
		out[key] = value
	}
	mapOwner := func(value any) any {
		id, ok := value.(string)
		if !ok || id == "" {
			return value
		}
		if scope.Kind == "personal" && id == scope.UserID {
			return target.UserID
		}
		if mapped := opts.MapUser[scope.OrgSlug+"/"+id]; mapped != "" {
			org, user, ok := strings.Cut(mapped, "/")
			if ok && org == target.OrgSlug {
				return user
			}
		}
		return value
	}
	switch table {
	case "bots":
		out["owner_id"] = mapOwner(row["owner_id"])
		// Read positions belong to current viewers. Preserve historical actors
		// everywhere else. On a cursor collision reset to unread rather than
		// accidentally hide activity from the mapped viewer.
		if data, err := restoredBotDocument(row["data"]); err == nil {
			copied := make(map[string]any, len(data))
			for k, v := range data {
				copied[k] = v
			}
			if cursors, ok := data["read_cursors"].(map[string]any); ok {
				mapped := make(map[string]any, len(cursors))
				for viewer, cursor := range cursors {
					key := mapOwner(viewer).(string)
					if _, exists := mapped[key]; exists {
						mapped[key] = "0"
					} else {
						mapped[key] = cursor
					}
				}
				copied["read_cursors"] = mapped
			}
			out["data"] = copied
		}
	case "sessions":
		out["user_id"] = mapOwner(row["user_id"])
	}
	return out
}

func remapRestoreValue(value any, opts PlatformRestoreOptions) any {
	switch v := value.(type) {
	case string:
		if mapped := opts.MapOrg[v]; mapped != "" {
			return mapped
		}
		for from, to := range opts.MapTeam {
			_, fromTeam, okFrom := strings.Cut(from, "/")
			_, toTeam, okTo := strings.Cut(to, "/")
			if okFrom && okTo && v == fromTeam {
				return toTeam
			}
		}
		for from, to := range opts.MapUser {
			_, fromUser, okFrom := strings.Cut(from, "/")
			_, toUser, okTo := strings.Cut(to, "/")
			if okFrom && okTo && v == fromUser {
				return toUser
			}
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, nested := range v {
			out[key] = remapRestoreValue(nested, opts)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, nested := range v {
			out[i] = remapRestoreValue(nested, opts)
		}
		return out
	default:
		return v
	}
}
