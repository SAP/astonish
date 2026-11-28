package bot

import (
	"errors"
	"fmt"
	"math"

	"github.com/SAP/astonish/pkg/store"
)

type BudgetDimension string
type BudgetScope string
type BudgetReason string

const (
	BudgetTokens            BudgetDimension = "tokens"
	BudgetRun               BudgetScope     = "run"
	BudgetMandateTotal      BudgetScope     = "mandate_total"
	BudgetExhausted         BudgetReason    = "exhausted"
	BudgetInputDoesNotFit   BudgetReason    = "input_does_not_fit"
	BudgetSettlementOverrun BudgetReason    = "settlement_overrun"
)

// BudgetLimitError is a detached snapshot taken at the enforcement boundary.
// Used includes outstanding reservations, not just measured provider consumption.
// Requested is absent when there is no proposed action (e.g. Check).
type BudgetLimitError struct {
	Dimension                     BudgetDimension
	Scope                         BudgetScope
	Reason                        BudgetReason
	Used, Limit, Remaining        int64
	Requested                     *int64
	InputEstimate, OutputEstimate *int64
}

func (e *BudgetLimitError) Error() string {
	text := fmt.Sprintf("bot budget: dimension=%s scope=%s reason=%s limit=%d accounted=%d remaining=%d", e.Dimension, e.Scope, e.Reason, e.Limit, e.Used, e.Remaining)
	if e.Requested != nil {
		text += fmt.Sprintf(" requested=%d", *e.Requested)
	}
	if e.InputEstimate != nil {
		text += fmt.Sprintf(" input_estimate=%d", *e.InputEstimate)
	}
	if e.OutputEstimate != nil {
		text += fmt.Sprintf(" output_estimate=%d", *e.OutputEstimate)
	}
	return text
}
func (e *BudgetLimitError) Unwrap() error { return ErrBudgetExceeded }

func (b *SharedRunBudget) tokenFailure(reason BudgetReason, input, output *int64) error {
	if b.inputUsed > math.MaxInt64-b.outputUsed {
		return ErrBudgetExceeded
	}
	used := b.inputUsed + b.outputUsed
	e := &BudgetLimitError{Dimension: BudgetTokens, Scope: BudgetRun, Reason: reason, Used: used, Limit: *b.tokens, Remaining: max(0, *b.tokens-used), InputEstimate: copyBudgetInt(input), OutputEstimate: copyBudgetInt(output)}
	if input != nil && output != nil {
		n := *input + *output
		e.Requested = &n
	}
	return e
}

// BudgetFailureMessage recognizes native errors only; never parse provider text.
func BudgetFailureMessage(err error) (string, bool) {
	var e *BudgetLimitError
	if errors.As(err, &e) && e.Dimension == BudgetTokens && (e.Scope == BudgetRun || e.Scope == BudgetMandateTotal) {
		allowance := "run budget"
		if e.Scope == BudgetMandateTotal {
			allowance = "aggregate mandate allowance"
		}
		intro := "I stopped this run because its token budget has been reached."
		if e.Reason == BudgetInputDoesNotFit {
			intro = "I stopped this run because the next model request does not fit the remaining token budget."
		}
		if e.Reason == BudgetSettlementOverrun {
			intro = "I stopped this run because reported model usage exceeded its token budget."
		}
		text := fmt.Sprintf("### Budget limit reached\n\n%s\n\n- Token %s: %s tokens\n- Used or reserved: %s tokens\n- Remaining: %s tokens", intro, allowance, budgetNumber(e.Limit), budgetNumber(e.Used), budgetNumber(e.Remaining))
		if e.InputEstimate != nil {
			text += fmt.Sprintf("\n- Estimated next input: %s tokens (before an answer)", budgetNumber(*e.InputEstimate))
		} else if e.Requested != nil {
			text += fmt.Sprintf("\n- Next request reservation: %s tokens", budgetNumber(*e.Requested))
		}
		return text + "\n\nYou can increase this Bot’s token budget or shorten the task before sending a new request. Existing completed work has not been automatically retried.", true
	}
	var quota *store.BotMandateQuotaError
	if errors.As(err, &quota) {
		scope := BudgetRun
		switch quota.Dimension {
		case store.BotQuotaRunTokens:
		case store.BotQuotaTotalTokens:
			scope = BudgetMandateTotal
		default:
			return "I stopped this run because a mandate quota prevented further work. Review the quota before starting another run.", true
		}
		return BudgetFailureMessage(&BudgetLimitError{Dimension: BudgetTokens, Scope: scope, Reason: BudgetInputDoesNotFit, Used: quota.Used, Limit: quota.Limit, Remaining: max(0, quota.Limit-quota.Used), Requested: &quota.Requested})
	}
	if errors.Is(err, ErrBudgetExceeded) || errors.Is(err, ErrBudgetUnknownCost) {
		return "I stopped this run because its budget prevented further work. Exact limit or usage details are unavailable. Review the task and budget before starting another run.", true
	}
	return "", false
}

// budgetNumber formats enforcement facts without consulting mutable defaults.
func budgetNumber(n int64) string {
	s := fmt.Sprintf("%d", n)
	start := 0
	if n < 0 {
		start = 1
	}
	for i := len(s) - 3; i > start; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// BudgetFailureDetails excludes wrapper text, request bodies and provider secrets.
func BudgetFailureDetails(err error) string {
	var e *BudgetLimitError
	if errors.As(err, &e) {
		return e.Error()
	}
	var quota *store.BotMandateQuotaError
	if errors.As(err, &quota) {
		switch quota.Dimension {
		case store.BotQuotaRunTokens, store.BotQuotaTotalTokens:
			return fmt.Sprintf("bot mandate quota: dimension=%s accounted=%d requested=%d limit=%d", quota.Dimension, quota.Used, quota.Requested, quota.Limit)
		}
	}
	return "Budget enforcement stopped the run; no safe numeric diagnostic is available."
}
