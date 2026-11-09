package astonish

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBotTitleStartOptIn(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		t.Run(map[bool]string{false: "inert", true: "explicit"}[optIn], func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/studio/bots" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				var q map[string]any
				if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
					t.Error(err)
				}
				if optIn {
					if q["start_from_title"] != true {
						t.Errorf("missing opt-in: %+v", q)
					}
				} else if q["start_from_title"] == true {
					t.Error("creation implicitly started")
				}
				_ = json.NewEncoder(w).Encode(store.Bot{Name: "Title only"})
			}))
			defer s.Close()
			args := []string{"create", "draft", "Title only", "--json"}
			if optIn {
				args = append(args, "--start-from-title")
			} else {
				args = append(args, "--charter", "Inert draft")
			}
			if err := runBotsCommand(context.Background(), client.NewWithConfig(s.URL), args, io.Discard); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("%d requests", calls)
			}
		})
	}
}
