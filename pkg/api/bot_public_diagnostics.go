package api

import (
	"bytes"
	"encoding/json"
)

// botPublicJSON projects public responses without mutating durable reports or
// their integrity digests. RawMessage leaves preserve integer precision.
func botPublicJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return botPublicJSONNode(raw)
}

func botPublicJSONNode(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, &json.SyntaxError{}
	}
	switch trimmed[0] {
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil {
			return nil, err
		}
		delete(object, "technical_details")
		for key, value := range object {
			projected, err := botPublicJSONNode(value)
			if err != nil {
				return nil, err
			}
			object[key] = projected
		}
		return json.Marshal(object)
	case '[':
		var array []json.RawMessage
		if err := json.Unmarshal(trimmed, &array); err != nil {
			return nil, err
		}
		for i, value := range array {
			projected, err := botPublicJSONNode(value)
			if err != nil {
				return nil, err
			}
			array[i] = projected
		}
		return json.Marshal(array)
	default:
		if !json.Valid(trimmed) {
			return nil, &json.SyntaxError{}
		}
		return trimmed, nil
	}
}
