package store

import "slices"

// BotOwnerScheduleCommand is an explicit owner request envelope, admitted alongside
// the canonical user event. It is not a model's assertion of consent. The tool may
// select schedule parameters only within this task and operation scope.
type BotOwnerScheduleCommand struct {
	Operations   []BotControlOperation `json:"operations"`
	Instructions string                `json:"instructions"`
}

func (c BotOwnerScheduleCommand) Validate() error {
	if len(c.Operations) == 0 || len(c.Operations) > 4 || len(c.Instructions) == 0 || len(c.Instructions) > 65536 {
		return ErrBotInvalid
	}
	for _, op := range c.Operations {
		switch op {
		case BotControlScheduleCreate, BotControlScheduleEdit, BotControlSchedulePause, BotControlScheduleRemove:
		default:
			return ErrBotInvalid
		}
	}
	return nil
}
func (c BotOwnerScheduleCommand) Allows(p BotControlPayload) bool {
	return c.Validate() == nil && p.Schedule != nil && slices.Contains(c.Operations, p.Operation) && (p.Operation == BotControlSchedulePause || p.Operation == BotControlScheduleRemove || p.Schedule.Trigger.Instructions == c.Instructions)
}
