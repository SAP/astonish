package tui

import (
	"strings"

	"github.com/SAP/astonish/pkg/tui/backend"
)

type codePermissionRow struct {
	label, status, description string
	scope                      backend.CodePermissionScope
	actionable                 bool
}

func (m model) codePermissionRows() []codePermissionRow {
	s := m.permissions.snapshot
	rows := []codePermissionRow{
		{label: "Folders", status: "Ask outside project", description: "Access outside this project requires approval. Allow / Always Allow is available in folder prompts."},
		{label: "Tools", status: "Ask when needed", description: "Tools without an existing approval ask before running. Allow / Always Allow is available in tool prompts."},
		{label: "Plan access", status: "No active plan", description: "Choose full access when approving a plan. It applies only to that plan."},
	}
	if s.SessionAllFolders {
		rows[0].status = "All folders · session"
		rows[0].description = "Enter revokes All folders for this session. Access outside the project will ask again."
		rows[0].scope, rows[0].actionable = backend.CodePermissionSessionAllFolders, true
	}
	if s.AllToolsSession {
		rows[1].status = "All tools · session"
		rows[1].description = "All tools are approved for this session, independently of folder access. This grant cannot be revoked here."
	}
	if _, ok := m.backend.(backend.CodeSessionPermissionsBackend); ok {
		rows[0].scope, rows[0].actionable = backend.CodePermissionSessionAllFolders, true
		rows[1].scope, rows[1].actionable = backend.CodePermissionSessionAllTools, true
		rows[0].description = "Enter allows all folders for this session. Press Enter again to require approval outside the project."
		rows[1].description = "Enter allows all tools for this session. Press Enter again to require approval. Not sandboxed."
		if s.SessionAllFolders {
			rows[0].description = "Enter revokes All folders for this session. Access outside the project will ask again."
		}
		if s.AllToolsSession {
			rows[1].description = "Enter requires tool approval again for this session. Folder access is unchanged."
		}
	}
	if m.codePermissionPlan() != nil {
		rows[2].status = "Standard approvals"
		rows[2].description = "Enter re-enables full access for this active plan after confirmation."
		_, rows[2].actionable = m.backend.(codePlanPermissionEnabler)
	}
	if s.PlanFullAccess {
		rows[0].status, rows[1].status = "All folders · plan", "All tools · plan"
		rows[0].description = "Full access for the active plan permits all folders. Select Plan access to revoke it."
		rows[1].description = "Full access for the active plan permits all tools. Select Plan access to revoke it."
		if rows[0].actionable {
			rows[0].description = "Enter revokes the session's All folders grant. The active plan's full access remains until revoked under Plan access."
		}
		rows[2].status = "Full access"
		rows[2].description = "Enter revokes Full access for this plan. Tool and folder prompts will apply again."
		rows[2].scope, rows[2].actionable = backend.CodePermissionPlanFullAccess, true
	}
	if s.CLIOverride {
		for i := range rows {
			rows[i].status = "CLI override"
			rows[i].description = "Started with --yolo / --auto-approve. Restart without the flag to disable this override."
			rows[i].actionable = false
		}
	}
	return rows
}

func (m model) renderCodePermissionsPicker() string {
	th := m.theme
	w, h := max(40, m.width), max(8, m.height-4)
	var body strings.Builder
	body.WriteString(th.Header.Render("Permissions") + th.Muted.Render("  ↑↓ move  enter select  r refresh  esc close") + "\n\n")
	switch {
	case m.permissions.loading:
		body.WriteString(th.Muted.Render("Loading / saving permissions…"))
	case m.permissions.err != "":
		body.WriteString(th.Error.Render(m.permissions.err) + "\n" + th.Muted.Render("Press r to retry."))
	case !m.permissions.known:
		body.WriteString(th.Muted.Render("Press r to load permissions."))
	default:
		rows := m.codePermissionRows()
		cursor := max(0, min(m.permissions.cursor, len(rows)-1))
		for i, row := range rows {
			mark, style := "  ", th.Text
			if i == cursor {
				mark, style = "› ", th.Success
			}
			body.WriteString(style.Render(mark+row.label) + th.Muted.Render("  "+row.status) + "\n")
		}
		body.WriteString("\n" + th.Muted.Render(wrapPlain(rows[cursor].description, max(1, w-10))))
		if m.permissions.confirm {
			body.WriteString("\n\n" + th.Approval.Render(wrapPlain(codeFullAccessWarning, max(1, w-10))) + "\n" + th.Text.Render("Enter confirms full access; Esc cancels."))
		}
	}
	box := th.InputBorderFocus.Width(w-2).MaxHeight(h).Padding(1, 2).Render(body.String())
	return m.paintCompletionPopup(box, w-2)
}
