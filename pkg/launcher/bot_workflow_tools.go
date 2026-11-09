package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
)

// BotWorkflowTools reuses Chat handlers and their exact schemas. These tools must
// be installed through NewBotChatAgentFactory: the terminal bound wrapper checks
// the live lease, isolation, exact-action approval and tool budget before Run.
// No global registry/client is initialized here.
func BotWorkflowTools(r bot.RunRequest, scope *BotChatScope) ([]tool.Tool, error) {
	if scope == nil || scope.Services == nil || (r.Run.RequiresAutonomousPolicy() && r.Policy == nil) || r.Budget == nil {
		return nil, store.ErrBotUnsupported
	}
	if r.Bot.Scope != store.BotScopePersonal && r.Bot.Scope != store.BotScopeTeam {
		return nil, store.ErrBotInvalid
	}
	svc := *scope.Services
	if r.Bot.Scope == store.BotScopeTeam {
		svc.PersonalDocs = nil
		svc.PersonalCredentials = nil
		svc.PersonalFlows = nil
	}
	var result []tool.Tool
	for _, family := range []struct {
		kind botWorkflowKind
		make func() ([]tool.Tool, error)
	}{
		{botWorkflowCredentials, tools.GetCredentialTools},
		{botWorkflowDrills, tools.GetDrillTools},
		{botWorkflowSlides, slides.GetTools},
		{botWorkflowFleet, tools.GetFleetPlanTools},
		{botWorkflowFleet, tools.GetFleetPlanValidateTools},
		{botWorkflowFleet, tools.GetFleetSetupTools},
		{botWorkflowFleet, tools.GetFleetTools},
	} {
		ts, err := family.make()
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			raw, ok := t.(botRunnableTool)
			if !ok {
				return nil, store.ErrBotUnsupported
			}
			result = append(result, &botWorkflowTool{botRunnableTool: raw, kind: family.kind, req: r, services: svc})
		}
	}
	return result, nil
}

type botWorkflowKind uint8

const (
	botWorkflowCredentials botWorkflowKind = iota
	botWorkflowDrills
	botWorkflowSlides
	botWorkflowFleet
)

type botWorkflowTool struct {
	botRunnableTool
	kind     botWorkflowKind
	req      bot.RunRequest
	services store.Services
}

func (t *botWorkflowTool) unsupported(reason string) (map[string]any, error) {
	return nil, fmt.Errorf("%w: %s: %s", store.ErrBotUnsupported, t.Name(), reason)
}

func (t *botWorkflowTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	// A raw adapter must not become an alternate authority path. Only the private
	// factory wrapper can supply this type, after its native Bot policy guard.
	if _, ok := ctx.(botBoundToolContext); !ok {
		return nil, fmt.Errorf("%w: workflow requires bound tool context", store.ErrBotForbidden)
	}
	id, ok := execution.BotRunIdentityFromContext(ctx)
	want := execution.BotRunIdentity{BotID: t.req.Bot.ID, RunID: t.req.Run.ID, Version: t.req.Version.Version, ActorID: t.req.Bot.OwnerID.String(), OrgSlug: t.req.Principal.OrgSlug, TeamSlug: t.req.Principal.TeamSlug}
	if !ok || id != want || store.SessionIDFromContext(ctx) != t.req.Run.SessionID.String() {
		return nil, fmt.Errorf("%w: workflow identity mismatch", store.ErrBotForbidden)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err = json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if err = common.ValidateToolArguments(t, values); err != nil {
		return nil, err
	}
	var base context.Context = store.WithServices(ctx, &t.services)
	switch t.kind {
	case botWorkflowCredentials:
		if t.Name() == "test_credential" {
			return t.unsupported("token acquisition requires a sandbox credential-test transport; daemon OAuth/Keystone resolution is disabled")
		}
		cs := store.NewMergedCredentialStore(t.services.PersonalCredentials, t.services.Credentials)
		if t.services.PersonalCredentials == nil && t.services.Credentials == nil {
			return t.unsupported("owner credential store unavailable; global fallback disabled")
		}
		if t.Name() == "save_credential" || t.Name() == "remove_credential" {
			// Chat credential mutations are personal; never reinterpret them as team
			// publication/deletion just because a team Bot has no personal store.
			if t.req.Bot.Scope != store.BotScopePersonal || t.services.PersonalCredentials == nil {
				return t.unsupported("Chat credential mutations require a personal store; team publication rights are unchanged")
			}
			base = store.WithCredentialStore(base, t.services.PersonalCredentials)
		} else {
			base = store.WithCredentialStore(base, cs)
		}
	case botWorkflowDrills:
		switch t.Name() {
		case "present_tutorial_blueprint", "blueprint_to_tutorial_drill":
			return t.unsupported("interactive tutorial creator approval is not a durable detached Bot checkpoint")
		}
		// Chat intentionally stores drills in the team flow store even in personal
		// sessions. Never substitute a private/global file store for a missing team.
		if t.services.Flows == nil {
			return t.unsupported("team drill flow store unavailable; filesystem fallback disabled")
		}
		base = store.WithTeamFlowStore(base, t.services.Flows)
		base = store.WithFlowStore(base, t.services.Flows)
		base = store.WithDrillReportStore(base, t.services.DrillReports)
	case botWorkflowSlides:
		if t.Name() == "ask_user" {
			if !bot.HasInteractionOwner(ctx) {
				return t.unsupported("durable question checkpoint owner unavailable")
			}
			// Generic cards need no Docs store. Convenience pickers still require
			// the exact personal owner store; never use global template fallback.
			picker := false
			for _, key := range []string{"slidesTemplate", "slidesTemplatePicker", "slidesPalettePicker", "slidesImagePicker"} {
				if v := values[key]; v != nil && v != "" && v != false {
					picker = true
				}
			}
			if picker && (t.req.Bot.Scope != store.BotScopePersonal || t.services.PersonalDocs == nil) {
				return t.unsupported("question picker requires owner personal documents")
			}
		}
		switch t.Name() {
		case "ask_user", "list_slide_templates", "get_template_variant_previews", "get_archetype":
		default:
			if t.req.Bot.Scope != store.BotScopePersonal || t.services.PersonalDocs == nil {
				return t.unsupported("Chat slide authoring requires personal documents; team Bots cannot borrow the owner's private store")
			}
		}
		for _, key := range []string{"url", "titleImage"} {
			if s, ok := values[key].(string); ok {
				s = strings.ToLower(strings.TrimSpace(s))
				if key == "url" && s != "" || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") {
					return t.unsupported("remote slide images require sandbox asset ingestion; daemon HTTP is disabled")
				}
			}
		}
		// Detached turns have no authenticated attachment payload. Never borrow a
		// caller's ChatFiles; existing deck/template asset refs remain usable.
		base = store.WithChatFiles(base, nil)
	case botWorkflowFleet:
		if t.Name() != "get_setup_profile" {
			return t.unsupported("Fleet activation, setup review and task-board authority are not bound to this Bot run; existing Fleet semantics are unchanged")
		}
		if draft, _ := values["draft_id"].(string); strings.TrimSpace(draft) != "" {
			return t.unsupported("Fleet setup drafts require creator/session ownership authorization; no draft is read")
		}
		base = store.WithFleetSetupProfileStore(base, t.services.FleetSetupProfiles)
	}
	redactor := credentials.RedactorFromContext(ctx)
	if redactor == nil {
		return nil, store.ErrBotForbidden
	}
	if t.kind == botWorkflowCredentials && t.Name() == "save_credential" {
		// Register supplied secret fields before Set: a failed store write may
		// echo the rejected value and cannot be hydrated from persistence.
		for _, key := range []string{"value", "token", "password", "client_secret", "access_token", "refresh_token", "application_credential_secret", "content"} {
			if secret, ok := values[key].(string); ok && secret != "" {
				redactor.AddSecret("workflow-credential", secret)
			}
		}
	}
	result, err := t.botRunnableTool.Run(botBoundToolContext{Context: ctx, owner: base}, values)
	if err != nil {
		return nil, fmt.Errorf("workflow %s: %s", t.Name(), redactor.Redact(err.Error()))
	}
	return redactor.RedactMap(result), nil
}
