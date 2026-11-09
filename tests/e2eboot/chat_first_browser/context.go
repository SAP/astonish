package main

import (
	"context"
	"github.com/go-rod/rod"
	"time"
)

// Each operation owns its timeout. CancelTimeout must never be called on the
// caller's page: Rod cancels that shared context before returning its parent.
func independentPageTimeout(page *rod.Page, timeout time.Duration) *rod.Page {
	return page.Context(context.Background()).Timeout(timeout)
}
