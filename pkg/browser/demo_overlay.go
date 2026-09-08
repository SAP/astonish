package browser

import (
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// demoOverlayState tracks highlight overlays and the visible demo cursor.
type demoOverlayState struct {
	mu              sync.Mutex
	highlightSeq    atomic.Uint64
	demoCursorOn    bool
	removeCursorDoc func() error
}

const highlightInjectJS = `(opts) => {
  const sel = opts.selector || '';
  const label = opts.label || '';
  const color = opts.color || '#FF6B2C';
  const durationMs = opts.duration_ms || 0;
  const id = opts.id;
  let el = null;
  if (sel) {
    try { el = document.querySelector(sel); } catch (e) { return JSON.stringify({ok:false,error:'bad selector'}); }
  }
  if (!el) return JSON.stringify({ok:false,error:'element not found'});

  if (!window.__astonishHighlights) window.__astonishHighlights = {};
  const prev = window.__astonishHighlights[id];
  if (prev) {
    if (prev.overlay && prev.overlay.remove) prev.overlay.remove();
    if (prev.caption && prev.caption.remove) prev.caption.remove();
    if (prev.ro) prev.ro.disconnect();
    if (prev.onScroll) {
      window.removeEventListener('scroll', prev.onScroll, true);
      window.removeEventListener('resize', prev.onScroll);
    }
    if (prev.timer) clearTimeout(prev.timer);
  }

  const overlay = document.createElement('div');
  overlay.setAttribute('data-astonish-highlight', id);
  overlay.style.cssText = [
    'position:fixed','pointer-events:none','z-index:2147483646',
    'border:3px solid ' + color,
    'box-shadow:0 0 0 2px rgba(0,0,0,0.25), 0 0 12px ' + color,
    'border-radius:6px','box-sizing:border-box','transition:all 80ms linear'
  ].join(';');

  let caption = null;
  if (label) {
    caption = document.createElement('div');
    caption.setAttribute('data-astonish-highlight-label', id);
    caption.textContent = label;
    caption.style.cssText = [
      'position:fixed','pointer-events:none','z-index:2147483647',
      'background:' + color,'color:#fff','font:600 12px/1.3 system-ui,sans-serif',
      'padding:4px 8px','border-radius:4px','white-space:nowrap',
      'box-shadow:0 2px 8px rgba(0,0,0,0.25)'
    ].join(';');
  }

  function place() {
    const r = el.getBoundingClientRect();
    const pad = 4;
    overlay.style.left = (r.left - pad) + 'px';
    overlay.style.top = (r.top - pad) + 'px';
    overlay.style.width = Math.max(0, r.width + pad * 2) + 'px';
    overlay.style.height = Math.max(0, r.height + pad * 2) + 'px';
    if (caption) {
      caption.style.left = Math.max(0, r.left - pad) + 'px';
      caption.style.top = Math.max(0, r.top - pad - 26) + 'px';
    }
  }

  document.documentElement.appendChild(overlay);
  if (caption) document.documentElement.appendChild(caption);
  place();

  const onScroll = function() { place(); };
  window.addEventListener('scroll', onScroll, true);
  window.addEventListener('resize', onScroll);
  let ro = null;
  if (window.ResizeObserver) {
    ro = new ResizeObserver(onScroll);
    ro.observe(el);
  }

  let timer = null;
  if (durationMs > 0) {
    timer = setTimeout(function() {
      if (window.__astonishClearHighlight) window.__astonishClearHighlight(id);
    }, durationMs);
  }

  window.__astonishHighlights[id] = {overlay: overlay, caption: caption, onScroll: onScroll, ro: ro, timer: timer};

  if (!window.__astonishClearHighlight) {
    window.__astonishClearHighlight = function(hid) {
      const h = window.__astonishHighlights && window.__astonishHighlights[hid];
      if (!h) return;
      if (h.overlay && h.overlay.remove) h.overlay.remove();
      if (h.caption && h.caption.remove) h.caption.remove();
      if (h.ro) h.ro.disconnect();
      if (h.onScroll) {
        window.removeEventListener('scroll', h.onScroll, true);
        window.removeEventListener('resize', h.onScroll);
      }
      if (h.timer) clearTimeout(h.timer);
      delete window.__astonishHighlights[hid];
    };
    window.__astonishClearAllHighlights = function() {
      const ids = Object.keys(window.__astonishHighlights || {});
      for (var i = 0; i < ids.length; i++) window.__astonishClearHighlight(ids[i]);
    };
  }

  return JSON.stringify({ok:true,id:id});
}`

const clearAllHighlightsJS = `() => {
  if (window.__astonishClearAllHighlights) window.__astonishClearAllHighlights();
  return JSON.stringify({ok:true});
}`

// demoCursorBodyJS is the install body shared by Eval (function) and
// EvalOnNewDocument (IIFE source).
const demoCursorBodyJS = `
  if (window.__astonishDemoCursorInstalled) {
    var existing = document.getElementById('astonish-demo-cursor');
    if (existing) return;
    window.__astonishDemoCursorInstalled = false;
  }
  window.__astonishDemoCursorInstalled = true;

  function ensure() {
    var c = document.getElementById('astonish-demo-cursor');
    if (c) return c;
    c = document.createElement('div');
    c.id = 'astonish-demo-cursor';
    c.setAttribute('aria-hidden', 'true');
    c.style.cssText = [
      'position:fixed','left:0','top:0','width:22px','height:22px',
      'margin-left:-2px','margin-top:-2px','z-index:2147483647',
      'pointer-events:none','transform:translate(-100px,-100px)',
      'transition:transform 40ms linear'
    ].join(';');
    c.innerHTML = '<svg width="22" height="22" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">' +
      '<path d="M4 2 L4 18 L9 13 L12 20 L14.5 19 L11.5 12 L18 12 Z" fill="#111" stroke="#fff" stroke-width="1.2"/>' +
      '</svg>';
    (document.documentElement || document.body).appendChild(c);
    return c;
  }

  window.__astonishMoveDemoCursor = function(x, y) {
    var c = ensure();
    c.style.transform = 'translate(' + x + 'px,' + y + 'px)';
  };

  window.__astonishShowDemoCursor = function(show) {
    var c = ensure();
    c.style.display = show === false ? 'none' : 'block';
  };

  ensure();
`

const demoCursorEvalJS = `() => {` + demoCursorBodyJS + `}`

const demoCursorOnNewDocJS = `(function() {` + demoCursorBodyJS + `})();`

func (m *Manager) demoState() *demoOverlayState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.demoOverlay == nil {
		m.demoOverlay = &demoOverlayState{}
	}
	return m.demoOverlay
}

// HighlightSelector draws a visible square overlay around the first element
// matching selector. durationMs 0 keeps the highlight until ClearHighlights.
func (m *Manager) HighlightSelector(selector, label, color string, durationMs int) (string, error) {
	if selector == "" {
		return "", fmt.Errorf("selector is required")
	}
	id := fmt.Sprintf("hl-%d", m.demoState().highlightSeq.Add(1))
	return m.highlightSelectorWithID(selector, label, color, durationMs, id)
}

// HighlightElement marks the element with a temporary attribute and highlights it.
func (m *Manager) HighlightElement(el *rod.Element, label, color string, durationMs int) (string, error) {
	if el == nil {
		return "", fmt.Errorf("element is nil")
	}
	id := fmt.Sprintf("hl-%d", m.demoState().highlightSeq.Add(1))
	attr := "data-astonish-hl-" + id
	if _, err := el.Eval(`(n) => { this.setAttribute(n, '1'); return true; }`, attr); err != nil {
		return "", fmt.Errorf("highlight mark: %w", err)
	}
	hid, err := m.highlightSelectorWithID("["+attr+"]", label, color, durationMs, id)
	if err != nil {
		_, _ = el.Eval(`(n) => { this.removeAttribute(n); return true; }`, attr)
		return "", err
	}
	return hid, nil
}

func (m *Manager) highlightSelectorWithID(selector, label, color string, durationMs int, id string) (string, error) {
	pg, err := m.CurrentPage()
	if err != nil {
		return "", err
	}
	if color == "" {
		color = "#FF6B2C"
	}
	opts := map[string]any{
		"selector":    selector,
		"label":       label,
		"color":       color,
		"duration_ms": durationMs,
		"id":          id,
	}
	res, err := pg.Eval(highlightInjectJS, opts)
	if err != nil {
		return "", fmt.Errorf("highlight inject: %w", err)
	}
	raw := ""
	if res != nil {
		raw = res.Value.Str()
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		ID    string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return "", fmt.Errorf("highlight parse %q: %w", raw, err)
	}
	if !out.OK {
		return "", fmt.Errorf("highlight: %s", out.Error)
	}
	return id, nil
}

// ClearHighlights removes all highlight overlays on the current page.
func (m *Manager) ClearHighlights() error {
	pg, err := m.CurrentPage()
	if err != nil {
		return err
	}
	_, err = pg.Eval(clearAllHighlightsJS)
	return err
}

// EnableDemoCursor injects a visible demo cursor that survives navigations.
func (m *Manager) EnableDemoCursor() error {
	pg, err := m.CurrentPage()
	if err != nil {
		return err
	}
	st := m.demoState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.demoCursorOn {
		_, _ = pg.Eval(demoCursorEvalJS)
		return nil
	}
	remove, err := pg.EvalOnNewDocument(demoCursorOnNewDocJS)
	if err != nil {
		return fmt.Errorf("demo cursor EvalOnNewDocument: %w", err)
	}
	if _, err := pg.Eval(demoCursorEvalJS); err != nil {
		_ = remove()
		return fmt.Errorf("demo cursor inject: %w", err)
	}
	st.demoCursorOn = true
	st.removeCursorDoc = remove
	return nil
}

// DisableDemoCursor stops re-injecting the cursor on new documents.
func (m *Manager) DisableDemoCursor() error {
	st := m.demoState()
	st.mu.Lock()
	remove := st.removeCursorDoc
	st.demoCursorOn = false
	st.removeCursorDoc = nil
	st.mu.Unlock()
	if remove != nil {
		_ = remove()
	}
	pg, err := m.CurrentPage()
	if err != nil {
		return nil
	}
	_, _ = pg.Eval(`() => {
		var c = document.getElementById('astonish-demo-cursor');
		if (c) c.remove();
		window.__astonishDemoCursorInstalled = false;
		window.__astonishMoveDemoCursor = undefined;
	}`)
	return nil
}

// SyncDemoCursorOverlay moves the visible cursor overlay to (x, y) in CSS pixels.
func (m *Manager) SyncDemoCursorOverlay(x, y float64) error {
	if err := m.EnableDemoCursor(); err != nil {
		return err
	}
	pg, err := m.CurrentPage()
	if err != nil {
		return err
	}
	_, err = pg.Eval(`(x, y) => {
		if (window.__astonishMoveDemoCursor) {
			window.__astonishMoveDemoCursor(x, y);
			return true;
		}
		return false;
	}`, x, y)
	return err
}

// easeInOutCubic maps a linear progress value t in [0,1] to a cubic
// ease-in-out curve: slow start, fast middle, slow end.
// Boundary contracts: easeInOutCubic(0)==0, easeInOutCubic(0.5)==0.5, easeInOutCubic(1)==1.
func easeInOutCubic(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}

// MoveMouseAnimated moves the real mouse (and demo cursor overlay) to pt.
// When durationMs > 0, the movement is spread over that many milliseconds
// using a cubic ease-in-out curve so the visible cursor glides naturally
// (slow start, fast middle, slow end). When durationMs <= 0, the existing
// fast linear movement is used for backward-compatible instant movement.
//
// Note: actual wall-clock duration is approximate — stepSleep*(steps-1) plus
// per-frame CDP round-trip latency (MoveTo + SyncDemoCursorOverlay).
func (m *Manager) MoveMouseAnimated(pg *rod.Page, pt proto.Point, steps int, durationMs int) error {
	if pg == nil {
		return fmt.Errorf("page is nil")
	}
	if steps < 1 {
		steps = 12
	}
	if err := m.EnableDemoCursor(); err != nil {
		return err
	}
	if durationMs <= 0 {
		// Fast path: original behavior, no inter-step delay.
		if err := pg.Mouse.MoveLinear(pt, steps); err != nil {
			return err
		}
		return m.SyncDemoCursorOverlay(pt.X, pt.Y)
	}
	// Timed natural movement: distribute durationMs across steps using cubic
	// ease-in-out. stepSleep is approximate — CDP latency adds to actual duration.
	startPos := pg.Mouse.Position()
	deltaX := pt.X - startPos.X
	deltaY := pt.Y - startPos.Y
	stepSleep := time.Duration(durationMs) * time.Millisecond / time.Duration(steps)
	for i := 1; i <= steps; i++ {
		eased := easeInOutCubic(float64(i) / float64(steps))
		interPt := proto.NewPoint(
			startPos.X+deltaX*eased,
			startPos.Y+deltaY*eased,
		)
		if err := pg.Mouse.MoveTo(interPt); err != nil {
			return err
		}
		// Best-effort overlay sync: dropped intermediate frames are acceptable.
		_ = m.SyncDemoCursorOverlay(interPt.X, interPt.Y)
		if i < steps {
			time.Sleep(stepSleep)
		}
	}
	// Final sync at exact destination to correct any floating-point drift.
	return m.SyncDemoCursorOverlay(pt.X, pt.Y)
}

// SetFullscreen toggles Chromium window fullscreen via CDP (best-effort).
// X11grab still captures the display; the goal is minimizing browser chrome.
func (m *Manager) SetFullscreen(enabled bool) error {
	pg, err := m.CurrentPage()
	if err != nil {
		return err
	}
	win, err := proto.BrowserGetWindowForTarget{}.Call(pg)
	if err != nil {
		if enabled {
			if kErr := pg.Keyboard.Type(input.F11); kErr != nil {
				return fmt.Errorf("fullscreen: get window: %w (F11 fallback: %v)", err, kErr)
			}
			return nil
		}
		return fmt.Errorf("fullscreen: get window: %w", err)
	}
	state := proto.BrowserWindowStateNormal
	if enabled {
		state = proto.BrowserWindowStateFullscreen
	}
	setBounds := proto.BrowserSetWindowBounds{
		WindowID: win.WindowID,
		Bounds:   &proto.BrowserBounds{WindowState: state},
	}
	if err := setBounds.Call(pg); err != nil {
		return fmt.Errorf("set window bounds: %w", err)
	}
	return nil
}
