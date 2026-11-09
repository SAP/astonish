package main

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// The API is evidence history, not a current-only projection. Bind the single
// retired row to the saved exception target and exact operation; retain every
// original field and all neighboring rows. Expired authority is not a tombstone.
func employeeForgottenMemory(beforeJSON, afterJSON string, approved store.BotOperationView) error {
	if approved.Operation != store.BotControlMemoryForget || approved.OperationID == uuid.Nil || approved.Exception == nil || approved.Exception.OperationID != approved.OperationID {
		return fmt.Errorf("missing exact forget exception")
	}
	target := ""
	for _, field := range approved.Exception.Fields {
		if field.Caption == "Memory" {
			if target != "" {
				return fmt.Errorf("duplicate memory target")
			}
			target = field.Value
		}
	}
	if id, err := uuid.Parse(target); err != nil || id == uuid.Nil {
		return fmt.Errorf("missing exact memory target")
	}
	decode := func(raw string) (map[string]store.MemorySearchResult, error) {
		var response struct {
			Memories []store.MemorySearchResult `json:"memories"`
		}
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			return nil, err
		}
		rows := map[string]store.MemorySearchResult{}
		for _, row := range response.Memories {
			if _, exists := rows[row.ID]; exists || row.ID == "" {
				return nil, fmt.Errorf("duplicate or missing memory identity")
			}
			rows[row.ID] = row
		}
		return rows, nil
	}
	before, err := decode(beforeJSON)
	if err != nil {
		return err
	}
	after, err := decode(afterJSON)
	if err != nil {
		return err
	}
	source, exists := before[target]
	if !exists || source.Attribution == nil || source.Attribution.BotID != approved.BotID || source.Attribution.SupersededBy != "" {
		return fmt.Errorf("target not an active attributed source")
	}
	if len(before) != len(after) {
		return fmt.Errorf("forget deleted history or added unexpected memory")
	}
	tombstones := 0
	for id, old := range before {
		row, exists := after[id]
		if !exists {
			return fmt.Errorf("missing retained memory %s", id)
		}
		if row.Attribution != nil && row.Attribution.SupersededBy == "routine-operation:"+approved.OperationID.String() {
			tombstones++
		}
		if id == target {
			if store.IsCurrentBotMemory(row) || row.Attribution == nil || row.Attribution.SupersededBy != "routine-operation:"+approved.OperationID.String() {
				return fmt.Errorf("exact forget tombstone not observed")
			}
			a := *old.Attribution
			a.SupersededBy = "routine-operation:" + approved.OperationID.String()
			old.Attribution = &a
		}
		if !reflect.DeepEqual(old, row) {
			return fmt.Errorf("forget changed source evidence or peer memory %s", id)
		}
	}
	if tombstones != 1 {
		return fmt.Errorf("expected exactly one forget effect, got %d", tombstones)
	}
	return nil
}
