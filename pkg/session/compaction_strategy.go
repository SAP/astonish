package session

import (
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// CompactionStrategy controls how the old conversation portion is summarized.
// Implementations are mode-specific: code mode produces structured file/task
// tracking summaries; platform mode uses the existing generic prompt.
type CompactionStrategy interface {
	// BuildSummarizationPrompt constructs the LLM prompt for summarizing
	// the old conversation portion. The contents slice is the old portion
	// that will be summarized (already split by CompactContents).
	BuildSummarizationPrompt(contents []*genai.Content) string
	// Name returns a short identifier for logging/events ("code", "platform").
	Name() string
}

// GenericStrategy is the default summarization strategy. It produces the
// existing CURRENT TASK/PROGRESS/COMPLETED format suitable for platform/chat
// conversations. This wraps the original inline prompt logic from summarize().
type GenericStrategy struct{}

func (s *GenericStrategy) Name() string { return "platform" }

func (s *GenericStrategy) BuildSummarizationPrompt(contents []*genai.Content) string {
	var sb strings.Builder
	sb.WriteString("Summarize the following conversation history. Focus on:\n")
	sb.WriteString("1. CURRENT TASK: What is the user's most recent request? What was the model actively working on?\n")
	sb.WriteString("2. PROGRESS: What has been accomplished so far? What step was the model on when this history ends?\n")
	sb.WriteString("3. KEY FACTS: Important decisions, file paths, variable names, and outcomes.\n")
	sb.WriteString("4. COMPLETED WORK: What earlier tasks finished successfully.\n\n")
	sb.WriteString("Start your summary with 'CURRENT TASK:' stating what's actively being worked on.\n")
	sb.WriteString("Then 'PROGRESS:' with what's been done for that task.\n")
	sb.WriteString("Then 'COMPLETED:' listing earlier finished work.\n\n")

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
				sb.WriteString(fmt.Sprintf("[%s]: %s\n", role, truncateText(p.Text, 500)))
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
	if len(prompt) > 30000 {
		prompt = prompt[:30000] + "\n\n[... truncated for summarization ...]"
	}
	return prompt
}
