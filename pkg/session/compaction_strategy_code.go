package session

import (
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// CodeStrategy produces structured summaries tailored for coding sessions.
// It extracts file paths from tool calls and produces a 7-section summary
// (Objective, Files Modified, Tasks Completed, Tasks Pending, Key Decisions,
// Errors & Fixes, Current State) that preserves critical project context.
type CodeStrategy struct {
	// SessionNotes, when non-nil and non-empty, provides a pre-built summary
	// from incremental session tracking. The LLM is instructed to use these
	// as the primary source and only supplement from conversation gaps.
	SessionNotes *SessionNotes
}

func (s *CodeStrategy) Name() string { return "code" }

func (s *CodeStrategy) BuildSummarizationPrompt(contents []*genai.Content) string {
	var sb strings.Builder

	// If we have pre-built session notes, prepend them as primary source
	if s.SessionNotes != nil && !s.SessionNotes.IsEmpty() {
		sb.WriteString("[Pre-built session state — use as primary source, supplement from conversation only if gaps exist]\n")
		sb.WriteString(s.SessionNotes.Render())
		sb.WriteString("\n\n---\n\n")
	}

	sb.WriteString("You are summarizing a coding session. Produce a structured summary with EXACTLY these sections:\n\n")
	sb.WriteString("## OBJECTIVE\nWhat the user is trying to accomplish (the overall goal of this session).\n\n")
	sb.WriteString("## FILES MODIFIED\nList every file path that was created/edited/deleted with a one-line description of each change. Format: `path/to/file` — description\n\n")
	sb.WriteString("## TASKS COMPLETED\nBulleted list of work items that have been finished successfully.\n\n")
	sb.WriteString("## TASKS PENDING\nBulleted list of work still to be done (unfinished items, known next steps, anything the model was about to do).\n\n")
	sb.WriteString("## KEY DECISIONS\nTechnical decisions made during this session (architecture choices, library selections, API designs, naming conventions).\n\n")
	sb.WriteString("## ERRORS & FIXES\nErrors encountered and how they were resolved. Note any that are still unresolved.\n\n")
	sb.WriteString("## CURRENT STATE\nWhat was the model actively doing when this conversation segment ended? What file was open? What was the immediate next step?\n\n")
	sb.WriteString("---\n\n")

	// Extract file paths from tool calls to provide structured data
	filePaths := extractFilePathsFromContents(contents)
	if len(filePaths) > 0 {
		sb.WriteString("[Files touched in this session (extracted from tool calls)]:\n")
		for path, action := range filePaths {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", path, action))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("[Conversation to summarize]:\n\n")

	// Render the conversation with more detail for code sessions
	var lastToolName string
	var toolRepeatCount int

	flushToolRepeat := func() {
		if toolRepeatCount > 0 {
			if toolRepeatCount == 1 {
				sb.WriteString(fmt.Sprintf("[model] Called tool: %s\n[tool] %s responded\n", lastToolName, lastToolName))
			} else {
				sb.WriteString(fmt.Sprintf("[model] Called tool: %s (×%d repeated calls)\n", lastToolName, toolRepeatCount))
			}
			toolRepeatCount = 0
			lastToolName = ""
		}
	}

	for _, content := range contents {
		if content == nil {
			continue
		}
		role := content.Role
		if role == "" {
			role = "system"
		}
		for _, p := range content.Parts {
			if p == nil {
				continue
			}
			if p.Text != "" {
				flushToolRepeat()
				// Code sessions get a bit more text budget per message
				sb.WriteString(fmt.Sprintf("[%s]: %s\n", role, truncateText(p.Text, 800)))
			}
			if p.FunctionCall != nil {
				if p.FunctionCall.Name == lastToolName {
					toolRepeatCount++
				} else {
					flushToolRepeat()
					lastToolName = p.FunctionCall.Name
					toolRepeatCount = 1
				}
			}
			if p.FunctionResponse != nil {
				if p.FunctionResponse.Name != lastToolName {
					flushToolRepeat()
					sb.WriteString(fmt.Sprintf("[tool] %s responded\n", p.FunctionResponse.Name))
				}
			}
		}
	}
	flushToolRepeat()

	prompt := sb.String()
	// Code sessions are denser — allow larger prompt
	if len(prompt) > 40000 {
		prompt = prompt[:40000] + "\n\n[... truncated for summarization ...]"
	}
	return prompt
}

// extractFilePathsFromContents scans tool call arguments for known file-path
// parameters, returning a map of path → last action performed on it.
func extractFilePathsFromContents(contents []*genai.Content) map[string]string {
	paths := make(map[string]string)

	for _, content := range contents {
		if content == nil {
			continue
		}
		for _, p := range content.Parts {
			if p == nil || p.FunctionCall == nil {
				continue
			}
			fc := p.FunctionCall
			switch fc.Name {
			case "edit_file":
				if path, ok := fc.Args["path"].(string); ok && path != "" {
					paths[path] = "edited"
				}
			case "write_file":
				if path, ok := fc.Args["file_path"].(string); ok && path != "" {
					paths[path] = "written"
				}
			case "read_file":
				if path, ok := fc.Args["path"].(string); ok && path != "" {
					if _, exists := paths[path]; !exists {
						paths[path] = "read"
					}
				}
			case "shell_command":
				if dir, ok := fc.Args["working_dir"].(string); ok && dir != "" {
					if _, exists := paths[dir]; !exists {
						paths[dir] = "shell (working_dir)"
					}
				}
			case "find_files", "grep_search":
				if path, ok := fc.Args["search_path"].(string); ok && path != "" {
					if _, exists := paths[path]; !exists {
						paths[path] = "searched"
					}
				}
			}
		}
	}
	return paths
}
