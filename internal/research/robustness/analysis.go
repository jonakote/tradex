package robustness

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"sort"

	"tradex/internal/domain"
	"tradex/internal/experiment"
	"tradex/internal/reporting"
	"tradex/internal/research/replay"
)

const SchemaVersion = "tradex.robustness/v1"

type Config struct {
	Iterations             int       `json:"iterations"`
	MeanBlockLength        int       `json:"mean_block_length"`
	Seed                   uint64    `json:"seed"`
	RuinDrawdownBPS        int64     `json:"ruin_drawdown_bps"`
	RiskFreeReturnPerTrade float64   `json:"risk_free_return_per_trade"`
	IndependentTrials      float64   `json:"independent_trials"`
	TrialSharpeRatios      []float64 `json:"trial_sharpe_ratios"`
}

type PathStats struct {
	EndingPnLMinor       int64 `json:"ending_pnl_minor"`
	MaxDrawdownMinor     int64 `json:"max_drawdown_minor"`
	MaxDrawdownBPS       int64 `json:"max_drawdown_bps"`
	MaxConsecutiveLosses int   `json:"max_consecutive_losses"`
	RuinThresholdHit     bool  `json:"ruin_threshold_hit"`
}

type Int64Quantiles struct {
	P05 int64 `json:"p05"`
	P50 int64 `json:"p50"`
	P95 int64 `json:"p95"`
	P99 int64 `json:"p99"`
}

type IntQuantiles struct {
	P50 int `json:"p50"`
	P95 int `json:"p95"`
	P99 int `json:"p99"`
}

type BootstrapSummary struct {
	Method                     string         `json:"method"`
	RestartProbability         float64        `json:"restart_probability"`
	Iterations                 int            `json:"iterations"`
	MeanBlockLength            int            `json:"mean_block_length"`
	Seed                       uint64         `json:"seed"`
	EndingPnLMinor             Int64Quantiles `json:"ending_pnl_minor"`
	MaxDrawdownMinor           Int64Quantiles `json:"max_drawdown_minor"`
	MaxDrawdownBPS             Int64Quantiles `json:"max_drawdown_bps"`
	MaxConsecutiveLosses       IntQuantiles   `json:"max_consecutive_losses"`
	MeanEndingPnLMinor         float64        `json:"mean_ending_pnl_minor"`
	ProbabilityOfLoss          float64        `json:"probability_of_loss"`
	ProbabilityOfRuinThreshold float64        `json:"probability_of_ruin_threshold"`
}

type DeflatedSharpe struct {
	ReturnUnit                string  `json:"return_unit"`
	SharpeEstimator           string  `json:"sharpe_estimator"`
	MomentEstimator           string  `json:"moment_estimator"`
	SelectionThreshold        string  `json:"selection_threshold"`
	SampleCount               int     `json:"sample_count"`
	ObservedSharpe            float64 `json:"observed_sharpe"`
	Skewness                  float64 `json:"skewness"`
	Kurtosis                  float64 `json:"kurtosis"`
	TotalTrials               int     `json:"total_trials"`
	IndependentTrials         float64 `json:"independent_trials"`
	TrialSharpeVariance       float64 `json:"trial_sharpe_variance"`
	ExpectedMaximumSharpe     float64 `json:"expected_maximum_sharpe"`
	DeflatedSharpeProbability float64 `json:"deflated_sharpe_probability"`
}

type Result struct {
	SchemaVersion      string           `json:"schema_version"`
	Mode               string           `json:"mode"`
	ExperimentID       string           `json:"experiment_id"`
	ManifestSHA256     string           `json:"manifest_sha256"`
	SourceResultSHA256 string           `json:"source_result_sha256"`
	TradePnLSHA256     string           `json:"trade_pnl_sha256"`
	TradeCount         int              `json:"trade_count"`
	InitialEquityMinor int64            `json:"initial_equity_minor"`
	Config             Config           `json:"config"`
	Observed           PathStats        `json:"observed"`
	Bootstrap          BootstrapSummary `json:"stationary_bootstrap"`
	DeflatedSharpe     DeflatedSharpe   `json:"deflated_sharpe"`
	Caveats            []string         `json:"caveats"`
	ResultSHA256       string           `json:"result_sha256"`
}

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	var config Config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode robustness config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Config{}, errors.New("robustness config contains trailing JSON")
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) Validate() error {
	if c.Iterations < 1000 || c.MeanBlockLength <= 0 || c.RuinDrawdownBPS <= 0 ||
		c.RuinDrawdownBPS > 10000 || len(c.TrialSharpeRatios) < 2 ||
		c.IndependentTrials < 1 || c.IndependentTrials > float64(len(c.TrialSharpeRatios)) ||
		!finite(c.RiskFreeReturnPerTrade) || math.Abs(c.RiskFreeReturnPerTrade) >= 1 {
		return errors.New("invalid robustness simulation or DSR configuration")
	}
	for _, sharpe := range c.TrialSharpeRatios {
		if !finite(sharpe) {
			return errors.New("trial Sharpe ratios must be finite")
		}
	}
	return nil
}

func FromSingle(result replay.Result, config Config) (Result, error) {
	report, err := reporting.FromSingle(result)
	if err != nil {
		return Result{}, err
	}
	return analyze("single", result.Scenario, result.Manifest.ManifestSHA256, report.SourceResultSHA256,
		report.Portfolio.InitialEquityMinor, closingPnLs(result.Fills), config)
}

func FromShared(result replay.SharedResult, config Config) (Result, error) {
	report, err := reporting.FromShared(result)
	if err != nil {
		return Result{}, err
	}
	return analyze("shared", result.Scenario, result.Manifest.ManifestSHA256, report.SourceResultSHA256,
		report.Portfolio.InitialEquityMinor, closingPnLs(result.Fills), config)
}

func analyze(mode, experimentID, manifestHash, sourceHash string, initialEquity int64,
	pnls []int64, config Config) (Result, error) {
	if err := config.Validate(); err != nil {
		return Result{}, err
	}
	if len(pnls) < 3 {
		return Result{}, errors.New("robustness analysis requires at least three completed trades")
	}
	pnlHash, err := experiment.Digest(pnls)
	if err != nil {
		return Result{}, err
	}
	observed, err := pathStats(pnls, initialEquity, config.RuinDrawdownBPS)
	if err != nil {
		return Result{}, err
	}
	bootstrap, err := stationaryBootstrap(pnls, initialEquity, config)
	if err != nil {
		return Result{}, err
	}
	returns, err := equityReturns(pnls, initialEquity, config.RiskFreeReturnPerTrade)
	if err != nil {
		return Result{}, err
	}
	dsr, err := calculateDeflatedSharpe(returns, config.TrialSharpeRatios, config.IndependentTrials)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		SchemaVersion: SchemaVersion, Mode: mode, ExperimentID: experimentID,
		ManifestSHA256: manifestHash, SourceResultSHA256: sourceHash, TradePnLSHA256: pnlHash,
		TradeCount: len(pnls), InitialEquityMinor: initialEquity, Config: config,
		Observed: observed, Bootstrap: bootstrap, DeflatedSharpe: dsr,
		Caveats: []string{
			"Bootstrap paths resample observed trades; they cannot represent risks absent from the source data.",
			"Per-trade Sharpe is not annualized and irregular trade timing is not modeled.",
			"Independent trial count and the complete trial-Sharpe set must be governed externally and must not omit failed experiments.",
			"Deflated Sharpe and Monte Carlo estimates do not prove future profitability.",
		},
	}
	result.ResultSHA256, err = result.digest()
	if err != nil {
		return Result{}, err
	}
	return result, result.Verify()
}

func closingPnLs(fills []domain.Fill) []int64 {
	result := make([]int64, 0)
	for _, fill := range fills {
		if fill.Role == domain.FillStopLoss || fill.Role == domain.FillTakeProfit {
			result = append(result, fill.ClosedTradeNetMinor)
		}
	}
	return result
}

func pathStats(pnls []int64, initialEquity, ruinBPS int64) (PathStats, error) {
	if initialEquity <= 0 {
		return PathStats{}, errors.New("initial equity must be positive")
	}
	equity, peak, total := initialEquity, initialEquity, int64(0)
	stats := PathStats{}
	consecutiveLosses := 0
	for _, pnl := range pnls {
		var ok bool
		total, ok = safeAdd(total, pnl)
		if !ok {
			return PathStats{}, errors.New("path P&L overflow")
		}
		equity, ok = safeAdd(equity, pnl)
		if !ok {
			return PathStats{}, errors.New("path equity overflow")
		}
		if equity > peak {
			peak = equity
		}
		drawdownMinor := positiveDifference(peak, equity)
		drawdownBPS := ratioBPS(drawdownMinor, peak)
		if drawdownMinor > stats.MaxDrawdownMinor {
			stats.MaxDrawdownMinor = drawdownMinor
		}
		if drawdownBPS > stats.MaxDrawdownBPS {
			stats.MaxDrawdownBPS = drawdownBPS
		}
		if drawdownBPS >= ruinBPS || equity <= 0 {
			stats.RuinThresholdHit = true
		}
		if pnl < 0 {
			consecutiveLosses++
			if consecutiveLosses > stats.MaxConsecutiveLosses {
				stats.MaxConsecutiveLosses = consecutiveLosses
			}
		} else {
			consecutiveLosses = 0
		}
	}
	stats.EndingPnLMinor = total
	return stats, nil
}

func stationaryBootstrap(pnls []int64, initialEquity int64, config Config) (BootstrapSummary, error) {
	ending := make([]int64, config.Iterations)
	drawdownMinor := make([]int64, config.Iterations)
	drawdownBPS := make([]int64, config.Iterations)
	lossStreak := make([]int, config.Iterations)
	random := splitMix64(config.Seed)
	losses, ruins := 0, 0
	meanEnding := 0.0
	path := make([]int64, len(pnls))
	for iteration := 0; iteration < config.Iterations; iteration++ {
		index := 0
		for position := range path {
			if position == 0 || random.intn(config.MeanBlockLength) == 0 {
				index = random.intn(len(pnls))
			} else {
				index = (index + 1) % len(pnls)
			}
			path[position] = pnls[index]
		}
		stats, err := pathStats(path, initialEquity, config.RuinDrawdownBPS)
		if err != nil {
			return BootstrapSummary{}, err
		}
		ending[iteration], drawdownMinor[iteration], drawdownBPS[iteration] =
			stats.EndingPnLMinor, stats.MaxDrawdownMinor, stats.MaxDrawdownBPS
		lossStreak[iteration] = stats.MaxConsecutiveLosses
		meanEnding += float64(stats.EndingPnLMinor)
		if stats.EndingPnLMinor < 0 {
			losses++
		}
		if stats.RuinThresholdHit {
			ruins++
		}
	}
	return BootstrapSummary{
		Method:             "POLITIS_ROMANO_STATIONARY_CIRCULAR_V1",
		RestartProbability: 1 / float64(config.MeanBlockLength),
		Iterations:         config.Iterations, MeanBlockLength: config.MeanBlockLength, Seed: config.Seed,
		EndingPnLMinor: quantilesInt64(ending), MaxDrawdownMinor: quantilesInt64(drawdownMinor),
		MaxDrawdownBPS: quantilesInt64(drawdownBPS), MaxConsecutiveLosses: quantilesInt(lossStreak),
		MeanEndingPnLMinor:         meanEnding / float64(config.Iterations),
		ProbabilityOfLoss:          float64(losses) / float64(config.Iterations),
		ProbabilityOfRuinThreshold: float64(ruins) / float64(config.Iterations),
	}, nil
}

func quantilesInt64(values []int64) Int64Quantiles {
	copy := append([]int64(nil), values...)
	sort.Slice(copy, func(i, j int) bool { return copy[i] < copy[j] })
	return Int64Quantiles{P05: nearestInt64(copy, 0.05), P50: nearestInt64(copy, 0.50),
		P95: nearestInt64(copy, 0.95), P99: nearestInt64(copy, 0.99)}
}

func quantilesInt(values []int) IntQuantiles {
	copy := append([]int(nil), values...)
	sort.Ints(copy)
	return IntQuantiles{P50: nearestInt(copy, 0.50), P95: nearestInt(copy, 0.95), P99: nearestInt(copy, 0.99)}
}

func nearestInt64(sorted []int64, probability float64) int64 {
	index := int(math.Ceil(probability*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func nearestInt(sorted []int, probability float64) int {
	index := int(math.Ceil(probability*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func equityReturns(pnls []int64, initialEquity int64, riskFree float64) ([]float64, error) {
	equity := initialEquity
	returns := make([]float64, len(pnls))
	for index, pnl := range pnls {
		if equity <= 0 {
			return nil, errors.New("cannot calculate returns after non-positive equity")
		}
		returns[index] = float64(pnl)/float64(equity) - riskFree
		var ok bool
		equity, ok = safeAdd(equity, pnl)
		if !ok {
			return nil, errors.New("return equity overflow")
		}
	}
	return returns, nil
}

func (r Result) Verify() error {
	if r.SchemaVersion != SchemaVersion || (r.Mode != "single" && r.Mode != "shared") ||
		r.ExperimentID == "" || r.TradeCount < 3 || r.InitialEquityMinor <= 0 ||
		!validHash(r.ManifestSHA256) || !validHash(r.SourceResultSHA256) ||
		!validHash(r.TradePnLSHA256) || !validHash(r.ResultSHA256) {
		return errors.New("invalid robustness result identity")
	}
	if err := r.Config.Validate(); err != nil {
		return err
	}
	if r.Bootstrap.Iterations != r.Config.Iterations || r.Bootstrap.MeanBlockLength != r.Config.MeanBlockLength ||
		r.Bootstrap.Seed != r.Config.Seed || !probability(r.Bootstrap.ProbabilityOfLoss) ||
		!probability(r.Bootstrap.ProbabilityOfRuinThreshold) ||
		r.Bootstrap.Method != "POLITIS_ROMANO_STATIONARY_CIRCULAR_V1" ||
		r.Bootstrap.RestartProbability != 1/float64(r.Config.MeanBlockLength) ||
		r.DeflatedSharpe.SampleCount != r.TradeCount || r.DeflatedSharpe.TotalTrials != len(r.Config.TrialSharpeRatios) ||
		r.DeflatedSharpe.IndependentTrials != r.Config.IndependentTrials ||
		r.DeflatedSharpe.ReturnUnit != "PER_TRADE_EXCESS_RETURN" ||
		r.DeflatedSharpe.SharpeEstimator != "MEAN_OVER_SAMPLE_STANDARD_DEVIATION_V1" ||
		r.DeflatedSharpe.MomentEstimator != "POPULATION_CENTRAL_MOMENTS_V1" ||
		r.DeflatedSharpe.SelectionThreshold != "EXPECTED_MAXIMUM_SHARPE_BAILEY_LOPEZ_DE_PRADO_V1" ||
		!finite(r.DeflatedSharpe.ObservedSharpe) || !finite(r.DeflatedSharpe.Skewness) ||
		!finite(r.DeflatedSharpe.Kurtosis) || !finite(r.DeflatedSharpe.TrialSharpeVariance) ||
		!finite(r.DeflatedSharpe.ExpectedMaximumSharpe) ||
		!finite(r.Bootstrap.MeanEndingPnLMinor) || !probability(r.DeflatedSharpe.DeflatedSharpeProbability) {
		return errors.New("invalid robustness result metrics")
	}
	if !orderedInt64(r.Bootstrap.EndingPnLMinor) || !orderedInt64(r.Bootstrap.MaxDrawdownMinor) ||
		!orderedInt64(r.Bootstrap.MaxDrawdownBPS) || !orderedInt(r.Bootstrap.MaxConsecutiveLosses) {
		return errors.New("invalid robustness quantiles")
	}
	want, err := r.digest()
	if err != nil {
		return err
	}
	if want != r.ResultSHA256 {
		return errors.New("robustness result hash mismatch")
	}
	return nil
}

func (r Result) digest() (string, error) {
	copy := r
	copy.ResultSHA256 = ""
	return experiment.Digest(copy)
}

func orderedInt64(value Int64Quantiles) bool {
	return value.P05 <= value.P50 && value.P50 <= value.P95 && value.P95 <= value.P99
}

func orderedInt(value IntQuantiles) bool {
	return value.P50 <= value.P95 && value.P95 <= value.P99
}

func probability(value float64) bool { return finite(value) && value >= 0 && value <= 1 }
func finite(value float64) bool      { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func safeAdd(left, right int64) (int64, bool) {
	value := new(big.Int).Add(big.NewInt(left), big.NewInt(right))
	return value.Int64(), value.IsInt64()
}

func positiveDifference(high, low int64) int64 {
	if low >= high {
		return 0
	}
	value := new(big.Int).Sub(big.NewInt(high), big.NewInt(low))
	if !value.IsInt64() {
		return math.MaxInt64
	}
	return value.Int64()
}

func ratioBPS(value, base int64) int64 {
	if value <= 0 || base <= 0 {
		return 0
	}
	ratio := new(big.Int).Mul(big.NewInt(value), big.NewInt(10000))
	ratio.Quo(ratio, big.NewInt(base))
	if !ratio.IsInt64() {
		return math.MaxInt64
	}
	return ratio.Int64()
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

type splitMix64 uint64

func (r *splitMix64) next() uint64 {
	*r += 0x9e3779b97f4a7c15
	z := uint64(*r)
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *splitMix64) intn(limit int) int {
	if limit <= 0 {
		panic("splitMix64 intn requires a positive limit")
	}
	bound := uint64(limit)
	threshold := -bound % bound
	for {
		value := r.next()
		if value >= threshold {
			return int(value % bound)
		}
	}
}
