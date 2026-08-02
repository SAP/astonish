package tools

// ToolDeclaration holds metadata about a tool for listing purposes.
// This avoids constructing actual tool instances (which may have side effects
// or require runtime dependencies) just to list tool names and descriptions.
type ToolDeclaration struct {
	Name        string
	Description string
	Category    string // source/group category: "internal", "browser", "credentials", etc.
}

// GetAllFlowToolDeclarations returns declarations for all tools that can be used
// in flow tools_selection. This excludes chat-only tools (delegate_tasks,
// announce_plan, read_task_result, run_flow, search_flows, search_tools).
//
// The returned list is deterministic and requires no runtime dependencies.
func GetAllFlowToolDeclarations() []ToolDeclaration {
	var decls []ToolDeclaration

	// Internal tools (15)
	decls = append(decls, internalToolDeclarations()...)

	// Process tools (4)
	decls = append(decls, processToolDeclarations()...)

	// Credential tools (5)
	decls = append(decls, credentialToolDeclarations()...)

	// Scheduler tools (4)
	decls = append(decls, schedulerToolDeclarations()...)

	// Browser tools (~31)
	decls = append(decls, browserToolDeclarations()...)

	// Email tools (8)
	decls = append(decls, emailToolDeclarations()...)

	// Distill tools (1)
	decls = append(decls, distillToolDeclarations()...)

	// Drill tools (7)
	decls = append(decls, drillToolDeclarations()...)

	// Fleet tools (2)
	decls = append(decls, fleetToolDeclarations()...)

	// Memory tools (4)
	decls = append(decls, memoryToolDeclarations()...)

	// Skill lookup tool (1)
	decls = append(decls, ToolDeclaration{
		Name:        "skill_lookup",
		Description: "Load full instructions for a CLI tool skill by name. Returns detailed workflow and usage guidelines.",
		Category:    "skill",
	})

	// Sandbox template tools (3)
	decls = append(decls, sandboxToolDeclarations()...)

	return decls
}

func internalToolDeclarations() []ToolDeclaration {
	return []ToolDeclaration{
		{Name: "read_file", Description: "Read file contents with optional line range", Category: "internal"},
		{Name: "write_file", Description: "Write content to a file (creates or overwrites)", Category: "internal"},
		{Name: "shell_command", Description: "Execute a shell command with PTY support", Category: "internal"},
		{Name: "filter_json", Description: "Filter and transform JSON data using jq-like expressions", Category: "internal"},
		{Name: "git_diff_add_line_numbers", Description: "Add line numbers to git diff output for precise editing", Category: "internal"},
		{Name: "file_tree", Description: "Budgeted directory tree with subtree summaries for project orientation", Category: "internal"},
		{Name: "grep_search", Description: "Search for text/regex patterns with context lines, type filters, and glob support", Category: "internal"},
		{Name: "find_files", Description: "Find files by glob pattern with .gitignore respect and mtime sorting", Category: "internal"},
		{Name: "edit_file", Description: "Edit a file by finding and replacing text", Category: "internal"},
		{Name: "repo_map", Description: "Build a structural source map using tree-sitter definitions and references", Category: "internal"},
		{Name: "code_definition", Description: "Find structural definitions of a symbol using tree-sitter", Category: "internal"},
		{Name: "code_references", Description: "Find structural references to a symbol using tree-sitter", Category: "internal"},
		{Name: "web_fetch", Description: "Fetch and extract content from a URL", Category: "internal"},
		{Name: "perplexity_web_search", Description: "Search the web using the configured Perplexity/Sonar model", Category: "internal"},
		{Name: "read_pdf", Description: "Extract text content from a PDF file", Category: "internal"},
		{Name: "http_request", Description: "Make an HTTP request with full control over method, headers, and body", Category: "internal"},
	}
}

func processToolDeclarations() []ToolDeclaration {
	return []ToolDeclaration{
		{Name: "process_read", Description: "Read output from a running background process", Category: "process"},
		{Name: "process_write", Description: "Send input to a running background process", Category: "process"},
		{Name: "process_list", Description: "List active and recent background processes", Category: "process"},
		{Name: "process_kill", Description: "Kill a running background process", Category: "process"},
	}
}

func credentialToolDeclarations() []ToolDeclaration {
	return []ToolDeclaration{
		{Name: "save_credential", Description: "Save a credential to the encrypted credential store", Category: "credentials"},
		{Name: "list_credentials", Description: "List stored credentials with their types", Category: "credentials"},
		{Name: "remove_credential", Description: "Remove a credential from the store", Category: "credentials"},
		{Name: "test_credential", Description: "Test a credential by making an authenticated request", Category: "credentials"},
		{Name: "resolve_credential", Description: "Retrieve fields of a stored credential for use in requests", Category: "credentials"},
	}
}

func schedulerToolDeclarations() []ToolDeclaration {
	return []ToolDeclaration{
		{Name: "schedule_job", Description: "Create a scheduled job that runs on a cron schedule", Category: "scheduler"},
		{Name: "list_scheduled_jobs", Description: "List all scheduled jobs with their status", Category: "scheduler"},
		{Name: "remove_scheduled_job", Description: "Remove a scheduled job", Category: "scheduler"},
		{Name: "update_scheduled_job", Description: "Update a scheduled job's schedule or configuration", Category: "scheduler"},
	}
}

func browserToolDeclarations() []ToolDeclaration {
	return []ToolDeclaration{
		{Name: "browser_navigate", Description: "Navigate the browser to a URL (sandbox: use localhost/127.0.0.1, not the container bridge IP)", Category: "browser"},
		{Name: "browser_navigate_back", Description: "Go back to the previous page in browser history", Category: "browser"},
		{Name: "browser_click", Description: "Click an element on the page", Category: "browser"},
		{Name: "browser_type", Description: "Type text into an input element", Category: "browser"},
		{Name: "browser_hover", Description: "Hover over an element", Category: "browser"},
		{Name: "browser_drag", Description: "Drag an element to a target", Category: "browser"},
		{Name: "browser_press_key", Description: "Press a keyboard key or key combination", Category: "browser"},
		{Name: "browser_select_option", Description: "Select an option from a dropdown", Category: "browser"},
		{Name: "browser_fill_form", Description: "Fill multiple form fields at once", Category: "browser"},
		{Name: "browser_highlight", Description: "Draw a visible highlight overlay around an element", Category: "browser"},
		{Name: "browser_clear_highlights", Description: "Remove all highlight overlays", Category: "browser"},
		{Name: "browser_move_cursor", Description: "Move the visible demo cursor to an element or coordinates", Category: "browser"},
		{Name: "browser_snapshot", Description: "Get accessibility tree snapshot of the page", Category: "browser"},
		{Name: "browser_take_screenshot", Description: "Take a screenshot of the current page", Category: "browser"},
		{Name: "browser_console_messages", Description: "Get browser console messages", Category: "browser"},
		{Name: "browser_network_requests", Description: "Get network requests made by the page", Category: "browser"},
		{Name: "browser_tabs", Description: "Manage browser tabs (list, create, switch, close)", Category: "browser"},
		{Name: "browser_close", Description: "Close the current page or tab", Category: "browser"},
		{Name: "browser_resize", Description: "Resize the browser viewport", Category: "browser"},
		{Name: "browser_fullscreen", Description: "Enter or exit Chromium window fullscreen for recording", Category: "browser"},
		{Name: "browser_wait_for", Description: "Wait for a condition (element, network idle, timeout)", Category: "browser"},
		{Name: "browser_pause", Description: "Pause for a fixed duration (tutorial pacing)", Category: "browser"},
		{Name: "browser_file_upload", Description: "Upload files to a file input element", Category: "browser"},
		{Name: "browser_handle_dialog", Description: "Handle a native browser dialog (alert, confirm, prompt)", Category: "browser"},
		{Name: "browser_evaluate", Description: "Evaluate a JavaScript expression in the page", Category: "browser"},
		{Name: "browser_run_code", Description: "Run a JavaScript code snippet in the page", Category: "browser"},
		{Name: "browser_pdf", Description: "Save the current page as a PDF", Category: "browser"},
		{Name: "browser_response_body", Description: "Intercept and return HTTP response body", Category: "browser"},
		{Name: "browser_cookies", Description: "Manage browser cookies (get, set, delete)", Category: "browser"},
		{Name: "browser_storage", Description: "Manage localStorage and sessionStorage", Category: "browser"},
		{Name: "browser_set_offline", Description: "Simulate offline/online network mode", Category: "browser"},
		{Name: "browser_set_headers", Description: "Set extra HTTP headers for all requests", Category: "browser"},
		{Name: "browser_set_credentials", Description: "Set HTTP Basic Auth credentials", Category: "browser"},
		{Name: "browser_set_geolocation", Description: "Override browser geolocation", Category: "browser"},
		{Name: "browser_set_media", Description: "Set color scheme preference (light/dark)", Category: "browser"},
		{Name: "browser_set_timezone", Description: "Override browser timezone", Category: "browser"},
		{Name: "browser_set_locale", Description: "Override browser locale", Category: "browser"},
		{Name: "browser_set_device", Description: "Emulate a mobile device", Category: "browser"},
		{Name: "browser_request_human", Description: "Request human-in-the-loop browser interaction", Category: "browser"},
		{Name: "browser_start_action_capture", Description: "Start DOM action capture for drill authoring", Category: "browser"},
		{Name: "browser_stop_action_capture", Description: "Stop DOM action capture", Category: "browser"},
		{Name: "browser_get_action_log", Description: "Get captured DOM action log JSON", Category: "browser"},
		{Name: "browser_clear_action_log", Description: "Clear the DOM action capture log", Category: "browser"},
		{Name: "browser_start_recording", Description: "Start recording the browser display to an MP4", Category: "browser"},
		{Name: "browser_stop_recording", Description: "Stop browser display recording and finalize the MP4", Category: "browser"},
		{Name: "browser_recording_status", Description: "Check whether a browser display recording is in progress", Category: "browser"},
	}
}

func emailToolDeclarations() []ToolDeclaration {
	return []ToolDeclaration{
		{Name: "email_list", Description: "List emails in the inbox", Category: "email"},
		{Name: "email_read", Description: "Read full email content by ID", Category: "email"},
		{Name: "email_search", Description: "Search emails by query", Category: "email"},
		{Name: "email_send", Description: "Send a new email", Category: "email"},
		{Name: "email_reply", Description: "Reply to an email", Category: "email"},
		{Name: "email_mark_read", Description: "Mark email as read or unread", Category: "email"},
		{Name: "email_delete", Description: "Delete emails", Category: "email"},
		{Name: "email_wait", Description: "Wait for an email matching criteria", Category: "email"},
	}
}

func distillToolDeclarations() []ToolDeclaration {
	return []ToolDeclaration{
		{Name: "distill_flow", Description: "Distill a conversation into a reusable flow", Category: "distill"},
	}
}

func drillToolDeclarations() []ToolDeclaration {
	return []ToolDeclaration{
		{Name: "save_drill", Description: "Save a drill suite with test files", Category: "drill"},
		{Name: "validate_drill", Description: "Validate a drill suite configuration", Category: "drill"},
		{Name: "delete_drill", Description: "Delete a drill suite or individual drill file", Category: "drill"},
		{Name: "list_drills", Description: "List available drill suites", Category: "drill"},
		{Name: "read_drill", Description: "Read drill YAML content", Category: "drill"},
		{Name: "edit_drill", Description: "Edit an existing drill file", Category: "drill"},
		{Name: "run_drill", Description: "Run a drill suite and return the test report", Category: "drill"},
		{Name: "inject_drill_credentials", Description: "Inject suite credentials into the sandbox before start-services", Category: "drill"},
		{Name: "draft_drill_from_action_log", Description: "Convert a browser action capture log into a mode-neutral drill YAML skeleton", Category: "drill"},
		{Name: "draft_tutorial_blueprint", Description: "Draft a HeyGen-style Scene|Voiceover|Visual tutorial blueprint", Category: "drill"},
		{Name: "validate_tutorial_blueprint", Description: "Validate a tutorial video blueprint", Category: "drill"},
		{Name: "present_tutorial_blueprint", Description: "Present a tutorial blueprint for in-chat approval", Category: "drill"},
		{Name: "blueprint_to_tutorial_drill", Description: "Convert approved blueprint screen scenes to tutorial drill YAML", Category: "drill"},
	}
}

func fleetToolDeclarations() []ToolDeclaration {
	return []ToolDeclaration{
		{Name: "save_fleet_plan", Description: "Save a fleet plan configuration for parallel agent execution", Category: "fleet"},
		{Name: "validate_fleet_plan", Description: "Validate fleet plan connections and dependencies", Category: "fleet"},
		{Name: "update_setup_draft", Description: "Update in-progress fleet setup draft collected values", Category: "fleet"},
		{Name: "get_setup_profile", Description: "Load fleet setup profile definition and draft progress", Category: "fleet"},
	}
}

func memoryToolDeclarations() []ToolDeclaration {
	return []ToolDeclaration{
		{Name: "memory_save", Description: "Save durable facts to persistent memory", Category: "memory"},
		{Name: "memory_search", Description: "Search memory for relevant knowledge", Category: "memory"},
		{Name: "memory_get", Description: "Read full memory file content by path", Category: "memory"},
		{Name: "memory_delete", Description: "Delete an existing memory by exact ID", Category: "memory"},
	}
}

func sandboxToolDeclarations() []ToolDeclaration {
	return []ToolDeclaration{
		{Name: "save_sandbox_template", Description: "Freeze the current sandbox container as a reusable template", Category: "sandbox"},
		{Name: "list_sandbox_templates", Description: "List available sandbox templates", Category: "sandbox"},
		{Name: "use_sandbox_template", Description: "Launch a sandbox container from a saved template", Category: "sandbox"},
	}
}
