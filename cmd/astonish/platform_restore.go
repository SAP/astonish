package astonish

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
)

func handlePlatformRestoreCommand(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printPlatformRestoreUsage()
		return nil
	}
	if len(args) > 0 && args[0] == "cleanup" {
		return handlePlatformRestoreCleanup(args[1:])
	}
	opts, err := parsePlatformRestoreArgs(args)
	if err != nil {
		return err
	}
	if err := validatePlatformRestoreOptions(opts); err != nil {
		return err
	}

	appCfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	ctx := context.Background()
	entCfg, _, err := backupEntstoreConfig(appCfg)
	if err != nil {
		return err
	}
	if err := entstore.BootstrapPlatform(ctx, entCfg, nil); err != nil {
		return fmt.Errorf("failed to bootstrap target platform store: %w", err)
	}
	_, es, err := entstore.NewPlatformServices(ctx, entCfg)
	if err != nil {
		return fmt.Errorf("failed to connect to platform store: %w", err)
	}
	defer es.Close()

	var cleanup func()
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()
	var files entstore.BotRestoreBackend
	fileBackend := func(context.Context) (entstore.BotRestoreBackend, error) {
		if files != nil {
			return files, nil
		}
		b, closeBackend, err := sandbox.BackendFromAppConfig(appCfg)
		cleanup = closeBackend
		if err != nil {
			return nil, err
		}
		var ok bool
		files, ok = b.(entstore.BotRestoreBackend)
		if !ok {
			return nil, fmt.Errorf("configured backend cannot restore fresh Bot Homes: %w", store.ErrBotUnsupported)
		}
		return files, nil
	}
	restoreOpts := entstore.PlatformRestoreOptions{
		FileBackend:         fileBackend,
		DryRun:              opts.dryRun,
		ResetTarget:         opts.resetTarget,
		EnableScheduledJobs: opts.enableScheduledJobs,
		IncludeTransient:    opts.includeTransient,
		Passphrase:          opts.passphrase,
		MapOrg:              opts.mapOrg,
		MapTeam:             opts.mapTeam,
		MapUser:             opts.mapUser,
	}
	if opts.dryRun {
		plan, err := es.PlanPlatformRestore(ctx, opts.archivePath, restoreOpts)
		if err != nil {
			return err
		}
		return printRestorePlan(*plan, opts.jsonOut)
	}
	result, err := es.RestorePlatformBackup(ctx, opts.archivePath, restoreOpts)
	if err != nil {
		return err
	}
	return printRestoreResult(*result, opts.jsonOut)
}

type platformRestoreOptions struct {
	archivePath         string
	dryRun              bool
	confirm             bool
	resetTarget         bool
	enableScheduledJobs bool
	includeTransient    bool
	jsonOut             bool
	passphrase          string
	mapOrg              map[string]string
	mapTeam             map[string]string
	mapUser             map[string]string
}

func validatePlatformRestoreOptions(opts platformRestoreOptions) error {
	if !opts.dryRun && !opts.confirm {
		return fmt.Errorf("restore writes require --confirm; run with --dry-run first to preview")
	}
	return nil
}

func parsePlatformRestoreArgs(args []string) (platformRestoreOptions, error) {
	var opts platformRestoreOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			opts.dryRun = true
		case "--confirm":
			opts.confirm = true
		case "--reset-target":
			opts.resetTarget = true
		case "--enable-scheduled-jobs":
			opts.enableScheduledJobs = true
		case "--include-transient":
			opts.includeTransient = true
		case "--json":
			opts.jsonOut = true
		case "--passphrase":
			values := args[i+1:]
			if len(values) == 0 {
				return opts, fmt.Errorf("%s requires a value", args[i])
			}
			opts.passphrase = values[0]
			i++
		case "--map-org", "--map-team", "--map-user":
			values := args[i+1:]
			if len(values) == 0 {
				return opts, fmt.Errorf("%s requires a value", args[i])
			}
			if err := addRestoreMapping(&opts, args[i], values[0]); err != nil {
				return opts, err
			}
			i++
		case "-h", "--help":
			return opts, fmt.Errorf("--help must be the only argument")
		default:
			if opts.archivePath != "" {
				return opts, fmt.Errorf("unexpected argument: %s", args[i])
			}
			opts.archivePath = args[i]
		}
	}
	if opts.archivePath == "" {
		return opts, fmt.Errorf("backup archive path is required")
	}
	if opts.dryRun && opts.confirm {
		return opts, fmt.Errorf("--dry-run and --confirm cannot be used together")
	}
	return opts, nil
}

func addRestoreMapping(opts *platformRestoreOptions, flag, value string) error {
	from, to, ok := strings.Cut(value, ":")
	if !ok || from == "" || to == "" {
		return fmt.Errorf("%s requires from:to", flag)
	}
	switch flag {
	case "--map-org":
		if strings.Contains(from, "/") || strings.Contains(to, "/") {
			return fmt.Errorf("--map-org requires old-org:new-org")
		}
		if opts.mapOrg == nil {
			opts.mapOrg = make(map[string]string)
		}
		opts.mapOrg[from] = to
	case "--map-team":
		if !restoreMappingPairHasTwoParts(from) || !restoreMappingPairHasTwoParts(to) {
			return fmt.Errorf("--map-team requires old-org/old-team:new-org/new-team")
		}
		if opts.mapTeam == nil {
			opts.mapTeam = make(map[string]string)
		}
		opts.mapTeam[from] = to
	case "--map-user":
		if !restoreMappingPairHasTwoParts(from) || !restoreMappingPairHasTwoParts(to) {
			return fmt.Errorf("--map-user requires old-org/old-user:new-org/new-user")
		}
		if opts.mapUser == nil {
			opts.mapUser = make(map[string]string)
		}
		opts.mapUser[from] = to
	}
	return nil
}

func restoreMappingPairHasTwoParts(value string) bool {
	left, right, ok := strings.Cut(value, "/")
	return ok && left != "" && right != "" && !strings.Contains(right, "/")
}

func printRestorePlan(plan backup.RestorePlan, jsonOut bool) error {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(plan)
	}
	fmt.Println("Restore dry run complete.")
	printRestorePlanSummary(plan)
	if len(plan.Blockers) > 0 {
		fmt.Println("Blockers:")
		for _, blocker := range plan.Blockers {
			fmt.Printf("  - %s\n", blocker)
		}
	}
	if len(plan.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, warning := range plan.Warnings {
			fmt.Printf("  - %s\n", warning)
		}
	}
	return nil
}

func printRestoreResult(result backup.RestoreResult, jsonOut bool) error {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Println("Restore completed successfully.")
	printRestorePlanSummary(result.Plan)
	fmt.Printf("Restored entries: %d\n", result.RestoredEntries)
	fmt.Printf("Restored records: %d\n", result.RestoredRecords)
	if result.SkippedEntries > 0 {
		fmt.Printf("Skipped entries: %d\n", result.SkippedEntries)
	}
	if len(result.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, warning := range result.Warnings {
			fmt.Printf("  - %s\n", warning)
		}
	}
	return nil
}

func printRestorePlanSummary(plan backup.RestorePlan) {
	manifest := plan.Archive.Manifest
	fmt.Printf("Archive: %s v%d (%s, %s)\n", manifest.Format, manifest.FormatVersion, manifest.Backend, manifest.Mode)
	fmt.Printf("Target backend: %s\n", plan.TargetBackend)
	fmt.Printf("Target empty: %t\n", plan.TargetEmpty)
	fmt.Printf("Scopes: %d\n", len(plan.Scopes))
	for _, scope := range plan.Scopes {
		fmt.Printf("  - %s", scope.Scope.Kind)
		if scope.Scope.OrgSlug != "" {
			fmt.Printf(" org=%s", scope.Scope.OrgSlug)
		}
		if scope.Scope.TeamSlug != "" {
			fmt.Printf(" team=%s", scope.Scope.TeamSlug)
		}
		if scope.Scope.UserID != "" {
			fmt.Printf(" user=%s", scope.Scope.UserID)
		}
		fmt.Printf(" (%d entries, %d records)\n", scope.Entries, scope.Records)
	}
	fmt.Printf("Entries: %d\n", len(plan.Entries))
}

func printPlatformRestoreUsage() {
	fmt.Println("usage: astonish platform restore <archive> [--dry-run|--confirm] [options]")
	fmt.Println("       astonish platform restore cleanup <same-archive> --confirm")
	fmt.Println("")
	fmt.Println("Recover a clean platform installation from a logical backup archive.")
	fmt.Println("Cleanup: stop all target writers; removes only journal-owned uncommitted restore destinations.")
	fmt.Println("Keep the same configured target, archive, and backend. Cleanup never resets a populated platform.")
	fmt.Println("")
	fmt.Println("options:")
	fmt.Println("  --dry-run                 Validate and preview restore without writing")
	fmt.Println("  --confirm                 Execute restore; required for writes")
	fmt.Println("  --reset-target            Delete and recreate a non-empty SQLite target before restore")
	fmt.Println("  --enable-scheduled-jobs   Restore scheduled jobs as active instead of paused")
	fmt.Println("  --include-transient       Restore login/runtime transient tables")
	fmt.Println("  --passphrase <secret>     Decrypt an encrypted backup archive")
	fmt.Println("  --map-org old:new         Restore an organization under a new slug")
	fmt.Println("  --map-team oldorg/old:neworg/new")
	fmt.Println("                           Restore a team under a new org/team slug")
	fmt.Println("  --map-user oldorg/old:neworg/new")
	fmt.Println("                           Restore a personal scope under a new org/user ID")
	fmt.Println("  --json                    Print JSON output")
	fmt.Println("")
	fmt.Println("examples:")
	fmt.Println("  astonish platform restore backup.astonish-backup --dry-run")
	fmt.Println("  astonish platform restore backup.astonish-backup --confirm")
}
