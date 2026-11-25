package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotFileFirstFactoryMandatoryFiles(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.services.BotFilesFactory = nil
	if resources, err := f.construct(t); resources != nil || !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("missing files accepted: %v", err)
	}
}
func TestBotFileFirstFactoryConfinedViews(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "autonomous", true: "interactive"}[interactive], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			if interactive {
				f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
				f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
				f.req.Authorization = interactiveFactoryAuthorization{}
				f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
					return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
				}
			}
			var safe *BotChatSafeResources
			prepare := f.cfg.Prepare
			f.cfg.Prepare = func(ctx context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
				var err error
				safe, err = prepare(ctx, r, s)
				return safe, err
			}
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			if safe.FileReceipts == nil || len(safe.FileReceipts.Snapshot()) != 0 {
				t.Fatal("missing fresh trusted receipts")
			}
			for _, name := range []string{"read_file", "write_file", "edit_file", "find_files", "file_tree", "publish_file"} {
				entry := resources.Agent.ToolIndex.GetToolEntry(name)
				if entry == nil {
					t.Fatalf("missing scoped file tool %s", name)
				}
				wrapped, ok := entry.Tool.(*chatRuntimeTool)
				if !ok || wrapped.binding == nil || wrapped.binding.hooks.Dispatch == nil {
					t.Fatalf("unbound %s: %T", name, entry.Tool)
				}
				if name == "publish_file" {
					if _, ok := wrapped.chatRuntimeRunnable.(*botConfinedFileTool); !ok {
						t.Fatalf("unconfined publication: %T", wrapped.chatRuntimeRunnable)
					}
				} else if _, ok := wrapped.chatRuntimeRunnable.(*sandbox.NodeTool); !ok {
					t.Fatalf("ordinary file tool bypasses scoped node: %s %T", name, wrapped.chatRuntimeRunnable)
				}
			}
		})
	}
}
