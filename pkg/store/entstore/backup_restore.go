package entstore

import (
	"context"
	"fmt"

	"github.com/SAP/astonish/pkg/backup"
)

type PlatformRestoreOptions struct {
	DryRun              bool
	ResetTarget         bool
	EnableScheduledJobs bool
	IncludeTransient    bool
	Passphrase          string
	MapOrg              map[string]string
	MapTeam             map[string]string
	MapUser             map[string]string
}

func (s *Store) PlanPlatformRestore(ctx context.Context, archivePath string, opts PlatformRestoreOptions) (*backup.RestorePlan, error) {
	summary, err := backup.Verify(archivePath, backup.ReaderOptions{Passphrase: opts.Passphrase})
	if err != nil {
		return nil, err
	}
	plan := backup.RestorePlan{
		Archive:       summary,
		TargetBackend: string(s.dialect),
	}
	if summary.Manifest.Mode != backupModeLogical {
		plan.Blockers = append(plan.Blockers, fmt.Sprintf("unsupported backup mode %q", summary.Manifest.Mode))
	}
	if err := validateRestoreMappings(summary.Manifest.Scopes, opts); err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
	}
	if s.dialect != DialectSQLite && s.dialect != DialectPostgres {
		plan.Blockers = append(plan.Blockers, fmt.Sprintf("restore for %s targets is not implemented yet", s.dialect))
	}

	empty, err := s.restoreTargetEmpty(ctx)
	if err != nil {
		return nil, err
	}
	plan.TargetEmpty = empty
	if !empty && !opts.ResetTarget {
		plan.Blockers = append(plan.Blockers, "target contains existing platform data; restore requires an empty target or --reset-target")
	}
	if opts.ResetTarget {
		plan.Warnings = append(plan.Warnings, "target data will be reset before restore")
		if s.dialect == DialectPostgres {
			plan.Blockers = append(plan.Blockers, "--reset-target for PostgreSQL targets is intentionally blocked; create a fresh target database or reset it externally before restore")
		}
	}
	credentialChecks, err := inspectRestoreCredentialKeys(ctx, archivePath, summary, opts)
	if err != nil {
		return nil, err
	}
	addCredentialRestoreCheckToPlan(&plan, credentialChecks)

	scopePlans := map[backup.Scope]*backup.RestoreScopePlan{}
	for _, entry := range summary.Manifest.Entries {
		action, reason := restoreActionForEntry(entry, opts)
		targetScope := mappedRestoreScope(entry.Scope, opts)
		plan.Entries = append(plan.Entries, backup.RestoreEntryPlan{
			Path:     entry.Path,
			Scope:    targetScope,
			Entity:   entry.Entity,
			Records:  entry.Records,
			Action:   action,
			Reason:   reason,
			Redacted: entry.Redacted,
		})
		sp := scopePlans[targetScope]
		if sp == nil {
			sp = &backup.RestoreScopePlan{Scope: targetScope}
			scopePlans[targetScope] = sp
		}
		sp.Entries++
		sp.Records += entry.Records
		if entry.Redacted {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s contains redacted fields and cannot fully recover protected values", entry.Path))
		}
		if action == "skip" && reason != "" {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s will be skipped: %s", entry.Path, reason))
		}
	}
	for _, scope := range summary.Manifest.Scopes {
		targetScope := mappedRestoreScope(scope, opts)
		if sp := scopePlans[targetScope]; sp != nil {
			plan.Scopes = append(plan.Scopes, *sp)
		} else {
			plan.Scopes = append(plan.Scopes, backup.RestoreScopePlan{Scope: targetScope})
		}
	}
	return &plan, nil
}

func (s *Store) RestorePlatformBackup(ctx context.Context, archivePath string, opts PlatformRestoreOptions) (*backup.RestoreResult, error) {
	plan, err := s.PlanPlatformRestore(ctx, archivePath, opts)
	if err != nil {
		return nil, err
	}
	if len(plan.Blockers) > 0 {
		return nil, fmt.Errorf("restore blocked: %s", plan.Blockers[0])
	}
	if opts.DryRun {
		return &backup.RestoreResult{Plan: *plan, Warnings: plan.Warnings}, nil
	}
	if opts.ResetTarget {
		if s.dialect != DialectSQLite {
			return nil, fmt.Errorf("reset-target restore for %s targets is not implemented yet", s.dialect)
		}
		if err := s.resetSQLiteRestoreTarget(ctx); err != nil {
			return nil, err
		}
		plan.TargetEmpty = true
	}
	if s.dialect == DialectPostgres {
		return s.restorePostgresLogicalBackup(ctx, archivePath, opts, *plan)
	}
	return s.restoreSQLiteLogicalBackup(ctx, archivePath, opts, *plan)
}

func restoreActionForEntry(entry backup.Entry, opts PlatformRestoreOptions) (string, string) {
	switch entry.Entity {
	case "bots", "bot_runs", "bot_approvals", "bot_outbox":
		return "restore_quiesced", "Bot execution authority is invalidated; undelivered work requires reconciliation"
	}
	if isTransientRestoreEntity(entry.Entity) && !opts.IncludeTransient {
		return "skip", "transient runtime or security state is skipped by default"
	}
	if entry.Entity == "scheduled_jobs" {
		if !opts.EnableScheduledJobs {
			return "restore_disabled", "scheduled jobs are restored paused by default"
		}
		return "restore", "ordinary jobs may be enabled; Bot-owned jobs always restore paused"
	}
	return "restore", ""
}

func isTransientRestoreEntity(entity string) bool {
	switch entity {
	case "login_sessions", "device_sessions", "pending_link_codes", "sandbox_sessions", "link_codes", "oauth_cache":
		return true
	default:
		return false
	}
}
