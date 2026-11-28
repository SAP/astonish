package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRealScheduleFinalReportsApplied(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"applied", "Enabled every 5 minutes.", true},
		{"delivery_future", "Enabled every 5 minutes. The next delivery has not yet happened.", true},
		{"historical_pending", "Active every 5 minutes. The earlier durably queued and pending note is now superseded; the change is verified.", true},
		{"quoted_briefing", "Enabled every 5 minutes. Posts Scheduling health is normal; pending actions await verified receipts.", true},
		{"quoted_briefing_no_period", "Enabled every 5 minutes. Posts \"Scheduling health is normal; pending actions await verified receipts\" to Home.", true},
		{"committed_history", "Enabled every 5 minutes. The schedule creation that was earlier queued and pending has now been verified as committed.", true},
		{"outdated_history", "Enabled every 5 minutes. The earlier queued and pending note is now out of date; the schedule is live.", true},
		{"history_with_current_receipt", "Enabled every 5 minutes. While an earlier status said pending, the verified receipt confirms the schedule is now committed and active.", true},
		{"pending", "Scheduled every 5 minutes, queued and pending.", false},
		{"resolved_history", "Every 5 minutes. Status: Applied (the earlier pending note is now resolved). Enabled: Yes.", true},
		{"not_enabled", "Every 5 minutes, not yet enabled.", false},
		{"contradiction", "Enabled every 5 minutes. Application remains pending.", false},
		{"wrong_cadence", "Enabled every day.", false},
		{"no_outcome", "Every 5 minutes.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := realScheduleFinalReportsApplied(tc.text); got != tc.want {
				t.Fatalf("got=%t want=%t text=%q", got, tc.want, tc.text)
			}
		})
	}
}

func TestRealScheduleLiveCaptureWaitsForPublication(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	publish := make(chan struct{})
	handlerDone := make(chan struct{})
	type authKey struct{}
	auth := context.WithValue(ctx, authKey{}, "authenticated-test-owner")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCtx, cancelRequest := authorizedScheduleHTTPRequestContext(auth, r.Context(), time.Minute)
		defer cancelRequest()
		defer close(handlerDone)
		r = r.WithContext(requestCtx)
		if r.Context().Value(authKey{}) != "authenticated-test-owner" {
			t.Error("HTTP context lost authentication")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: replay_complete\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-publish:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "event: text\ndata: {\"text\":\"Enabled every 5 minutes\",\"_projection\":{\"event_id\":\"employee-final-stable\"}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	finish, err := realScheduleLiveCapture(ctx, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer finish("")
	close(publish)
	raw := finish("Enabled every 5 minutes")
	if id := realSchedulePublicationID(t, raw, "Enabled every 5 minutes"); id != "employee-final-stable" {
		t.Fatal(id)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Error("closing live capture did not cancel authenticated server handler")
		cancel() // Ensure a failing regression cannot hang Server.Close.
	}
}
