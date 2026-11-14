package entstore

import (
 "encoding/json"
 "github.com/SAP/astonish/pkg/store"
)

// Held source receipts may deliberately omit unsafe or unavailable evidence.
// They retain the immutable source tuple but are never eligible for generation.
func validKnowledgeRetainedInput(r store.BotKnowledgeExtraction) bool {
 if r.Input.Validate()==nil {return true}
 if r.State!=store.BotKnowledgeExtractionHeld && r.State!=store.BotKnowledgeExtractionAborted {return false}
 raw,err:=json.Marshal(r.Input)
 return err==nil && len(raw)<=store.BotKnowledgeExtractionMaxInputBytes && r.Input.SchemaVersion==store.BotKnowledgeExtractionSchemaVersion && r.Input.Source.Validate()==nil && !r.Input.Complete && r.HoldReason!=""
}
