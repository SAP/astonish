package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
)

// Only visible content from the single durable operation card may prove scope.
// A human action heading replaces the transport-only Change enum; no other
// field is translated, skipped or inferred from surrounding conversation text.
type employeeExceptionDOM struct {
	ID      string                        `json:"id"`
	Heading string                        `json:"heading"`
	Text    string                        `json:"text"`
	Scope   string                        `json:"scope"`
	Fields  []store.BotOperationFieldView `json:"fields"`
}

var errEmployeeExceptionCardPending = errors.New("exact operation card not visible yet")

// API readiness is not a browser render barrier. Observe only: never reload,
// refresh the product, or replay the request. The 30s bound allows the normal
// 15s snapshot poll as well as SSE/React delivery to catch up.
func employeeReadExceptionCard(page *rod.Page, op store.BotOperationView) (employeeExceptionDOM, error) {
	page = page.Timeout(30 * time.Second)
	defer page.CancelTimeout()
	for {
		card, err := employeeReadExceptionCardOnce(page, op)
		if !errors.Is(err, errEmployeeExceptionCardPending) {
			return card, err // Malformed/duplicate cards and browser errors are not retries.
		}
		select {
		case <-page.GetContext().Done():
			return card, fmt.Errorf("waiting for exact operation card %s: %w (%v)", op.OperationID, page.GetContext().Err(), err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func employeeReadExceptionCardOnce(page *rod.Page, op store.BotOperationView) (employeeExceptionDOM, error) {
	result, err := page.Eval(`id => {
   const cards = [...document.querySelectorAll('article[data-operation-id]')].filter(e => e.dataset.operationId === id && e.checkVisibility({checkOpacity:true, checkVisibilityCSS:true}));
   if (cards.length === 0) return null;
   if (cards.length !== 1) throw new Error('one visible exact operation card required; found ' + cards.length);
   const card = cards[0];
   const visible = e => e && e.checkVisibility({checkOpacity:true, checkVisibilityCSS:true}) && [...e.querySelectorAll('*')].filter(child => child.textContent.trim()).every(child => child.checkVisibility({checkOpacity:true, checkVisibilityCSS:true}));
   const h = card.querySelector('h3');
   return {id:card.dataset.operationId, text:card.innerText, heading:visible(h) ? h.innerText : '',
     scope:[...card.querySelectorAll('p')].filter(visible).map(e => e.innerText).find(t => t === 'Permission requested for this saved action only.') || '',
     fields:[...card.querySelectorAll('dt')].filter(visible).flatMap(dt => {
       const dd = dt.nextElementSibling;
       return dd?.tagName === 'DD' && visible(dd) ? [{caption:dt.innerText,value:dd.innerText}] : [];
     })};
 }`, op.OperationID.String())
	if err != nil {
		return employeeExceptionDOM{}, err
	}
	var card *employeeExceptionDOM
	if err = json.Unmarshal([]byte(result.Value.JSON("", "")), &card); err != nil {
		return employeeExceptionDOM{}, fmt.Errorf("decode exact operation card: %w", err)
	}
	if card == nil {
		return employeeExceptionDOM{}, errEmployeeExceptionCardPending
	}
	return *card, nil
}

func employeeExceptionCard(op store.BotOperationView, card employeeExceptionDOM) error {
	if err := employeeException(op); err != nil {
		return err
	}
	if card.ID != op.OperationID.String() {
		return fmt.Errorf("exception card identity mismatch")
	}
	if card.Heading != "Forget information · Blocked" {
		return fmt.Errorf("exact exception action not visible")
	}
	if card.Scope != "Permission requested for this saved action only." {
		return fmt.Errorf("exception scope not visible")
	}
	expected := append([]store.BotOperationFieldView{}, op.Exception.Fields...)
	expected = append(expected, store.BotOperationFieldView{Caption: "Expires", Value: op.Exception.ExpiresAt.UTC().Format(time.RFC3339Nano)})
	for _, field := range expected {
		if field.Caption == "Change" {
			if field.Value != string(op.Operation) {
				return fmt.Errorf("exception action mismatch")
			}
			continue // checked against the exact human heading above, not ignored.
		}
		if strings.TrimSpace(field.Caption) == "" || strings.TrimSpace(field.Value) == "" {
			return fmt.Errorf("empty exception field")
		}
		count := 0
		for _, shown := range card.Fields {
			if shown.Caption == field.Caption && shown.Value == field.Value {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("exact exception field %q not visible once", field.Caption)
		}
	}
	return nil
}
