package events

import (
	"reflect"
	"testing"
)

func TestBotReportIdentityDedupAndReset(t *testing.T) {
	const id = "bot-report:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	for _, linear := range []bool{false, true} {
		tr := NewTranscript()
		tr.LinearThread = linear
		report := NewBotReport(id, "row-a", "same")
		tr.Apply(report)
		tr.Apply(NewBotReport("bot-report:bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee", "row-b", "same"))
		before := append([]Item(nil), tr.Items...)
		tr.Apply(report)
		if !reflect.DeepEqual(before, tr.Items) {
			t.Fatal("duplicate moved/modified publication")
		}
		tr.Apply(NewText("later"))
		tr.Apply(NewDone())
		if len(tr.Items) != 3 || tr.Items[0].Content != "same" || tr.Items[2].Content != "later" {
			t.Fatalf("report merged: %+v", tr.Items)
		}
		tr.Reset()
		tr.Apply(report)
		if len(tr.Items) != 1 {
			t.Fatal("reset retained report dedup")
		}
	}
}

func TestBotReportDoesNotFinalizeRunningToolsOrInheritRouting(t *testing.T) {
	tr := NewTranscript()
	tr.Apply(Event{Kind: KindRoutingInfo, RoutingModel: "old"})
	tr.Apply(NewToolCall("read_file", "one", nil))
	status := tr.Status
	tr.Apply(NewBotReport("bot-report:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "row", "report"))
	if !tr.Streaming || tr.Status != status || tr.Items[0].Steps[0].Status != "running" {
		t.Fatalf("publication finalized other work: %+v", tr)
	}
	tr.Apply(Event{Kind: KindRoutingInfo, RoutingModel: "new"})
	if tr.Items[1].RoutingModel != "" {
		t.Fatal("publication inherited later routing")
	}
	tr.Apply(NewToolCall("read_file", "two", nil))
	tr.Apply(Event{Kind: KindRoutingInfo, RoutingModel: "newer"})
	if tr.Items[1].RoutingModel != "" {
		t.Fatal("backward routing scan stamped publication")
	}
}

func TestBotReportIdentityValidation(t *testing.T) {
	for _, id := range []string{"", "bot-report:run", "bot-report:AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", "bot-report:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\n"} {
		if IsBotReportInvocation(id) {
			t.Fatalf("invalid canonical ID: %q", id)
		}
		tr := NewTranscript()
		tr.Apply(NewBotReport(id, "row", "# Scheduled report"))
		if len(tr.Items) != 0 {
			t.Fatal("invalid typed publication accepted")
		}
	}
	tr := NewTranscript()
	tr.Apply(NewBotReport("bot-report:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "row", ""))
	if len(tr.Items) != 0 {
		t.Fatal("empty report added")
	}
	// Ordinary text/Done pairs remain soft, not newly minted report boundaries.
	tr.Apply(NewText("one"))
	tr.Apply(NewDone())
	tr.Apply(NewText("two"))
	tr.Apply(NewDone())
	if len(tr.Items) != 1 || tr.Items[0].Content != "onetwo" {
		t.Fatalf("ordinary Done contract changed: %+v", tr.Items)
	}
}
