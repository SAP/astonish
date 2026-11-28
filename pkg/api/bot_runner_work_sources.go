package api

import (
	"fmt"

	"github.com/SAP/astonish/pkg/store"
)

// References are transport labels, not source content or authorization. Paths
// point into the complete, unchanged contract instead of duplicating bounded text.
type botWorkSourceReference struct {
	SourceID     string `json:"source_id"`
	ContractPath string `json:"contract_path"`
}

type botWorkPrepareInput struct {
	store.BotWorkContract
	AvailableSourceIDs map[string]botWorkSourceReference `json:"available_source_ids"`
}

// Order is pinned contract order; no model-supplied names or matching heuristics
// participate. The returned inventory is local to this preparation call.
func botWorkSourceReferences(contract store.BotWorkContract) ([]string, map[string]botWorkSourceReference) {
	aliases := []string{"assignment", "charter"}
	refs := map[string]botWorkSourceReference{
		"assignment": {SourceID: "assignment", ContractPath: "instructions"},
		"charter":    {SourceID: "charter", ContractPath: "charter"},
	}
	for i, goal := range contract.Goals {
		alias := fmt.Sprintf("goal_%d", i+1)
		aliases = append(aliases, alias)
		refs[alias] = botWorkSourceReference{SourceID: "goal:" + goal.ID.String(), ContractPath: fmt.Sprintf("goals[%d]", i)}
	}
	for i, attachment := range contract.Attachments {
		alias := fmt.Sprintf("attachment_%d", i+1)
		aliases = append(aliases, alias)
		refs[alias] = botWorkSourceReference{SourceID: attachment.SourceID, ContractPath: fmt.Sprintf("attachments[%d]", i)}
	}
	return aliases, refs
}
