package main

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestEmployeeForgottenMemoryExactEffect(t *testing.T) {
	botID, memoryID, opID := uuid.New(), uuid.NewString(), uuid.New()
	before := []store.MemorySearchResult{{ID: memoryID, Snippet: "Prefer Celsius", Attribution: &store.MemoryAttribution{BotID: botID, Source: "owner", ObservedAt: time.Now(), Status: store.MemoryKnowledgeInferred}}, {ID: uuid.NewString(), Snippet: "unrelated peer fact"}}
	op := store.BotOperationView{BotID: botID, OperationID: opID, Operation: store.BotControlMemoryForget, Exception: &store.BotOperationExceptionView{OperationID: opID, Fields: []store.BotOperationFieldView{{Caption: "Memory", Value: memoryID}}}}
	encode := func(rows []store.MemorySearchResult) string {
		raw, _ := json.Marshal(map[string]any{"memories": rows})
		return string(raw)
	}
	for _, tc := range []string{"exact", "exact-after-expiry", "active", "wrong-operation", "correction", "supersession", "expired-exception", "deleted-history", "changed-content", "changed-provenance", "peer-changed", "second-effect", "duplicate", "missing-target"} {
		t.Run(tc, func(t *testing.T) {
			after := append([]store.MemorySearchResult{}, before...)
			a := *before[0].Attribution
			a.SupersededBy = "routine-operation:" + opID.String()
			after[0].Attribution = &a
			approved := op
			switch tc {
			case "active":
				a.SupersededBy = ""
			case "wrong-operation":
				a.SupersededBy = "routine-operation:" + uuid.NewString()
			case "correction":
				a.SupersededBy = "correction:" + opID.String()
			case "supersession":
				a.SupersededBy = "memory-review:" + opID.String()
			case "exact-after-expiry", "expired-exception":
				if tc == "expired-exception" {
					a.SupersededBy = ""
				}
				ex := *op.Exception
				ex.ExpiresAt = time.Now().Add(-time.Hour)
				approved.Exception = &ex
			case "deleted-history":
				after = after[1:]
			case "changed-content":
				after[0].Snippet = "erased"
			case "changed-provenance":
				a.Source = "rewritten"
			case "peer-changed":
				after[1].Snippet = "changed"
			case "second-effect":
				peerAttr := a
				after[1].Attribution = &peerAttr
			case "duplicate":
				after = append(after, after[0])
			case "missing-target":
				approved.Exception = nil
			}
			err := employeeForgottenMemory(encode(before), encode(after), approved)
			if (err == nil) != (tc == "exact" || tc == "exact-after-expiry") {
				t.Fatalf("%s: %v", tc, err)
			}
		})
	}
}
