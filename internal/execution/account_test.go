package execution

import (
	"fmt"
	"sync"
	"testing"

	"tradex/internal/config"
	"tradex/internal/domain"
	"tradex/internal/risk"
)

func TestSharedAccountAccumulatesCommonUSDBucketAndVetoesThirdTrade(t *testing.T) {
	plan, _, base := setupFixture(t)
	account, err := NewAccount(config.DefaultRisk(), 1000000)
	if err != nil {
		t.Fatal(err)
	}
	eur := accountRequest(base, "EURUSD", "EUR", 1)
	gbp := accountRequest(base, "GBPUSD", "GBP", 2)
	aud := accountRequest(base, "AUDUSD", "AUD", 3)
	_, first, err := account.PrepareSetup(plan, eur)
	if err != nil || !first.Approved {
		t.Fatalf("first decision=%+v err=%v", first, err)
	}
	_, second, err := account.PrepareSetup(plan, gbp)
	if err != nil || !second.Approved {
		t.Fatalf("second decision=%+v err=%v", second, err)
	}
	view, err := account.View(base.Now, "AUDUSD")
	if err != nil {
		t.Fatal(err)
	}
	wantUSD := first.EstimatedLossMinor + second.EstimatedLossMinor
	if view.BucketOpenRiskMinor["USD"] != wantUSD || view.DirectionalRiskMinor["USD"] != -wantUSD {
		t.Fatalf("common USD exposure not accumulated: %+v", view)
	}
	_, third, err := account.PrepareSetup(plan, aud)
	if err != nil || third.Approved || third.Code != risk.RejectExposureBucket || third.BreachedBucket != "USD" {
		t.Fatalf("third correlated trade was not vetoed: decision=%+v err=%v", third, err)
	}
	if err := account.ApplyClosingFill(domain.Fill{
		IntentID: eur.IntentID, Symbol: "EURUSD", Role: domain.FillTakeProfit, FilledAt: base.Now,
	}); err != nil {
		t.Fatal(err)
	}
	_, afterRelease, err := account.PrepareSetup(plan, aud)
	if err != nil || !afterRelease.Approved {
		t.Fatalf("released bucket risk did not restore capacity: decision=%+v err=%v", afterRelease, err)
	}
}

func TestSharedAccountSerializesConcurrentCorrelatedApprovals(t *testing.T) {
	plan, _, base := setupFixture(t)
	account, err := NewAccount(config.DefaultRisk(), 1000000)
	if err != nil {
		t.Fatal(err)
	}
	currencies := []string{"EUR", "GBP", "AUD"}
	decisions := make(chan risk.Decision, len(currencies))
	errors := make(chan error, len(currencies))
	var wait sync.WaitGroup
	for index, currency := range currencies {
		wait.Add(1)
		go func(index int, currency string) {
			defer wait.Done()
			request := accountRequest(base, currency+"USD", currency, index+1)
			_, decision, err := account.PrepareSetup(plan, request)
			decisions <- decision
			errors <- err
		}(index, currency)
	}
	wait.Wait()
	close(decisions)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	approved, bucketVetoes := 0, 0
	for decision := range decisions {
		if decision.Approved {
			approved++
		} else if decision.Code == risk.RejectExposureBucket {
			bucketVetoes++
		}
	}
	if approved != 2 || bucketVetoes != 1 {
		t.Fatalf("concurrent decisions approved=%d bucket_vetoes=%d", approved, bucketVetoes)
	}
}

func accountRequest(base SetupRequest, symbol, baseCurrency string, sequence int) SetupRequest {
	request := base
	request.IntentID = fmt.Sprintf("setup-%d", sequence)
	request.ApprovalID = fmt.Sprintf("risk-setup-%d", sequence)
	request.Symbol = symbol
	request.Setup.DetectedAt = base.Setup.DetectedAt
	request.Market.Instrument.Symbol = symbol
	request.Market.Instrument.BaseCurrency = baseCurrency
	request.Market.Instrument.QuoteCurrency = "USD"
	return request
}
