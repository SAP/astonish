package daemon

import (
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"strings"
	"testing"
)

func TestBotProductionUnknownPriceReadiness(t *testing.T) {
	v := store.DefaultBotVersion()
	v.Budget.MaxModelCostMicros = nil
	if err := productionBotPriceReadiness(v); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{0, 10000} {
		v.Budget.MaxModelCostMicros = &limit
		err := productionBotPriceReadiness(v)
		if !errors.Is(err, store.ErrBotUnsupported) || !strings.Contains(err.Error(), "price is unknown") {
			t.Fatal(err)
		}
	}
}
