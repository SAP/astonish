package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// CapturedDrill is trusted adapter evidence, not run_drill input or policy grants.
// The first supported capture is deliberately static: implicit browser actions,
// parameterization, retries and credential materialization require separate authority.
type CapturedDrill struct {
	Format   string                       `json:"format"`
	Suite    *drill.LoadedSuite           `json:"suite"`
	Children []execution.ActionDescriptor `json:"children"`
}

const CapturedDrillFormat = "static-drill-occurrences-v1"

func (s CapturedDrill) Fingerprint() string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CaptureDrill loads every selected store child strictly (the ordinary discovery
// loader intentionally tolerates malformed children). Missing evidence fails closed.
func CaptureDrill(ctx context.Context, fs store.FlowStore, args json.RawMessage, describe func(context.Context, string, json.RawMessage) (execution.ActionDescriptor, error)) (*CapturedDrill, error) {
	if fs == nil || describe == nil {
		return nil, store.ErrBotUnsupported
	}
	var a RunDrillArgs
	if json.Unmarshal(args, &a) != nil {
		return nil, store.ErrBotInvalid
	}
	name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(a.SuiteName), ".yaml"), ".yml")
	suite, err := drill.LoadSuiteFromStore(fs, ctx, name)
	if err != nil {
		return nil, err
	}
	// Verify discovery did not silently omit a broken or unreadable child.
	count := 0
	for _, row := range fs.ListFlowsByType(ctx, []string{"drill", "test"}) {
		if row.Suite == name {
			count++
		}
	}
	if count != len(suite.Tests) {
		return nil, store.ErrBotInvalid
	}
	if err := drill.ValidateSuite(suite); err != nil {
		return nil, err
	}
	sort.Slice(suite.Tests, func(i, j int) bool { return suite.Tests[i].Name < suite.Tests[j].Name })
	spec, err := drill.SpecFromSuite(name, suite.Config.SuiteConfig)
	if err != nil || (spec != nil && spec.HasWork()) {
		return nil, store.ErrBotUnsupported
	}
	selected := suite.Tests
	if a.Tag != "" {
		tags := strings.Split(a.Tag, ",")
		for i := range tags {
			tags[i] = strings.TrimSpace(tags[i])
		}
		selected = drill.FilterTestsByTag(selected, tags)
	}
	if a.TestName != "" {
		found := drill.FilterTestByName(selected, strings.TrimSuffix(strings.TrimSuffix(a.TestName, ".yaml"), ".yml"))
		if found == nil {
			return nil, store.ErrBotInvalid
		}
		selected = []drill.LoadedTest{*found}
	}
	snapshot := &CapturedDrill{Format: CapturedDrillFormat, Suite: suite}
	for _, test := range selected {
		cfg := test.Config
		if len(cfg.Flow) != 0 || len(cfg.Parameters) != 0 {
			return nil, store.ErrBotUnsupported
		}
		if dc := cfg.DrillConfig; dc != nil && (dc.AutoWait || dc.MaxRetries != 0 || drill.IsTutorialMode(dc)) {
			return nil, store.ErrBotUnsupported
		}
		for _, node := range cfg.Nodes {
			name, ok := node.Args["tool"].(string)
			if !ok || name == "" || strings.HasPrefix(name, "browser_") || name == "run_drill" || name == "run_flow" || node.Record != "" || node.Narration != "" {
				return nil, store.ErrBotUnsupported
			}
			values := map[string]any{}
			for k, v := range node.Args {
				if k != "tool" {
					values[k] = v
				}
			}
			raw, err := json.Marshal(values)
			if err != nil {
				return nil, err
			}
			if bytes.Contains(raw, []byte("{{")) || bytes.Contains(raw, []byte("{CONTAINER_IP}")) {
				return nil, store.ErrBotUnsupported
			}
			action, err := describe(ctx, name, raw)
			if err != nil {
				return nil, err
			}
			snapshot.Children = append(snapshot.Children, action)
		}
	}
	return snapshot, nil
}

// DecodeCapturedDrill verifies the exact immutable evidence saved with an approval.
func DecodeCapturedDrill(a execution.ActionDescriptor) (*CapturedDrill, error) {
	if a.Tool != "run_drill" || a.TargetFingerprint == "" {
		return nil, store.ErrBotInvalid
	}
	var s CapturedDrill
	d := json.NewDecoder(bytes.NewReader(a.TargetSnapshot))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || s.Format != CapturedDrillFormat || s.Suite == nil || s.Suite.Config == nil || s.Fingerprint() != a.TargetFingerprint {
		return nil, store.ErrBotInvalid
	}
	return &s, nil
}
