package daemon

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// These stages separate production startup from the reproduction's own physical
// document verification. Counters never read bodies or start observer processes.
type continuationStage string

const (
	continuationCreate       continuationStage = "create"
	continuationInitial      continuationStage = "initial-question"
	continuationAnswer       continuationStage = "answer-http"
	continuationStartup      continuationStage = "answer-preprocessing"
	continuationPrepare      continuationStage = "production-prepare"
	continuationDocuments    continuationStage = "test-document-verification"
	continuationPersistence  continuationStage = "followup-persistence"
	continuationVerification continuationStage = "test-final-verification"
)

type continuationOperation string

const (
	continuationOpen    continuationOperation = "open"
	continuationPut     continuationOperation = "put"
	continuationConsume continuationOperation = "consume-close"
)

type continuationCountKey struct {
	Stage     continuationStage
	Operation continuationOperation
	Kind      store.BotContentKind
	Key       string
}

type continuationCount struct {
	Calls, Errors int
	Elapsed       time.Duration
	Caller        string
}

type continuationMetrics struct {
	mu                    sync.Mutex
	stage                 continuationStage
	counts                map[continuationCountKey]continuationCount
	accepted, generation2 time.Time
}

func (m *continuationMetrics) setStage(stage continuationStage) continuationStage {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := m.stage
	m.stage = stage
	return previous
}

func (m *continuationMetrics) begin(op continuationOperation, kind store.BotContentKind, key string) func(string, error) {
	started := time.Now()
	m.mu.Lock()
	stage := m.stage
	m.mu.Unlock()
	// Open and consume-close are separate, nonoverlapping spans. The latter
	// includes stream reads, helper exit validation, and ownership-checked cleanup.
	return func(resultKey string, err error) {
		if resultKey != "" {
			key = resultKey
		}
		m.record(continuationCountKey{stage, op, kind, key}, time.Since(started), err)
	}
}

func (m *continuationMetrics) record(key continuationCountKey, elapsed time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts == nil {
		m.counts = make(map[continuationCountKey]continuationCount)
	}
	count := m.counts[key]
	if count.Calls == 0 || key.Stage == "read-return-to-next-producer" {
		// One short stack per stage/key, not a full goroutine dump per IO.
		var pcs [28]uintptr
		n := runtime.Callers(3, pcs[:])
		frames := runtime.CallersFrames(pcs[:n])
		var names []string
		for {
			frame, more := frames.Next()
			if strings.Contains(frame.Function, "github.com/SAP/astonish/") {
				names = append(names, strings.TrimPrefix(frame.Function, "github.com/SAP/astonish/"))
			}
			if !more {
				break
			}
		}
		count.Caller = strings.Join(names, " <- ")
		if key.Stage == "read-return-to-next-producer" {
			fmt.Printf("BETWEEN_TRACE kind=%s key=%s caller=%s\n", key.Kind, key.Key, count.Caller)
		}
	}
	count.Calls++
	if err != nil {
		count.Errors++
	}
	count.Elapsed += elapsed
	m.counts[key] = count
}

func (m *continuationMetrics) acceptAnswer() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accepted = time.Now()
	m.stage = continuationStartup
}

func (m *continuationMetrics) secondGeneration(t *testing.T) {
	m.mu.Lock()
	m.generation2 = time.Now()
	elapsed := m.generation2.Sub(m.accepted)
	accepted, generation2 := m.accepted, m.generation2
	m.stage = continuationPersistence
	m.mu.Unlock()
	t.Logf("STARTUP answer_accepted_to_generation2=%s", elapsed)
	if err := continuationStartupLatencyError(accepted, generation2); err != nil {
		t.Error(err)
	}
	m.report(t, "generation2")
}

func (m *continuationMetrics) report(t *testing.T, boundary string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	totals := make(map[string]continuationCount)
	keys := make([]continuationCountKey, 0, len(m.counts))
	for key, count := range m.counts {
		keys = append(keys, key)
		for _, group := range []string{"total/" + string(key.Operation), "stage/" + string(key.Stage) + "/" + string(key.Operation), "kind/" + string(key.Kind) + "/" + string(key.Operation)} {
			v := totals[group]
			v.Calls += count.Calls
			v.Errors += count.Errors
			v.Elapsed += count.Elapsed
			totals[group] = v
		}
	}
	groups := make([]string, 0, len(totals))
	for group := range totals {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		v := totals[group]
		t.Logf("CONTENT_COUNTS boundary=%s group=%s calls=%d errors=%d elapsed=%s", boundary, group, v.Calls, v.Errors, v.Elapsed)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m.counts[keys[i]].Calls != m.counts[keys[j]].Calls {
			return m.counts[keys[i]].Calls > m.counts[keys[j]].Calls
		}
		return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j])
	})
	for _, key := range keys {
		v := m.counts[key]
		t.Logf("CONTENT_KEY boundary=%s stage=%s op=%s kind=%s key=%s calls=%d errors=%d elapsed=%s caller=%s", boundary, key.Stage, key.Operation, key.Kind, key.Key, v.Calls, v.Errors, v.Elapsed, v.Caller)
	}
}

func (m *continuationMetrics) startupOpenErrors() []error {
	// Called with mu held. Count by immutable object, not stage: a legitimate
	// bootstrap object had <=10 reads in the baseline; the two hot objects had
	// 226 and 154. Sixteen allows ordinary guards without accepting O(turns*IO).
	counts := make(map[continuationCountKey]int)
	for key, count := range m.counts {
		if key.Operation != continuationOpen || (key.Stage != continuationStartup && key.Stage != continuationPrepare) {
			continue
		}
		key.Stage = continuationStartup
		counts[key] += count.Calls
	}
	var errs []error
	for key, calls := range counts {
		if calls > 16 {
			errs = append(errs, fmt.Errorf("startup repeated-open regression: kind=%s key=%s opens=%d limit=16", key.Kind, key.Key, calls))
		}
	}
	return errs
}

func (m *continuationMetrics) assertStartup(t *testing.T) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.accepted.IsZero() {
		return
	} // Earlier fixture failure is reported at source.
	if m.generation2.IsZero() {
		t.Errorf("startup never reached generation 2 after %s; limit 45s", time.Since(m.accepted))
	}
	for _, err := range m.startupOpenErrors() {
		t.Error(err)
	}
}

func TestContinuationMetricsOpenBudget(t *testing.T) {
	m := &continuationMetrics{counts: map[continuationCountKey]continuationCount{
		{continuationStartup, continuationOpen, store.BotContentCheckpoint, "key"}:     {Calls: 10},
		{continuationPrepare, continuationOpen, store.BotContentCheckpoint, "key"}:     {Calls: 6},
		{continuationDocuments, continuationOpen, store.BotContentCheckpoint, "key"}:   {Calls: 100},
		{continuationPersistence, continuationOpen, store.BotContentCheckpoint, "key"}: {Calls: 100},
	}}
	if errs := m.startupOpenErrors(); len(errs) != 0 {
		t.Fatal(errs)
	}
	m.record(continuationCountKey{continuationStartup, continuationOpen, store.BotContentCheckpoint, "key"}, 0, nil)
	if errs := m.startupOpenErrors(); len(errs) != 1 {
		t.Fatalf("wanted repeated-open failure, got %v", errs)
	}
}

func continuationStartupLatencyError(accepted, generation2 time.Time) error {
	if accepted.IsZero() || generation2.IsZero() || generation2.Before(accepted) {
		return fmt.Errorf("generation 2 missing or preceded answer acceptance")
	}
	if elapsed := generation2.Sub(accepted); elapsed > 45*time.Second {
		return fmt.Errorf("startup latency regression: answer acceptance to generation 2 took %s, limit 45s (includes first physical Home setup and preprocessing)", elapsed)
	}
	return nil
}

func TestContinuationMetricsLatencyBudget(t *testing.T) {
	start := time.Now()
	for _, tc := range []struct {
		name    string
		end     time.Time
		wantErr bool
	}{
		{"missing", time.Time{}, true},
		{"before acceptance", start.Add(-time.Second), true},
		{"at budget", start.Add(45 * time.Second), false},
		{"over budget", start.Add(45*time.Second + time.Nanosecond), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := continuationStartupLatencyError(start, tc.end); (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%t", err, tc.wantErr)
			}
		})
	}
}

func TestContinuationMetricsConcurrent(t *testing.T) {
	m := &continuationMetrics{stage: continuationStartup}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 25 {
				m.begin(continuationOpen, store.BotContentCheckpoint, "same-key")("", nil)
			}
		})
	}
	wg.Wait()
	if got := m.counts[continuationCountKey{continuationStartup, continuationOpen, store.BotContentCheckpoint, "same-key"}].Calls; got != 200 {
		t.Fatalf("concurrent open count=%d want=200", got)
	}
}

func TestContinuationMetricsStages(t *testing.T) {
	m := &continuationMetrics{stage: continuationStartup}
	end := m.begin(continuationOpen, store.BotContentCheckpoint, "key-a")
	m.setStage(continuationPrepare)
	end("", nil)
	m.begin(continuationPut, store.BotContentDocument, "")("key-b", fmt.Errorf("test"))
	if got := m.counts[continuationCountKey{continuationStartup, continuationOpen, store.BotContentCheckpoint, "key-a"}]; got.Calls != 1 || got.Errors != 0 {
		t.Fatalf("open count: %+v", got)
	}
	if got := m.counts[continuationCountKey{continuationPrepare, continuationPut, store.BotContentDocument, "key-b"}]; got.Calls != 1 || got.Errors != 1 {
		t.Fatalf("put count: %+v", got)
	}
}
