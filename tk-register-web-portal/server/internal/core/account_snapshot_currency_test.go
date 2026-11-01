package core

import (
	"math"
	"testing"

	"goapi-tk-register/server/common"
)

func TestSnapshotUSDConversionPreservesOriginalAndMissingValues(t *testing.T) {
	cost, zero, nan, inf := 123.45, 0.0, math.NaN(), math.Inf(1)
	cny, normalized, usd, blank := "CNY", " cny ", "USD", ""
	wantUSD := cost / 7.2
	for _, test := range []struct {
		name     string
		cost     *float64
		currency *string
		want     *float64
	}{
		{"cny precision", &cost, &cny, &wantUSD},
		{"normalized cny", &cost, &normalized, &wantUSD},
		{"real zero", &zero, &cny, &zero},
		{"absent amount", nil, &cny, nil},
		{"absent currency", &cost, nil, nil},
		{"empty currency", &cost, &blank, nil},
		{"unsupported currency", &cost, &usd, nil},
		{"nonfinite amount", &nan, &cny, nil},
		{"infinite amount", &inf, &cny, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := AccountSnapshot{AccountSnapshotValues: AccountSnapshotValues{CostTotal: test.cost, Currency: test.currency}}
			out := snapshotDTO(row)
			if out.CostTotal != test.cost || out.Currency != test.currency {
				t.Fatal("conversion changed the original observation")
			}
			if out.ExchangeRateCNYPerUSD != 7.2 {
				t.Fatalf("unexpected rate: %v", out.ExchangeRateCNYPerUSD)
			}
			if test.want == nil {
				if out.CostTotalUSD != nil {
					t.Fatalf("unavailable USD amount became %v", *out.CostTotalUSD)
				}
			} else if out.CostTotalUSD == nil || *out.CostTotalUSD != *test.want {
				t.Fatalf("wrong conversion or prematurely rounded quotient: %v, want %.15f", out.CostTotalUSD, *test.want)
			}
			// Invalid stored numbers are rejected before serialization; all valid
			// observations must expose explicit numeric or null additive fields.
			if test.cost != nil && (math.IsNaN(*test.cost) || math.IsInf(*test.cost, 0)) {
				return
			}
			raw, err := common.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := common.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			value, exists := payload["cost_total_usd"]
			if !exists || payload["exchange_rate_cny_per_usd"] != 7.2 || (test.want == nil && value != nil) || (test.want != nil && value != *test.want) {
				t.Fatalf("wrong USD JSON contract: %s", raw)
			}
		})
	}
}
