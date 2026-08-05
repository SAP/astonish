// Package channels — fleet-related slash commands for channel adapters.
//
// These commands allow users to start fleet sessions and create fleet plans
// directly from Telegram (or any other channel adapter). Fleet sessions
// bridge the fleet's internal chat channel to the external channel so
// agent messages are forwarded to the user and user replies are routed
// back to the fleet session.
package channels

import (
	"context"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
)

// registerFleetCommands adds fleet-related commands to the ChannelManager's
// command registry. Called from SetFleetDeps after dependencies are available.
func registerFleetCommands(mgr *ChannelManager) {
	mgr.commands.Register(fleetCommand(mgr))
	mgr.commands.Register(fleetPlanCommand(mgr))
	mgr.commands.Register(fleetStopCommand(mgr))
}

// fleetCommand returns the /fleet command handler.
//
// Usage:
//
//	/fleet              — List available chat-type fleet plans
//	/fleet <plan-key>   — Start a fleet session from a plan
//	/fleet <key> <task> — Start a fleet session with an initial task
func fleetCommand(mgr *ChannelManager) *Command {
	return &Command{
		Name:          "fleet",
		Description:   "Start a fleet session (autonomous agent team)",
		SessionScoped: true,
		Handler: func(ctx context.Context, cc CommandContext) (string, error) {
			if mgr.fleetDeps == nil {
				return "Fleet system is not available.", nil
			}

			// Check if a fleet session is already active for this chat
			if sessionID := mgr.GetActiveFleet(cc.SessionKey); sessionID != "" {
				shortID := sessionID
				if len(shortID) > 16 {
					shortID = shortID[:16]
				}
				return fmt.Sprintf("A fleet session is already active (%s). Use /fleet_stop to end it first.", shortID), nil
			}

			args := strings.TrimSpace(strings.TrimPrefix(cc.RawText, "/fleet"))

			// No args: list available chat-type plans
			if args == "" {
				return listChatPlans(mgr)
			}

			// Parse: first word is plan key, rest is optional message
			parts := strings.SplitN(args, " ", 2)
			planKey := parts[0]
			initialMessage := ""
			if len(parts) > 1 {
				initialMessage = strings.TrimSpace(parts[1])
			}

			return startFleetFromTelegram(ctx, mgr, cc, planKey, initialMessage)
		},
	}
}

// listChatPlans returns a formatted list of chat-type fleet plans.
func listChatPlans(mgr *ChannelManager) (string, error) {
	if mgr.fleetDeps.GetPlanRegistry == nil {
		return "Fleet plan system is not initialized.", nil
	}

	reg := mgr.fleetDeps.GetPlanRegistry()
	if reg == nil {
		return "Fleet plan system is not initialized.", nil
	}

	plans := reg.ListPlans()
	var chatPlans []FleetPlanSummary
	for _, p := range plans {
		if p.ChannelType == "chat" {
			chatPlans = append(chatPlans, p)
		}
	}

	if len(chatPlans) == 0 {
		return "No chat fleet plans available.\n\nCreate one first using /fleet_plan", nil
	}

	var b strings.Builder
	b.WriteString("Available Fleet Plans:\n\n")
	for _, p := range chatPlans {
		b.WriteString(fmt.Sprintf("  %s — %s\n", p.Key, p.Name))
		if p.Description != "" {
			b.WriteString(fmt.Sprintf("    %s\n", p.Description))
		}
		b.WriteString(fmt.Sprintf("    Agents: %s\n\n", strings.Join(p.AgentNames, ", ")))
	}
	b.WriteString("Start a session:\n  /fleet <plan-key>\n  /fleet <plan-key> <task>")
	return b.String(), nil
}

// startFleetFromTelegram creates a fleet session and wires it to the channel.
func startFleetFromTelegram(ctx context.Context, mgr *ChannelManager, cc CommandContext, planKey, initialMessage string) (string, error) {
	if mgr.fleetDeps.StartSessionFromPlan == nil {
		return "Fleet session system is not initialized.", nil
	}

	result, err := mgr.fleetDeps.StartSessionFromPlan(planKey, initialMessage)
	if err != nil {
		return fmt.Sprintf("Failed to start fleet session: %v", err), nil
	}

	target := Target{
		ChannelID: cc.ChannelID,
		ChatID:    cc.ChatID,
	}

	// Wire message forwarding: when agents post messages, forward them to the channel.
	// The OnMessagePosted callback is composed with the existing transcript callback.
	if result.SetOnMessagePosted != nil {
		result.SetOnMessagePosted(func(sender, text string) {
			// Only forward agent and system messages (not customer echoes)
			if sender == "customer" {
				return
			}

			// Format as @sender: text
			displayText := fmt.Sprintf("@%s: %s", sender, text)

			outMsg := OutboundMessage{
				Text:   displayText,
				Format: FormatHTML,
			}
			if sendErr := mgr.Send(ctx, target, outMsg); sendErr != nil {
				mgr.logger.Printf("[channels] Failed to forward fleet message to %s: %v", cc.ChannelID, sendErr)
			}
		})
	}

	// Wire session completion: auto-clear the fleet mapping when done.
	if result.SetOnSessionDone != nil {
		result.SetOnSessionDone(func(sessionID string, sessionErr error) {
			mgr.ClearActiveFleet(cc.SessionKey)

			doneText := "Fleet session completed."
			if sessionErr != nil {
				doneText = fmt.Sprintf("Fleet session ended with error: %v", sessionErr)
			}

			outMsg := OutboundMessage{
				Text:   doneText,
				Format: FormatText,
			}
			if sendErr := mgr.Send(context.Background(), target, outMsg); sendErr != nil {
				mgr.logger.Printf("[channels] Failed to send fleet completion to %s: %v", cc.ChannelID, sendErr)
			}
		})
	}

	// Map this chat to the fleet session
	mgr.SetActiveFleet(cc.SessionKey, result.SessionID)

	taskInfo := ""
	if initialMessage != "" {
		taskInfo = fmt.Sprintf("\nTask: %s", initialMessage)
	}

	return fmt.Sprintf("Fleet session started (%s).%s\n\nThe PO will begin shortly. Your messages will be routed to the fleet.\nUse /fleet_stop to end the session.", result.FleetName, taskInfo), nil
}

// fleetPlanCommand returns the /fleet_plan command handler.
//
// Usage:
//
//	/fleet_plan                 — List available fleet templates
//	/fleet_plan <template-key>  — Start a wizard conversation to create a plan
func fleetPlanCommand(mgr *ChannelManager) *Command {
	return &Command{
		Name:          "fleet_plan",
		Description:   "Create a fleet plan from a template",
		SessionScoped: true,
		Handler: func(ctx context.Context, cc CommandContext) (string, error) {
			if mgr.fleetDeps == nil {
				return "Fleet system is not available.", nil
			}

			args := strings.TrimSpace(strings.TrimPrefix(cc.RawText, "/fleet_plan"))

			// No args: list available templates
			if args == "" {
				return listFleetTemplates(mgr)
			}

			// Template key provided: inject wizard system prompt and trigger conversation
			templateKey := args

			if mgr.fleetDeps.GetFleetRegistry == nil {
				return "Fleet template system is not initialized.", nil
			}

			reg := mgr.fleetDeps.GetFleetRegistry()
			if reg == nil {
				return "Fleet template system is not initialized.", nil
			}

			tmpl, ok := reg.GetFleet(templateKey)
			if !ok {
				return fmt.Sprintf("Fleet template %q not found. Use /fleet_plan to list available templates.", templateKey), nil
			}

			// Determine the system prompt to inject
			systemPrompt := tmpl.WizardSystemPrompt
			if systemPrompt == "" {
				// Fallback: generic wizard prompt
				systemPrompt = fmt.Sprintf(
					"You are helping the user create a fleet plan based on the %q fleet template. "+
						"The base_fleet_key is %q. Guide them through:\n"+
						"1. Plan identity (key, name, description)\n"+
						"2. Communication channel type and settings\n"+
						"3. Artifact destinations\n"+
						"4. Credentials for external services\n"+
						"5. Any agent behavior customizations\n\n"+
						"Before saving, call validate_fleet_plan with all config including credentials. "+
						"Only call save_fleet_plan after validation passes. Include the same credentials in the save call.",
					templateKey, templateKey)
			}

			// Escape curly braces for ADK template safety
			systemPrompt = agent.EscapeCurlyPlaceholders(systemPrompt)

			// Inject the wizard system prompt for the next regular chat turn
			mgr.SetSessionContext(cc.SessionKey, systemPrompt)

			// Inject pinned tool groups if the template specifies them
			if len(tmpl.PinnedToolGroups) > 0 {
				mgr.SetPinnedToolGroups(cc.SessionKey, tmpl.PinnedToolGroups)
			}

			// Return a message that will trigger the user to send their first
			// regular message (which will have the wizard context injected).
			return fmt.Sprintf("Starting fleet plan wizard for %q (%s).\n\nJust say \"go\" or describe what you want to build, and I'll guide you through the configuration.", templateKey, tmpl.Name), nil
		},
	}
}

// listFleetTemplates returns a formatted list of available fleet templates.
func listFleetTemplates(mgr *ChannelManager) (string, error) {
	if mgr.fleetDeps.GetFleetRegistry == nil {
		return "Fleet template system is not initialized.", nil
	}

	reg := mgr.fleetDeps.GetFleetRegistry()
	if reg == nil {
		return "Fleet template system is not initialized.", nil
	}

	templates := reg.ListFleets()
	if len(templates) == 0 {
		return "No fleet templates available.", nil
	}

	var b strings.Builder
	b.WriteString("Available Fleet Templates:\n\n")
	for _, t := range templates {
		b.WriteString(fmt.Sprintf("  %s — %s\n", t.Key, t.Name))
		if t.Description != "" {
			b.WriteString(fmt.Sprintf("    %s\n", t.Description))
		}
		b.WriteString(fmt.Sprintf("    Agents: %s\n\n", strings.Join(t.AgentNames, ", ")))
	}
	b.WriteString("Create a plan:\n  /fleet_plan <template-key>")
	return b.String(), nil
}

// fleetStopCommand returns the /fleet_stop command handler.
func fleetStopCommand(mgr *ChannelManager) *Command {
	return &Command{
		Name:          "fleet_stop",
		Description:   "Stop the active fleet session",
		SessionScoped: true,
		Handler: func(ctx context.Context, cc CommandContext) (string, error) {
			sessionID := mgr.GetActiveFleet(cc.SessionKey)
			if sessionID == "" {
				return "No active fleet session.", nil
			}

			mgr.ClearActiveFleet(cc.SessionKey)

			// Stop the fleet session via the injected dependency
			if mgr.fleetDeps != nil && mgr.fleetDeps.StopSession != nil {
				if err := mgr.fleetDeps.StopSession(sessionID); err != nil {
					mgr.logger.Printf("[channels] Warning: failed to stop fleet session %s: %v", sessionID, err)
				}
			}

			return "Fleet session stopped. Returning to normal chat.", nil
		},
	}
}
