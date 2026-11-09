package main

import (
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestEmployeeExceptionHumanActionAndExactBounds(t *testing.T) {
	id := uuid.New()
	op := store.BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: id, Revision: 1, StateSequence: 1, Operation: store.BotControlMemoryForget, Status: store.BotOperationBlocked, Decision: store.BotOperationRequireAuthorization}
	op.Exception = &store.BotOperationExceptionView{OperationID: id, PayloadDigest: "digest", ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), Fields: []store.BotOperationFieldView{{Caption: "Change", Value: "memory_forget"}, {Caption: "Memory", Value: "memory-id"}, {Caption: "Recipient", Value: "owner@example.test"}, {Caption: "Cost limit", Value: "USD 0.25"}}}
	good := employeeExceptionDOM{ID: id.String(), Heading: "Forget information · Blocked", Scope: "Permission requested for this saved action only.", Fields: append(append([]store.BotOperationFieldView{}, op.Exception.Fields[1:]...), store.BotOperationFieldView{Caption: "Expires", Value: "2099-01-01T00:00:00Z"})}
	if err := employeeExceptionCard(op, good); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"foreign-id", "wrong-action", "raw-action", "scope", "memory", "recipient", "cost", "expiry", "caption", "prose-only", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			bad := good
			bad.Fields = append([]store.BotOperationFieldView{}, good.Fields...)
			switch name {
			case "foreign-id":
				bad.ID = uuid.NewString()
			case "wrong-action":
				bad.Heading = "Remember information · Blocked"
			case "raw-action":
				bad.Heading = "Change memory_forget"
			case "scope":
				bad.Scope = "Permission for all actions"
			case "memory":
				bad.Fields[0].Value = "memory-id-extra"
			case "recipient":
				bad.Fields[1].Value = "other@example.test"
			case "cost":
				bad.Fields[2].Value = "USD 25"
			case "expiry":
				bad.Fields[3].Value = "2099-01-02T00:00:00Z"
			case "caption":
				bad.Fields[0].Caption = "Recipient"
			case "prose-only":
				bad.Text = "Memory memory-id Recipient owner@example.test Cost limit USD 0.25 Expires 2099-01-01T00:00:00Z"
				bad.Fields = nil
			case "duplicate":
				bad.Fields = append(bad.Fields, bad.Fields[0])
			}
			if employeeExceptionCard(op, bad) == nil {
				t.Fatal("invalid bound accepted")
			}
		})
	}
	op.Exception.Fields[0].Value = "memory_remember"
	if employeeExceptionCard(op, good) == nil {
		t.Fatal("mismatched persisted Change accepted")
	}
}
