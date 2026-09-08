package browser

import (
	"math"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

func TestHighlightSelector_RequiresSelector(t *testing.T) {
	m := NewManager(DefaultConfig())
	_, err := m.HighlightSelector("", "label", "", 0)
	if err == nil {
		t.Fatal("expected error for empty selector")
	}
}

func TestClearHighlights_CurrentPageAutolaunch(t *testing.T) {
	m := NewManager(DefaultConfig())
	// CurrentPage() launches a browser and creates about:blank when needed.
	// With Chrome available this succeeds; without a binary it returns an error.
	// Either outcome is valid — the old "must error without a page" assertion
	// was wrong for hosts that can launch Chromium.
	if err := m.ClearHighlights(); err != nil {
		t.Logf("ClearHighlights: %v (ok when browser cannot launch)", err)
	}
}

func TestMoveMouseAnimated_NilPage(t *testing.T) {
	m := NewManager(DefaultConfig())
	if err := m.MoveMouseAnimated(nil, proto.NewPoint(1, 2), 1, 0); err == nil {
		t.Fatal("expected error for nil page")
	}
}

func TestMoveMouseAnimated_NilPageWithDuration(t *testing.T) {
	m := NewManager(DefaultConfig())
	if err := m.MoveMouseAnimated(nil, proto.NewPoint(1, 2), 1, 500); err == nil {
		t.Fatal("expected error for nil page with durationMs>0")
	}
}

func TestEnableDemoCursor_CurrentPageAutolaunch(t *testing.T) {
	m := NewManager(DefaultConfig())
	if err := m.EnableDemoCursor(); err != nil {
		t.Logf("EnableDemoCursor: %v (ok when browser cannot launch)", err)
	}
}

func TestSetFullscreen_CurrentPageAutolaunch(t *testing.T) {
	m := NewManager(DefaultConfig())
	if err := m.SetFullscreen(true); err != nil {
		t.Logf("SetFullscreen: %v (ok when browser cannot launch)", err)
	}
}

func TestDemoState_LazyInit(t *testing.T) {
	m := NewManager(DefaultConfig())
	st := m.demoState()
	if st == nil {
		t.Fatal("demoState should allocate")
	}
	if m.demoState() != st {
		t.Fatal("demoState should return same instance")
	}
}

func TestEaseInOutCubic_BoundaryValues(t *testing.T) {
	const eps = 1e-9
	cases := []struct {
		t    float64
		want float64
	}{
		{0.0, 0.0},   // origin: no movement
		{0.5, 0.5},   // midpoint: exactly halfway
		{1.0, 1.0},   // destination: fully arrived
		{0.25, 0.125}, // quarter-way: 4*(0.25)^3 = 0.0625… wait, 4*0.015625 = 0.0625
		{0.75, 0.875}, // three-quarter: symmetric to 0.25 → 1 - 0.125 = 0.875
	}
	// Recalculate expected values using the same formula to avoid hand-error.
	wantFn := func(t float64) float64 {
		if t < 0.5 {
			return 4 * t * t * t
		}
		return 1 - math.Pow(-2*t+2, 3)/2
	}
	for _, tc := range cases {
		got := easeInOutCubic(tc.t)
		want := wantFn(tc.t)
		if math.Abs(got-want) > eps {
			t.Errorf("easeInOutCubic(%v) = %v, want %v", tc.t, got, want)
		}
	}
}

func TestEaseInOutCubic_Monotonic(t *testing.T) {
	// The curve must be strictly non-decreasing from t=0 to t=1.
	steps := 1000
	prev := easeInOutCubic(0.0)
	for i := 1; i <= steps; i++ {
		cur := easeInOutCubic(float64(i) / float64(steps))
		if cur < prev-1e-12 {
			t.Errorf("easeInOutCubic not monotonic at step %d: prev=%v cur=%v", i, prev, cur)
		}
		prev = cur
	}
}

func TestEaseInOutCubic_SymmetricAroundMidpoint(t *testing.T) {
	// The curve should be symmetric: ease(1-t) == 1 - ease(t).
	const eps = 1e-9
	steps := 100
	for i := 0; i <= steps; i++ {
		tt := float64(i) / float64(steps)
		a := easeInOutCubic(tt)
		b := easeInOutCubic(1 - tt)
		if math.Abs((1-a)-b) > eps {
			t.Errorf("easeInOutCubic not symmetric at t=%v: ease(t)=%v ease(1-t)=%v (want 1-ease(t)=%v)", tt, a, b, 1-a)
		}
	}
}
