package reporting

import (
	"errors"
	"fmt"
	"math/big"
	"sort"

	"tradex/internal/analytics"
	"tradex/internal/domain"
	"tradex/internal/experiment"
	"tradex/internal/portfolio"
	"tradex/internal/research/replay"
	"tradex/internal/risk"
)

const SchemaVersion = "tradex.research-report/v1"

type Performance struct {
	Trades               int     `json:"trades"`
	Wins                 int     `json:"wins"`
	Losses               int     `json:"losses"`
	Breakeven            int     `json:"breakeven"`
	WinRate              float64 `json:"win_rate"`
	WinRateLower95       float64 `json:"win_rate_lower_95"`
	WinRateUpper95       float64 `json:"win_rate_upper_95"`
	NetProfitMinor       int64   `json:"net_profit_minor"`
	ReturnBPS            int64   `json:"return_bps"`
	WinningPnLMinor      int64   `json:"winning_pnl_minor"`
	LosingPnLMinor       int64   `json:"losing_pnl_minor"`
	TotalCommissionMinor int64   `json:"total_commission_minor"`
	TotalRolloverMinor   int64   `json:"total_rollover_minor"`
	ExpectancyMinor      float64 `json:"expectancy_minor"`
	ProfitFactor         float64 `json:"profit_factor"`
	ProfitFactorInfinite bool    `json:"profit_factor_infinite"`
	MaxDrawdownMinor     int64   `json:"max_drawdown_minor"`
	MaxConsecutiveLosses int     `json:"max_consecutive_losses"`
}

type DecisionSummary struct {
	Total            int            `json:"total"`
	Approved         int            `json:"approved"`
	Rejected         int            `json:"rejected"`
	RejectedByCode   map[string]int `json:"rejected_by_code"`
	ApprovedBySymbol map[string]int `json:"approved_by_symbol,omitempty"`
	RejectedBySymbol map[string]int `json:"rejected_by_symbol,omitempty"`
}

type ExecutionSummary struct {
	Orders                    int `json:"orders"`
	RejectedOrders            int `json:"rejected_orders"`
	CancelledOrders           int `json:"cancelled_orders"`
	OpenPartiallyFilledOrders int `json:"open_partially_filled_orders"`
	PartiallyExecutedOrders   int `json:"partially_executed_orders"`
	Fills                     int `json:"fills"`
	EntryFills                int `json:"entry_fills"`
	ClosingFills              int `json:"closing_fills"`
}

type OpportunitySummary struct {
	Total          int `json:"total"`
	Unconfirmed    int `json:"unconfirmed"`
	Invalidated    int `json:"invalidated"`
	RiskRejected   int `json:"risk_rejected"`
	BrokerRejected int `json:"broker_rejected"`
	NoFill         int `json:"no_fill"`
	Pending        int `json:"pending"`
	Open           int `json:"open"`
	Closed         int `json:"closed"`
}

type PortfolioSnapshot struct {
	InitialEquityMinor     int64            `json:"initial_equity_minor"`
	FinalEquityMinor       int64            `json:"final_equity_minor"`
	AggregateOpenRiskMinor int64            `json:"aggregate_open_risk_minor"`
	DailyPnLMinor          int64            `json:"daily_pnl_minor"`
	WeeklyPnLMinor         int64            `json:"weekly_pnl_minor"`
	DrawdownBPS            int64            `json:"drawdown_bps"`
	ConsecutiveLosses      int              `json:"consecutive_losses"`
	OpenPositions          int              `json:"open_positions"`
	BucketOpenRiskMinor    map[string]int64 `json:"bucket_open_risk_minor"`
	DirectionalRiskMinor   map[string]int64 `json:"directional_risk_minor"`
}

type Report struct {
	SchemaVersion      string                 `json:"schema_version"`
	Mode               string                 `json:"mode"`
	ExperimentID       string                 `json:"experiment_id"`
	ManifestSHA256     string                 `json:"manifest_sha256"`
	SourceResultSHA256 string                 `json:"source_result_sha256"`
	Account            Performance            `json:"account"`
	Symbols            map[string]Performance `json:"symbols,omitempty"`
	Decisions          DecisionSummary        `json:"decisions"`
	Opportunities      OpportunitySummary     `json:"opportunities"`
	Execution          ExecutionSummary       `json:"execution"`
	Portfolio          PortfolioSnapshot      `json:"portfolio"`
	Caveats            []string               `json:"caveats"`
	ReportSHA256       string                 `json:"report_sha256"`
}

func FromSingle(result replay.Result) (Report, error) {
	if err := result.Manifest.Verify(); err != nil {
		return Report{}, fmt.Errorf("verify replay manifest: %w", err)
	}
	if result.Scenario != result.Manifest.ExperimentID {
		return Report{}, errors.New("replay scenario and manifest experiment differ")
	}
	initial, err := initialEquity(result.Portfolio.EquityMinor, result.Report.NetProfitMinor)
	if err != nil {
		return Report{}, err
	}
	rebuilt, err := analytics.BuildReport(result.Fills, initial)
	if err != nil || rebuilt != result.Report {
		return Report{}, errors.New("replay analytics do not match its fills and portfolio")
	}
	sourceHash, err := experiment.Digest(result)
	if err != nil {
		return Report{}, err
	}
	decisions := make([]symbolDecision, len(result.Decisions))
	symbol := resultSymbol(result.Orders, result.Fills)
	for _, order := range result.Orders {
		if symbol != "" && order.Symbol != symbol {
			return Report{}, errors.New("single replay result contains multiple order symbols")
		}
	}
	for _, fill := range result.Fills {
		if symbol != "" && fill.Symbol != symbol {
			return Report{}, errors.New("single replay result contains multiple fill symbols")
		}
	}
	for index, decision := range result.Decisions {
		decisions[index] = symbolDecision{symbol: symbol, decision: decision}
	}
	return build("single", result.Scenario, result.Manifest.ManifestSHA256, sourceHash, result.Report,
		nil, decisions, result.Opportunities, result.Orders, result.Fills, result.Portfolio)
}

func FromShared(result replay.SharedResult) (Report, error) {
	if err := result.Manifest.Verify(); err != nil {
		return Report{}, fmt.Errorf("verify shared replay manifest: %w", err)
	}
	if result.Scenario != result.Manifest.ExperimentID {
		return Report{}, errors.New("shared replay scenario and manifest experiment differ")
	}
	if len(result.LegReports) < 2 {
		return Report{}, errors.New("shared replay must contain at least two symbol reports")
	}
	initial, err := initialEquity(result.Portfolio.EquityMinor, result.Report.NetProfitMinor)
	if err != nil {
		return Report{}, err
	}
	rebuilt, err := analytics.BuildReport(result.Fills, initial)
	if err != nil || rebuilt != result.Report {
		return Report{}, errors.New("shared replay analytics do not match its fills and portfolio")
	}
	for symbol, reported := range result.LegReports {
		fills := make([]domain.Fill, 0)
		for _, fill := range result.Fills {
			if fill.Symbol == symbol {
				fills = append(fills, fill)
			}
		}
		rebuilt, err := analytics.BuildReport(fills, initial)
		if err != nil || rebuilt != reported {
			return Report{}, fmt.Errorf("shared replay analytics for %s do not match its fills", symbol)
		}
	}
	for _, item := range result.Decisions {
		if _, exists := result.LegReports[item.Symbol]; !exists {
			return Report{}, fmt.Errorf("shared decision references unreported symbol %s", item.Symbol)
		}
	}
	for _, fill := range result.Fills {
		if _, exists := result.LegReports[fill.Symbol]; !exists {
			return Report{}, fmt.Errorf("shared fill references unreported symbol %s", fill.Symbol)
		}
	}
	for _, order := range result.Orders {
		if _, exists := result.LegReports[order.Symbol]; !exists {
			return Report{}, fmt.Errorf("shared order references unreported symbol %s", order.Symbol)
		}
	}
	sourceHash, err := experiment.Digest(result)
	if err != nil {
		return Report{}, err
	}
	decisions := make([]symbolDecision, len(result.Decisions))
	for index, item := range result.Decisions {
		decisions[index] = symbolDecision{symbol: item.Symbol, decision: item.Decision}
	}
	return build("shared", result.Scenario, result.Manifest.ManifestSHA256, sourceHash, result.Report,
		result.LegReports, decisions, result.Opportunities, result.Orders, result.Fills, result.Portfolio)
}

type symbolDecision struct {
	symbol   string
	decision risk.Decision
}

func build(mode, experimentID, manifestHash, sourceHash string, account analytics.Report,
	symbolReports map[string]analytics.Report, decisions []symbolDecision, opportunities []replay.Opportunity, orders []domain.BrokerOrder,
	fills []domain.Fill, view portfolio.View) (Report, error) {
	initial, err := initialEquity(view.EquityMinor, account.NetProfitMinor)
	if err != nil {
		return Report{}, err
	}
	decisionMetrics, err := decisionSummary(decisions)
	if err != nil {
		return Report{}, err
	}
	if decisionMetrics.Approved != len(orders) {
		return Report{}, errors.New("approved decision and order counts differ")
	}
	report := Report{
		SchemaVersion: SchemaVersion, Mode: mode, ExperimentID: experimentID,
		ManifestSHA256: manifestHash, SourceResultSHA256: sourceHash,
		Account: performance(account, initial), Symbols: make(map[string]Performance),
		Decisions: decisionMetrics, Opportunities: opportunitySummary(opportunities), Execution: executionSummary(orders, fills),
		Portfolio: PortfolioSnapshot{
			InitialEquityMinor: initial, FinalEquityMinor: view.EquityMinor,
			AggregateOpenRiskMinor: view.AggregateOpenRiskMinor, DailyPnLMinor: view.DailyPnLMinor,
			WeeklyPnLMinor: view.WeeklyPnLMinor, DrawdownBPS: view.DrawdownBPS,
			ConsecutiveLosses: view.ConsecutiveLosses, OpenPositions: view.OpenPositions,
			BucketOpenRiskMinor:  copyMap(view.BucketOpenRiskMinor),
			DirectionalRiskMinor: copyMap(view.DirectionalRiskMinor),
		},
		Caveats: []string{
			"Replay results are not evidence of future performance.",
			"No win-rate or profitability target is validated without untouched holdout evidence.",
			"Synthetic spread or fill assumptions can materially overstate executable performance.",
		},
	}
	for symbol, value := range symbolReports {
		report.Symbols[symbol] = performance(value, initial)
	}
	if len(report.Symbols) == 0 {
		report.Symbols = nil
	}
	report.ReportSHA256, err = report.digest()
	if err != nil {
		return Report{}, err
	}
	return report, report.Verify()
}

func (r Report) Verify() error {
	if r.SchemaVersion != SchemaVersion || (r.Mode != "single" && r.Mode != "shared") ||
		r.ExperimentID == "" || !validHash(r.ManifestSHA256) || !validHash(r.SourceResultSHA256) ||
		!validHash(r.ReportSHA256) {
		return errors.New("invalid research report identity")
	}
	if r.Portfolio.InitialEquityMinor <= 0 || r.Portfolio.FinalEquityMinor <= 0 ||
		r.Decisions.Total != r.Decisions.Approved+r.Decisions.Rejected ||
		r.Opportunities.Total != r.Opportunities.Unconfirmed+r.Opportunities.Invalidated+r.Opportunities.RiskRejected+r.Opportunities.BrokerRejected+r.Opportunities.NoFill+r.Opportunities.Pending+r.Opportunities.Open+r.Opportunities.Closed ||
		r.Execution.EntryFills+r.Execution.ClosingFills != r.Execution.Fills ||
		r.Execution.RejectedOrders+r.Execution.CancelledOrders+r.Execution.OpenPartiallyFilledOrders > r.Execution.Orders ||
		r.Execution.PartiallyExecutedOrders > r.Execution.Orders {
		return errors.New("invalid research report metrics")
	}
	want, err := r.digest()
	if err != nil {
		return err
	}
	if want != r.ReportSHA256 {
		return errors.New("research report hash mismatch")
	}
	return nil
}

func opportunitySummary(opportunities []replay.Opportunity) OpportunitySummary {
	result := OpportunitySummary{Total: len(opportunities)}
	for _, opportunity := range opportunities {
		switch opportunity.Status {
		case replay.OpportunityUnconfirmed:
			result.Unconfirmed++
		case replay.OpportunityExpired:
			result.Invalidated++
		case replay.OpportunityRiskRejected:
			result.RiskRejected++
		case replay.OpportunityBrokerRejected:
			result.BrokerRejected++
		case replay.OpportunityNoFill:
			result.NoFill++
		case replay.OpportunityPending:
			result.Pending++
		case replay.OpportunityOpen:
			result.Open++
		case replay.OpportunityClosed:
			result.Closed++
		}
	}
	return result
}

func (r Report) digest() (string, error) {
	copy := r
	copy.ReportSHA256 = ""
	return experiment.Digest(copy)
}

func performance(source analytics.Report, initial int64) Performance {
	return Performance{
		Trades: source.Trades, Wins: source.Wins, Losses: source.Losses, Breakeven: source.Breakeven,
		WinRate: source.WinRate, WinRateLower95: source.WinRateLower95, WinRateUpper95: source.WinRateUpper95,
		NetProfitMinor: source.NetProfitMinor, ReturnBPS: ratioBPS(source.NetProfitMinor, initial),
		WinningPnLMinor: source.WinningPnLMinor, LosingPnLMinor: source.LosingPnLMinor,
		TotalCommissionMinor: source.TotalCommissionMinor, TotalRolloverMinor: source.TotalRolloverMinor,
		ExpectancyMinor: source.ExpectancyMinor,
		ProfitFactor:    source.ProfitFactor, ProfitFactorInfinite: source.ProfitFactorInfinite,
		MaxDrawdownMinor: source.MaxDrawdownMinor, MaxConsecutiveLosses: source.MaxConsecutiveLosses,
	}
}

func decisionSummary(decisions []symbolDecision) (DecisionSummary, error) {
	result := DecisionSummary{
		Total: len(decisions), RejectedByCode: make(map[string]int),
		ApprovedBySymbol: make(map[string]int), RejectedBySymbol: make(map[string]int),
	}
	for _, item := range decisions {
		if item.decision.Approved {
			if item.decision.Code != "" {
				return DecisionSummary{}, errors.New("approved decision carries a rejection code")
			}
			result.Approved++
			if item.symbol != "" {
				result.ApprovedBySymbol[item.symbol]++
			}
			continue
		}
		if item.decision.Code == "" {
			return DecisionSummary{}, errors.New("rejected decision has no rejection code")
		}
		result.Rejected++
		result.RejectedByCode[string(item.decision.Code)]++
		if item.symbol != "" {
			result.RejectedBySymbol[item.symbol]++
		}
	}
	if len(result.ApprovedBySymbol) == 0 {
		result.ApprovedBySymbol = nil
	}
	if len(result.RejectedBySymbol) == 0 {
		result.RejectedBySymbol = nil
	}
	return result, nil
}

func executionSummary(orders []domain.BrokerOrder, fills []domain.Fill) ExecutionSummary {
	result := ExecutionSummary{Orders: len(orders), Fills: len(fills)}
	entryQuantityByOrder := make(map[string]int64)
	for _, fill := range fills {
		if fill.Role == domain.FillEntry {
			result.EntryFills++
			entryQuantityByOrder[fill.BrokerOrderID] += fill.Quantity
		} else {
			result.ClosingFills++
		}
	}
	for _, order := range orders {
		switch order.Status {
		case domain.OrderRejected:
			result.RejectedOrders++
		case domain.OrderCancelled:
			result.CancelledOrders++
		case domain.OrderPartiallyFilled:
			result.OpenPartiallyFilledOrders++
		}
		filled := entryQuantityByOrder[order.BrokerOrderID]
		if filled > 0 && filled < order.Quantity {
			result.PartiallyExecutedOrders++
		}
	}
	return result
}

func resultSymbol(orders []domain.BrokerOrder, fills []domain.Fill) string {
	if len(orders) > 0 {
		return orders[0].Symbol
	}
	if len(fills) > 0 {
		return fills[0].Symbol
	}
	return ""
}

func initialEquity(final, net int64) (int64, error) {
	value := new(big.Int).Sub(big.NewInt(final), big.NewInt(net))
	if !value.IsInt64() || value.Sign() <= 0 {
		return 0, errors.New("cannot derive positive initial equity from replay result")
	}
	return value.Int64(), nil
}

func ratioBPS(value, base int64) int64 {
	if base <= 0 {
		return 0
	}
	result := new(big.Int).Mul(big.NewInt(value), big.NewInt(10000))
	result.Quo(result, big.NewInt(base))
	if !result.IsInt64() {
		if result.Sign() < 0 {
			return -1 << 63
		}
		return 1<<63 - 1
	}
	return result.Int64()
}

func copyMap(source map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
