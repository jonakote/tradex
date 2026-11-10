package reporting

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tradex/internal/domain"
	"tradex/internal/research/replay"
)

func TestSingleReportIsDeterministicHashedAndExportedNewOnly(t *testing.T) {
	scenario, err := replay.Load(filepath.Join("..", "research", "replay", "testdata", "eurusd_continuation_win.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := replay.Run(context.Background(), scenario, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := FromSingle(result)
	if err != nil {
		t.Fatal(err)
	}
	second, err := FromSingle(result)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, second) {
		t.Fatal("same replay result produced different research reports")
	}
	if report.Account.Trades != 1 || report.Account.NetProfitMinor != 9102 || report.Account.ReturnBPS != 91 ||
		report.Decisions.Total != 1 || report.Decisions.Approved != 1 || report.Execution.ClosingFills != 1 ||
		report.Opportunities.Total != 1 || report.Opportunities.Closed != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if err := report.Verify(); err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	jsonPath := filepath.Join(directory, "report.json")
	markdownPath := filepath.Join(directory, "report.md")
	if err := WriteBundleNew(jsonPath, markdownPath, report); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Verify(); err != nil {
		t.Fatal(err)
	}
	markdown, err := os.ReadFile(markdownPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Account performance", "95% Wilson interval", "Opportunity outcomes", "Replay results are not evidence"} {
		if !strings.Contains(string(markdown), text) {
			t.Fatalf("Markdown report omitted %q", text)
		}
	}
	if err := WriteBundleNew(jsonPath, filepath.Join(directory, "other.md"), report); err == nil {
		t.Fatal("report export overwrote an existing artifact")
	}
	tampered := report
	tampered.Account.NetProfitMinor++
	if err := tampered.Verify(); err == nil {
		t.Fatal("tampered report passed hash verification")
	}
	tamperedResult := result
	tamperedResult.Report.NetProfitMinor++
	if _, err := FromSingle(tamperedResult); err == nil {
		t.Fatal("report builder trusted analytics inconsistent with source fills")
	}
}

func TestSharedReportIncludesPerSymbolDecisionBreakdown(t *testing.T) {
	base, err := replay.Load(filepath.Join("..", "research", "replay", "testdata", "eurusd_continuation_win.json"))
	if err != nil {
		t.Fatal(err)
	}
	eurusd := reportingLeg(base, "EURUSD", "EUR")
	gbpusd := reportingLeg(base, "GBPUSD", "GBP")
	shared := replay.SharedScenario{
		Name: "shared-report", InitialEquityMinor: base.InitialEquityMinor, Risk: base.Risk,
		Legs: []replay.Scenario{gbpusd, eurusd},
	}
	result, err := replay.RunShared(context.Background(), shared, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := FromShared(result)
	if err != nil {
		t.Fatal(err)
	}
	if report.Mode != "shared" || len(report.Symbols) != 2 || report.Symbols["EURUSD"].Trades != 1 ||
		report.Symbols["GBPUSD"].Trades != 1 || report.Decisions.ApprovedBySymbol["EURUSD"] != 1 ||
		report.Decisions.ApprovedBySymbol["GBPUSD"] != 1 {
		t.Fatalf("unexpected shared report: %+v", report)
	}
	markdown := RenderMarkdown(report)
	if strings.Index(markdown, "| EURUSD |") > strings.Index(markdown, "| GBPUSD |") {
		t.Fatal("per-symbol Markdown rows are not lexically ordered")
	}
}

func TestExecutionSummaryExposesRejectedAndPartialOutcomes(t *testing.T) {
	orders := []domain.BrokerOrder{
		{BrokerOrderID: "rejected", Quantity: 1000, Remaining: 1000, Status: domain.OrderRejected},
		{BrokerOrderID: "partial", Quantity: 10000, Remaining: 6000, Status: domain.OrderCancelled},
		{BrokerOrderID: "open-partial", Quantity: 5000, Remaining: 2000, Status: domain.OrderPartiallyFilled},
	}
	fills := []domain.Fill{
		{BrokerOrderID: "partial", Role: domain.FillEntry, Quantity: 4000},
		{BrokerOrderID: "partial", Role: domain.FillTakeProfit, Quantity: 4000},
		{BrokerOrderID: "open-partial", Role: domain.FillEntry, Quantity: 3000},
	}
	summary := executionSummary(orders, fills)
	if summary.Orders != 3 || summary.RejectedOrders != 1 || summary.CancelledOrders != 1 ||
		summary.OpenPartiallyFilledOrders != 1 || summary.PartiallyExecutedOrders != 2 ||
		summary.EntryFills != 2 || summary.ClosingFills != 1 {
		t.Fatalf("unexpected execution summary: %+v", summary)
	}
}

func reportingLeg(base replay.Scenario, symbol, baseCurrency string) replay.Scenario {
	leg := base
	leg.Name = symbol + "-report"
	leg.Instrument.Symbol = symbol
	leg.Instrument.BaseCurrency = baseCurrency
	leg.Events = append([]replay.Event(nil), base.Events...)
	for index := range leg.Events {
		if leg.Events[index].Candle != nil {
			candle := *leg.Events[index].Candle
			candle.Symbol = symbol
			leg.Events[index].Candle = &candle
		}
		if leg.Events[index].Quote != nil {
			quote := *leg.Events[index].Quote
			quote.Symbol = symbol
			leg.Events[index].Quote = &quote
		}
	}
	return leg
}
