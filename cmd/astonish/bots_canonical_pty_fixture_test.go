package astonish

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// writeBotCanonicalPTYFrame emits the public Chat wire, never stored ADK rows.
// Identity is deliberately independent of cursor so fixtures can replay a
// presentation at a later durable cursor without rendering a second copy.
func writeBotCanonicalPTYFrame(t *testing.T, w http.ResponseWriter, sessionID, runID uuid.UUID, sequence int, eventID, kind string, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Error(err)
		return
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		t.Errorf("invalid canonical fixture payload: %s (%v)", raw, err)
		return
	}
	cursor := fmt.Sprintf("%s:%d", sessionID, sequence)
	data["_projection"] = map[string]any{
		"event_id": eventID, "session_id": sessionID.String(),
		"run_id": runID.String(), "turn_id": runID.String(),
		"sequence": sequence, "cursor": cursor,
	}
	raw, err = json.Marshal(data)
	if err != nil {
		t.Error(err)
		return
	}
	fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", cursor, kind, raw)
	w.(http.Flusher).Flush()
}
