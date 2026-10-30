# Triangle Pattern Strategy Specification v0.1

Triangle detection is a research hypothesis and a feature detector. It is not
an automatic trade signal, and it cannot bypass context, trigger, risk,
portfolio, execution, or kill-switch gates.

## Supported forms

- `ASCENDING_TRIANGLE`: materially flat confirmed highs and rising confirmed
  lows.
- `DESCENDING_TRIANGLE`: falling confirmed highs and materially flat confirmed
  lows.
- `SYMMETRICAL_TRIANGLE`: falling confirmed highs and rising confirmed lows.

## Required evidence

The detector requires at least three confirmed touches on each boundary. The
two boundary lines must fit their selected swings within a configurable tick
tolerance, have positive height at the latest pivot, and converge to a future
projected apex. A wick through a boundary is not a breakout; a completed candle
must close beyond the projected boundary plus the configured breakout buffer.

## Configuration

- minimum touches per boundary;
- flat-slope tolerance in ticks per pivot step;
- minimum directional slope in ticks per pivot step;
- line-fitting tolerance in ticks;
- minimum initial triangle height in ticks;
- breakout buffer in ticks;
- setup and trigger expiry.

All thresholds are versioned and must be frozen before a backtest. The detector
uses only confirmed swings and records the pivot/confirmation timestamps so it
cannot introduce look-ahead bias.

## Strategy lifecycle

`NO_TRADE -> WATCH -> SETUP -> ARMED -> TRIGGERED -> EXECUTABLE`

Triangle geometry can move a market into `WATCH` or `SETUP`; the breakout close
is the trigger candidate. A target must be selected before execution, preferably
from validated external/internal liquidity or a predeclared measured objective.
Position size is calculated by the independent risk engine from the objective
stop distance. The triangle detector never submits an order.

## Research questions

- Do triangle breakouts generalize across the supported instruments and
  sessions after spread, slippage, and rollover costs?
- Does breakout direction need higher-timeframe context or liquidity evidence?
- Which height, duration, retest, and target rules survive walk-forward and
  untouched holdout evaluation?
- How should failed breakouts and post-breakout retests be classified without
  hindsight?
