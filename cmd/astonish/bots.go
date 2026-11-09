package astonish

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const botsUsage = `usage: astonish bots <command> [flags] [Bot UUID or unique name]
Commands: list, create draft, show, run, stop-run, test-once, conversation,
          pause, resume, stop, delete, restore, logs, approvals, approval approve|deny
Flags may precede or follow positional arguments:
  --scope personal|team  --json
  create draft NAME [--charter TEXT] [--start-from-title] [--key IDEMPOTENCY_KEY]
  run BOT [--version N] [--instructions TEXT] [--key IDEMPOTENCY_KEY]
  test-once|conversation BOT --version N --confirm SCOPE:BOT_UUID [--instructions TEXT] [--key KEY]
  pause|resume|stop|delete|restore BOT --revision N --reason TEXT
  resume BOT --run RUN_UUID --approval APPROVAL_UUID
  stop-run BOT --run RUN_UUID --confirm SCOPE:BOT_UUID:RUN_UUID
  logs BOT [--session UUID] [--follow] [--after SESSION_UUID:CURSOR]
  approval approve|deny BOT APPROVAL_UUID --digest EXACT_DIGEST [--response JSON]
Test-once requires Draft and leaves it Draft; conversation requires Paused and uses no tools.
Neither command activates a Bot. --confirm binds the selected scope and exact Bot UUID.
Resume activates a reviewed draft/paused Bot; --run resumes an approved checkpoint.
Detach from logs or chat never stops a Bot. Decisions never auto-resume work.`

func handleBotsCommand(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(botsUsage)
		return nil
	}
	if !client.IsRemoteMode() {
		return fmt.Errorf("bots requires platform login; run: astonish login <url>")
	}
	c, err := client.New()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	return runBotsCommand(ctx, c, args, os.Stdout)
}
func runBotsCommand(ctx context.Context, c *client.Client, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", botsUsage)
	}
	command := args[0]
	switch command {
	case "list", "create", "show", "run", "stop-run", "test-once", "conversation", "pause", "resume", "stop", "delete", "restore", "logs", "approvals", "approval":
	default:
		return fmt.Errorf("unknown bots command %q", command)
	}
	args = args[1:]
	decision := ""
	if command == "create" {
		if len(args) == 0 || args[0] != "draft" {
			return fmt.Errorf("use: bots create draft NAME --charter TEXT")
		}
		args = args[1:]
	}
	if command == "approval" {
		if len(args) == 0 || (args[0] != "approve" && args[0] != "deny") {
			return fmt.Errorf("use: bots approval approve|deny BOT APPROVAL_UUID --digest DIGEST")
		}
		decision = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("bots "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	scope := fs.String("scope", "personal", "Bot scope")
	asJSON := fs.Bool("json", false, "JSON output")
	startFromTitle := fs.Bool("start-from-title", false, "explicitly start an interactive conversation from the title")
	charter := fs.String("charter", "", "draft charter")
	key := fs.String("key", "", "idempotency key")
	revision := fs.Int64("revision", 0, "reviewed revision")
	version := fs.Int64("version", 0, "reviewed version")
	confirm := fs.String("confirm", "", "confirm exact SCOPE:BOT_UUID for test-once/conversation")
	reason := fs.String("reason", "", "review reason")
	instructions := fs.String("instructions", "", "run instructions")
	session := fs.String("session", "", "transcript session")
	follow := fs.Bool("follow", false, "follow persisted events")
	after := fs.String("after", "", "durable event cursor")
	digest := fs.String("digest", "", "reviewed action digest")
	response := fs.String("response", "", "typed interaction response JSON")
	runID := fs.String("run", "", "run UUID")
	approvalID := fs.String("approval", "", "approved checkpoint UUID")
	// flag.FlagSet stops at the first positional. Partition known flags so the
	// documented noun-first CLI works without silently dropping trailing flags.
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
		if !strings.Contains(a, "=") && name != "json" && name != "follow" && name != "start-from-title" {
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", a)
			}
			i++
			flags = append(flags, args[i])
		}
	}
	if err := fs.Parse(flags); err != nil {
		return err
	}
	b := client.Bots{Client: c, Scope: store.BotScope(*scope)}
	print := func(v any) error {
		enc := json.NewEncoder(out)
		if !*asJSON {
			enc.SetIndent("", "  ")
		}
		return enc.Encode(v)
	}
	if command == "list" {
		if len(pos) != 0 {
			return fmt.Errorf("list takes no Bot")
		}
		v, err := b.List(ctx)
		if err != nil {
			return err
		}
		return print(v)
	}
	if len(pos) == 0 {
		return fmt.Errorf("Bot UUID or unique name required")
	}
	if command == "create" {
		if len(pos) != 1 || (!*startFromTitle && strings.TrimSpace(*charter) == "") {
			return fmt.Errorf("draft name and --charter required")
		}
		if *key == "" {
			*key = uuid.NewString()
		}
		v, err := b.Create(ctx, client.BotDraft{StartFromTitle: *startFromTitle, Name: pos[0], IdempotencyKey: *key, Definition: client.BotDraftDefinition{Charter: *charter}})
		if err != nil {
			return err
		}
		return print(v)
	}
	if command != "approval" && len(pos) != 1 {
		return fmt.Errorf("unexpected arguments")
	}
	bot, err := b.Resolve(ctx, pos[0])
	if err != nil {
		return err
	}
	id := bot.ID.String()
	switch command {
	case "stop-run":
		rid, parseErr := uuid.Parse(*runID)
		if parseErr != nil || rid == uuid.Nil {
			return fmt.Errorf("stop-run requires an exact --run RUN_UUID")
		}
		expected := string(b.Scope) + ":" + id + ":" + rid.String()
		if *confirm != expected {
			return fmt.Errorf("stop-run requires reviewed exact confirmation --confirm %s; this cancels only this run, not the Bot", expected)
		}
		runs, err := b.Runs(ctx, id)
		if err != nil {
			return err
		}
		found := false
		for _, run := range runs {
			if run.ID == rid && run.BotID == bot.ID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("run does not belong to the selected Bot; reload and review")
		}
		v, err := b.CancelRun(ctx, id, rid.String())
		if err != nil {
			return err
		}
		return print(v)
	case "show":
		return print(bot)
	case "test-once", "conversation":
		if *version < 1 || *version != bot.Version {
			return fmt.Errorf("%s requires explicitly reviewed --version %d; reload and review the Bot", command, bot.Version)
		}
		expected := string(b.Scope) + ":" + id
		if *confirm != expected {
			return fmt.Errorf("%s requires scoped confirmation --confirm %s", command, expected)
		}
		call := b.TestRun
		want := store.BotDraft
		if command == "conversation" {
			call, want = b.Conversation, store.BotPaused
		}
		if bot.Status != want {
			return fmt.Errorf("%s requires a %s Bot (currently %s); no activation was performed", command, want, bot.Status)
		}
		if *key == "" {
			*key = uuid.NewString()
		}
		v, err := call(ctx, id, *version, *key, *instructions)
		if err != nil {
			return err
		}
		return print(v)
	case "run":
		if *key == "" {
			*key = uuid.NewString()
		}
		if *version == 0 {
			*version = bot.Version
		}
		v, err := b.Run(ctx, id, *version, *key, *instructions)
		if err != nil {
			return err
		}
		return print(v)
	case "pause", "resume", "stop", "delete", "restore":
		if command == "resume" && *runID != "" {
			if *approvalID == "" {
				return fmt.Errorf("--approval required for checkpoint resume")
			}
			if err := b.ResumeRun(ctx, id, *runID, *approvalID); err != nil {
				return err
			}
			return print(map[string]string{"status": "accepted"})
		}
		action := client.BotLifecycle(command)
		if command == "resume" {
			action = client.BotResume
		}
		v, err := b.Lifecycle(ctx, id, action, *revision, *reason)
		if err != nil {
			return err
		}
		return print(v)
	case "approvals":
		v, err := b.Approvals(ctx, id)
		if err != nil {
			return err
		}
		return print(v)
	case "approval":
		if len(pos) != 2 {
			return fmt.Errorf("exact approval UUID required")
		}
		status := store.ApprovalDenied
		if decision == "approve" {
			status = store.ApprovalApproved
		}
		q := client.BotDecision{ActionDigest: *digest, Status: status}
		if *response != "" {
			q.Response = &store.BotInteractionResponse{}
			d := json.NewDecoder(strings.NewReader(*response))
			d.DisallowUnknownFields()
			if err := d.Decode(q.Response); err != nil {
				return err
			}
			if err := d.Decode(new(any)); err != io.EOF {
				return fmt.Errorf("--response must contain exactly one JSON object")
			}
		}
		v, err := b.Decide(ctx, id, pos[1], q)
		if err != nil {
			return err
		}
		return print(v)
	case "logs":
		if *session == "" {
			*session = bot.HomeSessionID.String()
		}
		if *session == uuid.Nil.String() {
			return fmt.Errorf("Bot has no transcript session yet")
		}
		err := b.Watch(ctx, id, *session, *after, *follow, func(ev client.SSEEvent) error {
			if *asJSON {
				return print(ev)
			}
			_, err := fmt.Fprintf(out, "%s %s\n%s\n", ev.ID, ev.Type, ev.Data)
			return err
		})
		if ctx.Err() != nil {
			return nil
		}
		return err
	default:
		return fmt.Errorf("unknown bots command %q", command)
	}
}
