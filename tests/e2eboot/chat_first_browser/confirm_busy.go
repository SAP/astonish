package main

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
)

const explicitBusy = "Bot is still running or finishing cleanup; wait briefly and retry this proposal"

type confirmAttempt struct {
	Proposal store.BotControlProposal
	Status   int
	Body     string
	At       time.Time
}

// Only schedule_pause has no execution preflight side effects. Unknown outcomes,
// old checkpoints, and all other decisions remain non-replayable.
func permitBusyRetry(a confirmAttempt, p store.BotControlProposal, b store.Bot, now time.Time) bool {
	return a.Status == 409 && strings.TrimSpace(a.Body) == explicitBusy &&
		p.Payload.Operation == store.BotControlSchedulePause && p.Status == store.BotControlProposed &&
		p.DecidedAt == nil && now.Before(p.ExpiresAt) && reflect.DeepEqual(a.Proposal, p) &&
		b.ID == p.BotID && b.Revision == p.ExpectedRevision && b.Version == p.Version
}

func confirmWithBusyRetry(cp *smokeCheckpoint, p store.BotControlProposal, persist func(),
	fresh func() (store.BotControlProposal, store.Bot), idle func(), click func() (int, string)) error {
	id := p.ID.String()
	for n := 0; n < 3; n++ {
		if cp.Consents[id] != "" {
			attempts := cp.ConfirmAttempts[id]
			if len(attempts) == 0 || cp.Consents[id] != p.Digest {
				return fmt.Errorf("ambiguous prior consent; no replay: %s", id)
			}
			idle()
			current, b := fresh()
			if !permitBusyRetry(attempts[len(attempts)-1], current, b, time.Now()) || !reflect.DeepEqual(p, current) {
				return fmt.Errorf("confirmation not explicitly busy and unchanged; no replay: %s", id)
			}
		}
		if cp.ConfirmAttempts == nil {
			cp.ConfirmAttempts = map[string][]confirmAttempt{}
		}
		cp.Consents[id] = p.Digest // Never erase historical consent intent.
		cp.ConfirmAttempts[id] = append(cp.ConfirmAttempts[id], confirmAttempt{Proposal: p, At: time.Now()})
		persist() // A crash after this point remains ambiguous.
		status, body := click()
		i := len(cp.ConfirmAttempts[id]) - 1
		cp.ConfirmAttempts[id][i].Status, cp.ConfirmAttempts[id][i].Body = status, body
		persist()
		if status >= 200 && status < 300 {
			return nil
		}
		current, b := fresh()
		if !permitBusyRetry(cp.ConfirmAttempts[id][i], current, b, time.Now()) {
			return fmt.Errorf("confirmation rejected/unknown (HTTP %d): %s; no replay", status, body)
		}
	}
	return fmt.Errorf("explicit busy confirmation limit reached; no further clicks")
}

// Observe the actual UI request; never issue a substitute mutation. Match URL,
// method, digest and decision before accepting response evidence.
func captureConfirmClick(page *rod.Page, path, id, digest string) (int, string) {
	page.MustEval(`(path,digest) => {
 const previous=window.fetch;
 window.__confirmCapture={done:false,status:0,body:""};
 window.__restoreConfirmFetch=()=>{window.fetch=previous};
 window.fetch=async function(input,init) {
  const url=new URL(typeof input==='string'?input:input.url,location.href);
  let body; try {body=JSON.parse(init?.body)} catch {}
  const match=url.pathname===path && init?.method==='POST' && body?.digest===digest && body?.decision==='confirm';
  try { const response=await previous.apply(this,arguments);
   if(match) {const text=await response.clone().text(); window.__confirmCapture={done:true,status:response.status,body:text};}
   return response;
  } catch(error) {if(match) window.__confirmCapture={done:true,status:0,body:String(error)}; throw error;}
 };
 }`, path, digest)
	defer page.MustEval(`() => window.__restoreConfirmFetch()`)
	page.Timeout(30*time.Second).MustElementR(`[aria-label="Bot workspace"] [data-review-id="`+id+`"] button`, "^Confirm exact change$").MustClick()
	boundedWait("authoritative confirmation response "+id, 30*time.Second, func() bool { return page.MustEval(`() => window.__confirmCapture.done`).Bool() })
	return page.MustEval(`() => window.__confirmCapture.status`).Int(), page.MustEval(`() => window.__confirmCapture.body`).Str()
}
