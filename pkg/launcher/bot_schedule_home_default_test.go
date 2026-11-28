package launcher

import (
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func TestBostonScheduleNeedsNoDeliveryBinding(t *testing.T) {
	f, repo := scheduleFixture(t, store.BotScopePersonal)
	r, c := scheduleFactory(t, f, repo)
	defer r.Cleanup()
	v, err := repo.GetVersion(f.ctx, f.req.Bot.ID, f.req.Version.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Delivery.Bindings) != 0 {
		t.Fatal("fixture has external delivery")
	}
	proposal := decodeScheduleReview(t, basicCall(t, r, c, "schedule_job", map[string]any{"name": "Boston weather", "mode": "adaptive", "schedule": "0 9 * * *", "timezone": "America/New_York", "instructions": "Report Boston weather in this Bot chat."}))
	if proposal.Review.Body.Definition == nil || proposal.Review.Body.Definition.Enabled {
		t.Fatal("missing disabled schedule review")
	}
}
