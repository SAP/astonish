package daemon

import (
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func TestBotProductionUnknownPriceReadiness(t *testing.T) {
	v := store.DefaultBotVersion()
	for _, limit := range []*int64{nil, new(int64(0)), new(int64(10000))} {
		v.Budget.MaxModelCostMicros = limit
		if err := productionBotPriceReadiness(v); err != nil {
			t.Fatalf("historic model cap must not block production readiness: %v", err)
		}
	}
}
