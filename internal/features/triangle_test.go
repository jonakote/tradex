package features

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

func triangleSwing(kind SwingKind, index int, price domain.Price) Swing {
	pivot := time.Date(2026, 8, 20, 8+index, 0, 0, 0, time.UTC)
	return Swing{
		Kind: kind, Symbol: "EURUSD", Timeframe: time.Hour, Price: price,
		PivotTime: pivot, ConfirmedAt: pivot.Add(time.Hour), PivotIndex: index,
	}
}

func TestDetectTriangleClassifiesCommonForms(t *testing.T) {
	tests := []struct {
		name string
		high []domain.Price
		low  []domain.Price
		want TriangleKind
	}{
		{"ascending", []domain.Price{1100, 1101, 1100}, []domain.Price{800, 900, 1000}, TriangleAscending},
		{"descending", []domain.Price{1200, 1100, 1000}, []domain.Price{800, 799, 800}, TriangleDescending},
		{"symmetrical", []domain.Price{1300, 1150, 1000}, []domain.Price{700, 775, 850}, TriangleSymmetric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			swings := make([]Swing, 0, 6)
			for i := range tt.high {
				swings = append(swings, triangleSwing(SwingHigh, i*2, tt.high[i]))
				swings = append(swings, triangleSwing(SwingLow, i*2+1, tt.low[i]))
			}
			pattern, ok := DetectTriangle(swings, DefaultTriangleConfig())
			if !ok || pattern.Kind != tt.want {
				t.Fatalf("got pattern=%+v ok=%v, want %s", pattern, ok, tt.want)
			}
		})
	}
}

func TestTriangleRequiresConvergenceAndConfirmedSwings(t *testing.T) {
	swings := []Swing{
		triangleSwing(SwingHigh, 0, 1100), triangleSwing(SwingLow, 1, 800),
		triangleSwing(SwingHigh, 2, 1100), triangleSwing(SwingLow, 3, 900),
		triangleSwing(SwingHigh, 4, 1100), triangleSwing(SwingLow, 5, 1000),
	}
	swings[5].ConfirmedAt = time.Time{}
	if _, ok := DetectTriangle(swings, DefaultTriangleConfig()); ok {
		t.Fatal("unconfirmed swing created a triangle")
	}
	swings[5].ConfirmedAt = swings[5].PivotTime.Add(time.Hour)
	swings[4].Price = 900
	if _, ok := DetectTriangle(swings, DefaultTriangleConfig()); ok {
		t.Fatal("non-converging boundaries created a triangle")
	}
}

func TestTriangleBreakoutRequiresCloseBeyondProjectedBoundary(t *testing.T) {
	swings := []Swing{
		triangleSwing(SwingHigh, 0, 1100), triangleSwing(SwingLow, 1, 800),
		triangleSwing(SwingHigh, 2, 1100), triangleSwing(SwingLow, 3, 900),
		triangleSwing(SwingHigh, 4, 1100), triangleSwing(SwingLow, 5, 1000),
	}
	pattern, ok := DetectTriangle(swings, DefaultTriangleConfig())
	if !ok {
		t.Fatal("expected ascending triangle")
	}
	if direction, broke := pattern.Breakout(1099, 6, 0); broke || direction != "" {
		t.Fatalf("boundary touch/wick should not break: %s %v", direction, broke)
	}
	if direction, broke := pattern.Breakout(1101, 6, 0); !broke || direction != BreakBullish {
		t.Fatalf("expected bullish close breakout: %s %v", direction, broke)
	}
}
