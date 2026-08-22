# Robustness analysis method

Tradex robustness artifacts are diagnostic research outputs, not forecasts or promotion gates by themselves. Version 1 combines a stationary trade-outcome bootstrap with a Deflated Sharpe Ratio (DSR) probability.

## Stationary bootstrap

The source sequence is the account-wide, chronological series of completed-trade net P&L in account minor units. Tradex uses a circular stationary bootstrap based on Politis and Romano's random-length block construction. A new source index is sampled uniformly for the first outcome and whenever a block restarts; otherwise the index advances by one and wraps at the end. The restart probability is `1 / mean_block_length`, producing geometric block lengths. See [Politis and Romano, “The Stationary Bootstrap”](https://doi.org/10.1080/01621459.1994.10476870).

Every seeded path has the same number of trades as the observed path. Tradex reports empirical nearest-rank quantiles for ending P&L, maximum drawdown in minor units and basis points, and maximum consecutive losses. It also reports the fraction of paths ending below zero and the fraction touching the configured peak-to-equity drawdown threshold.

Version 1 resamples absolute net trade P&L. It does not resize historical outcomes when simulated equity changes, and it cannot generate spread shocks, gaps, dependencies, or loss magnitudes absent from the source sample. The mean block length is a research input, not an automatically estimated truth.

## Deflated Sharpe Ratio

The DSR follows Bailey and López de Prado's correction for multiple testing and non-normal returns. Their method uses the selected Sharpe estimate, sample length, skewness, kurtosis, the number of independent trials, and variance across trial Sharpe estimates. See [Bailey and López de Prado, “The Deflated Sharpe Ratio”](https://www.davidhbailey.com/dhbpapers/deflated-sharpe.pdf).

Tradex v1 uses:

- chronological per-trade excess return: `closed_net_pnl / equity_before_trade - risk_free_return_per_trade`;
- Sharpe estimator: arithmetic mean divided by sample standard deviation;
- skewness and kurtosis: population central-moment estimators;
- expected-maximum-Sharpe selection threshold from the paper's extreme-value approximation;
- the standard Normal CDF to express DSR as a probability between zero and one.

The statistic is deliberately not annualized because trade arrival is irregular. Every value in `trial_sharpe_ratios` must therefore use the same per-trade estimator and return unit. The list must include failed and discarded experiments, not merely published candidates. `independent_trials` is explicit because correlated trials reduce the effective count; Tradex refuses values above the number of supplied trials but does not claim it can infer independence.

At least three completed trades are required mechanically, but such a tiny sample is not credible evidence. A high DSR from a short, biased, or economically unrealistic replay remains unreliable.
