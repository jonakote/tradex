package marketdata

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

func candleAt(at time.Time, price domain.Price) domain.Candle {
	return domain.Candle{
		Source: "test", Symbol: "EURUSD", Timeframe: time.Minute,
		OpenTime: at, CloseTime: at.Add(time.Minute), Open: price,
		High: price, Low: price, Close: price, TickCount: 1,
	}
}

func TestValidateCandlesReportsMissingPeriods(t *testing.T) {
	t0 := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	report, err := ValidateCandles([]domain.Candle{candleAt(t0, 100), candleAt(t0.Add(3*time.Minute), 101)})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Gaps) != 1 || report.Gaps[0].MissingPeriods != 2 {
		t.Fatalf("unexpected gaps: %+v", report.Gaps)
	}
}

func TestValidateCandlesRejectsDuplicate(t *testing.T) {
	t0 := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	if _, err := ValidateCandles([]domain.Candle{candleAt(t0, 100), candleAt(t0, 100)}); err == nil {
		t.Fatal("duplicate candle accepted")
	}
}
