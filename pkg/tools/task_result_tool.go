package tools

import (
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// ReadTaskResultArgs is the input schema for the read_task_result tool.
type ReadTaskResultArgs struct {
	ResultID string `json:"result_id" jsonschema:"The ID of the task result to retrieve (returned by delegate_tasks in the full_result_id field)."`
}

// ReadTaskResultOutput is the output schema for the read_task_result tool.
type ReadTaskResultOutput struct {
	Content   string `json:"content,omitempty"`
	TaskName  string `json:"task_name,omitempty"`
	CharCount int    `json:"char_count,omitempty"`
	Error     string `json:"error,omitempty"`
}

func readTaskResult(ctx tool.Context, args ReadTaskResultArgs) (ReadTaskResultOutput, error) {
	store := taskResultsFromContext(ctx)

	if args.ResultID == "" {
		// List available results
		available := store.List()
		if len(available) == 0 {
			return ReadTaskResultOutput{
				Error: "No task results are stored. Results are only available after delegate_tasks produces results that were summarized due to length.",
			}, nil
		}
		// Return a listing with metadata
		var listing string
		for id, meta := range available {
			listing += id + ": " + meta["task_name"].(string) + " (" +
				meta["summary"].(string) + ")\n"
		}
		return ReadTaskResultOutput{
			Content: "Available task results:\n" + listing,
		}, nil
	}

	content, ok := store.Get(args.ResultID)
	if !ok {
		return ReadTaskResultOutput{
			Error: "Task result not found. It may have expired (results are kept for 30 minutes) or the ID is incorrect.",
		}, nil
	}

	taskName, charCount, _ := store.GetMeta(args.ResultID)
	return ReadTaskResultOutput{
		Content:   content,
		TaskName:  taskName,
		CharCount: charCount,
	}, nil
}

// NewReadTaskResultTool creates the read_task_result tool.
func NewReadTaskResultTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "read_task_result",
		Description: `Retrieve the full output of a delegated sub-task. When delegate_tasks returns summarized results (for large outputs), it includes a full_result_id that you can use here to get the complete text. Call with an empty result_id to list available results. Use this when you need the full details of a sub-task's output for synthesis or to quote specific information.`,
	}, readTaskResult)
}
