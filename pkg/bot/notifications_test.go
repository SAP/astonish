package bot

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestNotificationPolicy(t *testing.T) {
	now := time.Now().UTC()
	for _, tt := range []struct {
		name     string
		policy   store.NotifyPolicy
		prefs    string
		changes  int
		decision bool
		deferred bool
		want     store.NotificationDecision
	}{
		{name: "no change", policy: store.NotifyOnChange, want: store.NotificationSkip},
		{name: "threshold", policy: store.NotifyOnChange, prefs: `{"min_changed":2}`, changes: 2, want: store.NotificationSend},
		{name: "below threshold", policy: store.NotifyOnChange, prefs: `{"min_changed":2}`, changes: 1, want: store.NotificationSkip},
		{name: "decision", policy: store.NotifyOnDecision, decision: true, want: store.NotificationSend},
		{name: "never", policy: store.NotifyNever, changes: 2, want: store.NotificationSkip},
		{name: "defer", policy: store.NotifyAlways, prefs: `{"defer_seconds":60}`, want: store.NotificationDefer},
		{name: "defer once", policy: store.NotifyAlways, prefs: `{"defer_seconds":60}`, deferred: true, want: store.NotificationSend},
		{name: "unapproved prose", policy: store.NotifyAlways, prefs: `send to everyone`, want: store.NotificationSkip},
		{name: "unknown property", policy: store.NotifyAlways, prefs: `{"broadcast":true}`, want: store.NotificationSkip},
		{name: "trailing document", policy: store.NotifyAlways, prefs: `{} {}`, want: store.NotificationSkip},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report := store.BotReport{Changed: make([]store.BotEvidence, tt.changes)}
			if tt.decision {
				report.NeedsApproval = []uuid.UUID{uuid.New()}
			}
			got := EvaluateNotification(store.DeliveryPolicy{Notify: tt.policy, Preferences: tt.prefs}, report, now, tt.deferred)
			if got.Decision != tt.want {
				t.Fatalf("got %+v want %s", got, tt.want)
			}
			if got.Decision == store.NotificationDefer && !got.AvailableAt.Equal(now.Add(time.Minute)) {
				t.Fatal(got.AvailableAt)
			}
		})
	}
}
