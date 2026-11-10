package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

func TestBotSchedulePresentationRenderLiveReplay(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, status := range []store.BotOperationStatus{store.BotOperationApplied, store.BotOperationFailed, store.BotOperationBlocked, store.BotOperationCancelled} {
			t.Run(fmt.Sprintf("replay=%t/%s", replay, status), func(t *testing.T) {
				v := store.BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: uuid.New(), Revision: 1, StateSequence: 1, Operation: store.BotControlScheduleCreate, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Presentation: store.BotOperationPresentationInternal, Summary: "Change queued."}
				fresh := func() *events.Transcript {
					tr := events.NewTranscript()
					tr.BotID, tr.SessionID = v.BotID.String(), v.HomeSessionID.String()
					return tr
				}
				m := model{theme: DefaultTheme(), tr: fresh(), width: 100, height: 40, ready: true}
				journal := []events.Event{events.NewUser("Schedule a weather check every minute.")}
				apply := func(ev events.Event) {
					journal = append(journal, ev)
					if replay {
						m.tr = fresh()
						for _, e := range journal {
							m.tr.Apply(e)
						}
					} else {
						m.tr.Apply(ev)
					}
				}
				if !replay {
					m.tr.Apply(journal[0])
				}
				pending := v
				apply(events.Event{Kind: events.KindBotOperation, BotOperation: &pending})
				v.Revision++
				v.StateSequence++
				v.Status = store.BotOperationApplying
				v.Summary = "Applying change."
				applying := v
				apply(events.Event{Kind: events.KindBotOperation, BotOperation: &applying})
				apply(events.NewDone())
				out, hits, _ := m.renderTranscript()
				for _, hit := range hits {
					if hit.kind == events.ItemSystem || hit.kind == events.ItemAgent {
						t.Fatalf("internal operation rendered: %s", stripANSI(out))
					}
				}
				if strings.Contains(stripANSI(out), "Change queued") || strings.Contains(stripANSI(out), "Applying change") || !m.tr.Streaming {
					t.Fatal("internal presentation lost its separation from state")
				}
				v.Revision++
				v.StateSequence++
				v.Status = status
				v.Presentation = store.BotOperationPresentationConversation
				v.Summary = "I couldn't complete the schedule change. No schedule change was verified."
				if status == store.BotOperationApplied {
					v.VerifiedSchedule = &store.BotOperationScheduleView{Enabled: true, Version: 1}
					v.Summary = "I've created the schedule. It runs every minute."
				}
				if status == store.BotOperationCancelled {
					v.Summary = "The schedule change was cancelled."
				}
				final := v
				apply(events.Event{Kind: events.KindBotOperation, BotOperation: &final})
				apply(events.Event{Kind: events.KindBotOperation, BotOperation: &final})
				apply(events.Event{Kind: events.KindBotOperation, BotOperation: &pending})
				apply(events.NewDone())
				out, hits, _ = m.renderTranscript()
				plain := strings.Join(strings.Fields(stripANSI(out)), " ")
				agents, systems := 0, 0
				for _, hit := range hits {
					if hit.kind == events.ItemAgent {
						agents++
					}
					if hit.kind == events.ItemSystem {
						systems++
					}
				}
				if agents != 1 || systems != 0 || strings.Count(plain, v.Summary) != 1 || m.tr.Streaming {
					t.Fatalf("not one final assistant message: agents=%d systems=%d streaming=%t\n%s", agents, systems, m.tr.Streaming, plain)
				}
				for _, forbidden := range []string{"Change queued", "Applying change", "applied —", "failed —", "Schedule enabled."} {
					if strings.Contains(plain, forbidden) {
						t.Fatalf("operation scaffolding leaked %q: %s", forbidden, plain)
					}
				}
			})
		}
	}
}
