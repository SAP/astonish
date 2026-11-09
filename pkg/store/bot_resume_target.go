package store

import (
	"fmt"

	"github.com/google/uuid"
)

const BotResumeInvalidTargetCode = "invalid_target"

// BotResumeTargetError identifies a rejected exact target without revealing other
// Bots' resources or suggesting an alias. No durable operation has been accepted.
type BotResumeTargetError struct {
	ScheduleID uuid.UUID `json:"schedule_id"`
}

func (e *BotResumeTargetError) Error() string {
	return fmt.Sprintf("%s: resume_targets.schedule_ids value %s is not a current resumable scheduler job of this Bot; copy triggers[].schedule_id or list_scheduled_jobs jobs[].schedule_id, not trigger id; no operation was captured", BotResumeInvalidTargetCode, e.ScheduleID)
}

func (e *BotResumeTargetError) Unwrap() error { return ErrBotInvalid }
