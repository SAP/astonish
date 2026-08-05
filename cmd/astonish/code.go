package astonish

import (
	"context"
	"flag"
	"fmt"

	"github.com/SAP/astonish/pkg/launcher"
)

// codeFlags holds the parsed CLI flag values for `astonish code`.
type codeFlags struct {
	Provider    string
	Model       string
	Dir         string
	AutoApprove bool
	Debug       bool
	Resume      string
}

// parseCodeFlags parses the argv slice into a codeFlags struct. The -m/--model
// flag accepts either a bare model name or a "provider:model" pin; the latter
// is split into Provider/Model.
func parseCodeFlags(args []string) (codeFlags, error) {
	codeCmd := flag.NewFlagSet("code", flag.ContinueOnError)

	modelPin := codeCmd.String("model", "", "Model to use, as a bare model name or provider:model pin")
	dir := codeCmd.String("dir", "", "Working directory tools operate against (default: current directory)")
	autoApprove := codeCmd.Bool("auto-approve", false, "Auto-approve all tool executions")
	yolo := codeCmd.Bool("yolo", false, "Alias for --auto-approve")
	debugMode := codeCmd.Bool("debug", false, "Enable debug output")
	resumeSession := codeCmd.String("resume", "", "Resume an existing session by ID")

	codeCmd.StringVar(modelPin, "m", "", "Model or provider:model (short)")
	codeCmd.StringVar(dir, "C", "", "Working directory (short)")
	codeCmd.StringVar(resumeSession, "r", "", "Resume session (short)")

	if err := codeCmd.Parse(args); err != nil {
		return codeFlags{}, err
	}

	f := codeFlags{
		Dir:         *dir,
		AutoApprove: *autoApprove || *yolo,
		Debug:       *debugMode,
		Resume:      *resumeSession,
	}

	// A -m value may be either "provider:model" or a bare model name.
	if *modelPin != "" {
		if provider, model, err := parseModelPin(*modelPin); err == nil {
			f.Provider = provider
			f.Model = model
		} else {
			f.Model = *modelPin
		}
	}

	return f, nil
}

// handleCodeCommand implements `astonish code`: a fully local, in-process
// coding tool. Unlike `astonish chat`, it never contacts a platform — the
// single binary runs the agent loop in-process and calls the compiled-in tools
// directly against the host filesystem in the working directory. The sandbox
// is forced off so tools execute with the user's own permissions.
func handleCodeCommand(args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		printCodeUsage()
		return nil
	}

	flags, err := parseCodeFlags(args)
	if err != nil {
		return err
	}

	cfg := &launcher.CodeConfig{
		Provider:    flags.Provider,
		Model:       flags.Model,
		WorkingDir:  flags.Dir,
		AutoApprove: flags.AutoApprove,
		DebugMode:   flags.Debug,
		SessionID:   flags.Resume,
	}
	return launcher.RunCodeTUI(context.Background(), cfg)
}

func printCodeUsage() {
	fmt.Println("usage: astonish code [options]")
	fmt.Println("")
	fmt.Println("Run Astonish as a local coding tool. The binary runs the agent loop")
	fmt.Println("in-process and executes its built-in tools directly on your machine,")
	fmt.Println("in the working directory. No platform, daemon, or login required.")
	fmt.Println("")
	fmt.Println("Tools run unsandboxed with your own permissions; safety comes from the")
	fmt.Println("per-tool approval prompt. Use --auto-approve to skip it.")
	fmt.Println("")
	fmt.Println("Inside the app, /rollback reverts both the conversation and any file")
	fmt.Println("changes back to an earlier message.")
	fmt.Println("")
	fmt.Println("options:")
	fmt.Println("  -m, --model         Model to use (bare name or provider:model pin)")
	fmt.Println("  -C, --dir           Working directory (default: current directory)")
	fmt.Println("  -r, --resume        Resume an existing session by ID")
	fmt.Println("  --auto-approve      Auto-approve all tool executions")
	fmt.Println("  --yolo              Alias for --auto-approve")
	fmt.Println("  --debug             Enable debug output")
	fmt.Println("  -h, --help          Show this help message")
	fmt.Println("")
	fmt.Println("examples:")
	fmt.Println("  astonish code")
	fmt.Println("  astonish code -m openai:gpt-4o")
	fmt.Println("  astonish code -C ./my-project --auto-approve")
	fmt.Println("  astonish code --resume <session-id>")
}
