package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotHTTPBusyVersusChanged(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		text, retry string
	}{
		{"busy", store.ErrBotBusy, "Bot is still running or finishing cleanup; wait briefly and retry this proposal", "1"},
		{"wrapped busy", fmt.Errorf("confirm: %w", store.ErrBotBusy), "Bot is still running or finishing cleanup; wait briefly and retry this proposal", "1"},
		{"changed", store.ErrBotConflict, "Bot changed; reload and review before retrying", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			botHTTPError(w, tc.err)
			if w.Code != http.StatusConflict || strings.TrimSpace(w.Body.String()) != tc.text || w.Header().Get("Retry-After") != tc.retry {
				t.Fatalf("status=%d body=%q retry=%q", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
			}
		})
	}
}
