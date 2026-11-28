package chatruntime

import (
	"context"
	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/store"
	"strings"
)

func BuildMergedSkillIndex(ctx context.Context, filesystem []skills.Skill, platformStore, orgStore, teamStore store.SkillStore) string {
	index, _ := buildMergedSkillIndex(ctx, filesystem, platformStore, orgStore, teamStore, FallbackAllowed)
	return index
}
func buildMergedSkillIndex(ctx context.Context, filesystem []skills.Skill, platformStore, orgStore, teamStore store.SkillStore, fallback FallbackPolicy) (string, error) {
	all := append([]skills.Skill(nil), filesystem...)

	for _, tier := range []struct {
		name  string
		store store.SkillStore
	}{
		{name: "platform", store: platformStore},
		{name: "org", store: orgStore},
		{name: "team", store: teamStore},
	} {
		if tier.store == nil {
			continue
		}
		stored, err := tier.store.LoadAll(ctx)
		if err != nil {
			if fallback == FallbackForbidden {
				return "", err
			}
			continue
		}
		for _, s := range stored {
			all = append(all, skills.Skill{
				Name:        s.Name,
				Description: s.Description,
				OS:          s.OS,
				RequireBins: s.RequireBins,
				RequireEnv:  s.RequireEnv,
				Source:      tier.name,
			})
		}
	}

	seen := make(map[string]struct{}, len(all))
	deduped := make([]skills.Skill, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		name := strings.ToLower(strings.TrimSpace(all[i].Name))
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		deduped = append(deduped, all[i])
	}
	for i, j := 0, len(deduped)-1; i < j; i, j = i+1, j-1 {
		deduped[i], deduped[j] = deduped[j], deduped[i]
	}
	return skills.BuildSkillIndex(deduped), nil
}
