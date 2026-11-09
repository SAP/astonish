package config

import "fmt"

// BotRoutineControl is an operator restriction selector, never a capability grant.
type BotRoutineControl string

const BotRoutineMemoryForget BotRoutineControl = "memory_forget"

// BotRoutinePolicyConfig only removes existing routine authority. Empty keeps defaults.
// It deliberately accepts no owner IDs, memory IDs, tools, resources, or grants.
type BotRoutinePolicyConfig struct {
	RequireApproval []BotRoutineControl `yaml:"require_approval,omitempty" json:"require_approval,omitempty"`
}

func (c BotRoutinePolicyConfig) Validate() error {
	seen := map[BotRoutineControl]bool{}
	for _, control := range c.RequireApproval {
		if control != BotRoutineMemoryForget || seen[control] {
			return fmt.Errorf("bot_routine_policy.require_approval: unsupported or duplicate control %q", control)
		}
		seen[control] = true
	}
	return nil
}
