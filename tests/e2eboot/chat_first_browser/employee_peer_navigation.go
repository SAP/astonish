package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/google/uuid"
)

const employeePeerTitle = "Preservation witness"

// Observe the mounted workspace's existing StudioChat props, not a guessed ID,
// stale URL suffix, or React alternate fiber. No product state is mutated. The
// workspace has no DOM bot-id attribute; fail closed if its props shape changes.
const employeePeerNavigationJS = `(base, originalID) => new Promise((resolve, reject) => {
 const prefix = base + '/#/chat/bots/personal/';
 const original = prefix + originalID;
 let timer;
 const deadline = Date.now() + 30000;
 const visible = e => e.checkVisibility({checkOpacity:true,checkVisibilityCSS:true});
 const finish = (err, id) => { clearTimeout(timer); err ? reject(new Error(err)) : resolve(id); };
 const poll = () => {
  try {
   const alerts = [...document.querySelectorAll('[role="alert"]')].filter(visible);
   if (alerts.length) return finish('preservation peer UI error: '+alerts.map(e=>e.innerText).join('\n'));
   const url = location.href;
   if (!url.startsWith(prefix) || !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(url.slice(prefix.length)) || url.slice(prefix.length) === '00000000-0000-0000-0000-000000000000') return finish('preservation peer left canonical owned personal Home: '+url);
   const workspaces = [...document.querySelectorAll('[aria-label="Bot workspace"]')].filter(visible);
   if (workspaces.length > 1) return finish('multiple preservation peer workspaces');
   const dialogs = [...document.querySelectorAll('[role="dialog"]')].filter(e=>visible(e) && e.querySelector('[aria-label="Bot name"]'));
   if (dialogs.length > 1) return finish('multiple preservation peer creation dialogs');
   if (url !== original && dialogs.length === 0 && workspaces.length === 1) {
    const w = workspaces[0];
    const key = Object.keys(w).find(k=>k.startsWith('__reactProps$'));
    const threads = [];
    const visit = node => {
     if (!node || typeof node !== 'object') return;
     if (Array.isArray(node)) { node.forEach(visit); return; }
     const props = node.props;
     if (!props) return;
     if (props.botThread) threads.push(props.botThread);
     visit(props.children);
    };
    visit(w[key]?.children);
    if (threads.length > 1) return finish('multiple preservation peer workspace identities');
    if (threads.length === 1) {
     const thread = threads[0];
     if (thread.scope !== 'personal') return finish('preservation peer workspace scope mismatch');
     if (thread.botId === url.slice(prefix.length)) return finish(null, thread.botId);
     if (thread.botId !== originalID) return finish('preservation peer workspace identity mismatch');
    }
   }
   if (Date.now() >= deadline) return finish('timed out waiting for preservation peer route and workspace identity');
   timer = setTimeout(poll, 50);
  } catch (err) { finish(String(err)); }
 };
 poll();
})`

func employeeWaitPeerHome(page *rod.Page, base, originalID string) (string, error) {
	page = page.Timeout(35 * time.Second)
	defer page.CancelTimeout()
	result, err := page.Evaluate(rod.Eval(employeePeerNavigationJS, base, originalID).ByPromise())
	if err != nil {
		return "", err
	}
	return result.Value.Str(), nil
}

// List readback is independent of browser navigation. Existing peers cannot be
// mistaken for this creation, and multiple new peers never trigger a second create.
func employeeCreatedPeer(raw, peerID string, original store.Bot, before map[uuid.UUID]bool) (store.Bot, error) {
	var list struct {
		Bots []store.Bot `json:"bots"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return store.Bot{}, err
	}
	var added []store.Bot
	for _, b := range list.Bots {
		if !before[b.ID] {
			added = append(added, b)
		}
	}
	if len(added) != 1 {
		return store.Bot{}, fmt.Errorf("expected one newly created preservation peer, found %d", len(added))
	}
	b := added[0]
	if b.ID == uuid.Nil || b.ID == original.ID || b.ID.String() != peerID || b.Scope != store.BotScopePersonal || original.Scope != store.BotScopePersonal || b.OwnerID == uuid.Nil || b.OwnerID != original.OwnerID || b.Name != employeePeerTitle || b.HomeSessionID == uuid.Nil || b.HomeSessionID == original.HomeSessionID {
		return store.Bot{}, fmt.Errorf("preservation peer durable identity mismatch")
	}
	return b, nil
}
