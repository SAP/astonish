package api

import "encoding/json"

// botCapturedResponseJSON matches the durable transcript representation. ADK
// results can contain typed structs in the live session; restored results contain
// JSON maps. Evidence identity must not depend on that in-memory distinction.
func botCapturedResponseJSON(response map[string]any) ([]byte, error) {
	raw, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return nil, err
	}
	return json.Marshal(persisted)
}
