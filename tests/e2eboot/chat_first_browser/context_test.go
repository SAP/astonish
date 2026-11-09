package main

import (
	"context"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"os"
	"testing"
	"time"
)

func contextTestPage(t *testing.T) *rod.Page {
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		t.Skip("native Chrome required")
	}
	l := launcher.New().Bin(bin).Headless(true)
	b := rod.New().ControlURL(l.MustLaunch()).MustConnect()
	t.Cleanup(func() { b.MustClose(); l.Cleanup() })
	return b.MustPage()
}

func TestIndependentPageTimeoutPreservesSharedStage(t *testing.T) {
	page := contextTestPage(t).Timeout(time.Minute)
	child := independentPageTimeout(page, time.Second)
	if err := page.GetContext().Err(); err != nil {
		t.Fatalf("API helper canceled shared stage: %v", err)
	}
	child.CancelTimeout()
	if err := page.GetContext().Err(); err != nil {
		t.Fatalf("child cleanup canceled shared stage: %v", err)
	}
	if got := page.MustEval(`() => 42`).Int(); got != 42 {
		t.Fatal(got)
	}
}

func TestIndependentPageTimeoutRecoversExpiredStage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	page := contextTestPage(t).Context(ctx).Timeout(time.Minute)
	cancel()
	child := independentPageTimeout(page, time.Second)
	defer child.CancelTimeout()
	if err := child.GetContext().Err(); err != nil {
		t.Fatalf("fresh operation inherited canceled stage: %v", err)
	}
	if got := child.MustEval(`() => 42`).Int(); got != 42 {
		t.Fatal(got)
	}
}
