package store

import (
	"context"
	"time"
)

// BotPriorSkillTask supplies context only when recent completed owner tasks
// identify a single skill. Recency alone cannot resolve an ambiguous reference.
// This is context for model orientation, never consent or scheduling authority.
func BotPriorSkillTask(ctx context.Context, repo BotStore, current BotRun) (*BotRun, error) {
	runs, err := repo.ListRuns(ctx, current.BotID, BotPage{Limit: 100})
	if err != nil {
		return nil, err
	}
	var latest *BotRun
	var at time.Time
	ambiguous := false
	for i := range runs {
		r := &runs[i]
		if r.ID == current.ID || r.BotID != current.BotID || r.Trigger != TriggerManual || !r.CreatedAt.Before(current.CreatedAt) {
			continue
		}
		if latest == nil || r.CreatedAt.After(at) {
			latest = r
			at = r.CreatedAt
			ambiguous = false
		} else if r.CreatedAt.Equal(at) {
			ambiguous = true
		}
	}
	if ambiguous || latest == nil || latest.Status != RunSucceeded || latest.Report.Skill == nil {
		return nil, nil
	}
	receipt := latest.Report.Skill
	if receipt.BotID != current.BotID || receipt.RunID != latest.ID || receipt.Kind == BotSkillRejected || receipt.Kind == BotSkillConflict || receipt.Kind == BotSkillDeferred {
		return nil, nil
	}
	for _, r := range runs {
		if r.ID == current.ID || r.BotID != current.BotID || r.Trigger != TriggerManual || !r.CreatedAt.Before(current.CreatedAt) {
			continue
		}
		if r.Status == RunSucceeded && r.Report.Skill != nil && r.Report.Skill.SkillID != receipt.SkillID {
			return nil, nil
		}
	}
	return latest, nil
}
