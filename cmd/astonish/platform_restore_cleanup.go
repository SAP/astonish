package astonish

import (
	"context"
	"fmt"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
)

func parsePlatformRestoreCleanup(args []string) (platformRestoreOptions, error) {
	opts, err := parsePlatformRestoreArgs(args)
	if err != nil {
		return opts, err
	}
	if !opts.confirm || opts.dryRun || opts.resetTarget || opts.enableScheduledJobs || opts.includeTransient || len(opts.mapOrg)+len(opts.mapTeam)+len(opts.mapUser) != 0 {
		return opts, fmt.Errorf("usage: astonish platform restore cleanup <same-archive> --confirm; stop all target writers first (mappings come from the journal)")
	}
	return opts, nil
}
func handlePlatformRestoreCleanup(args []string) error {
	opts, err := parsePlatformRestoreCleanup(args)
	if err != nil {
		return err
	}
	cfg, err := config.LoadAppConfig()
	if err != nil {
		return err
	}
	entCfg, _, err := backupEntstoreConfig(cfg)
	if err != nil {
		return err
	}
	ctx := context.Background()
	_, es, err := entstore.NewPlatformServices(ctx, entCfg)
	if err != nil {
		return err
	}
	defer es.Close()
	var closeBackend func()
	defer func() {
		if closeBackend != nil {
			closeBackend()
		}
	}()
	err = es.CleanupFileFirstRestore(ctx, opts.archivePath, entstore.PlatformRestoreOptions{FileBackend: func(context.Context) (entstore.BotRestoreBackend, error) {
		b, closeFn, e := sandbox.BackendFromAppConfig(cfg)
		closeBackend = closeFn
		if e != nil {
			return nil, e
		}
		files, ok := b.(entstore.BotRestoreBackend)
		if !ok {
			return nil, store.ErrBotUnsupported
		}
		return files, nil
	}})
	if err != nil {
		return err
	}
	fmt.Println("Uncommitted restore destinations cleaned. Retry restore with the original archive and mappings.")
	return nil
}
