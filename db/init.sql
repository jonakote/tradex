-- Tradex Market Data Schema
-- Historical candle storage for the Market Data Manager

CREATE TABLE IF NOT EXISTS candles (
    id              BIGSERIAL PRIMARY KEY,
    symbol          TEXT NOT NULL,
    interval        TEXT NOT NULL,
    open_time       TIMESTAMPTZ NOT NULL,
    open            DOUBLE PRECISION NOT NULL,
    high            DOUBLE PRECISION NOT NULL,
    low             DOUBLE PRECISION NOT NULL,
    close           DOUBLE PRECISION NOT NULL,
    volume          DOUBLE PRECISION NOT NULL DEFAULT 0,
    provider        TEXT NOT NULL,
    fetched_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (symbol, interval, open_time)
);

-- Index for fast lookups by symbol + interval + time range
CREATE INDEX IF NOT EXISTS idx_candles_symbol_interval_time
    ON candles (symbol, interval, open_time DESC);

-- Provider usage tracking
CREATE TABLE IF NOT EXISTS provider_usage (
    id              BIGSERIAL PRIMARY KEY,
    provider        TEXT NOT NULL,
    request_type    TEXT NOT NULL,       -- 'candles', 'quote', 'symbols'
    symbol          TEXT,
    status          TEXT NOT NULL,       -- 'success', 'error', 'rate_limited', 'timeout'
    response_ms     INTEGER,
    error_message   TEXT,
    credits_used    INTEGER NOT NULL DEFAULT 1,
    requested_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_provider_usage_provider_time
    ON provider_usage (provider, requested_at DESC);

-- Daily quota summary (materialized for fast checks)
CREATE TABLE IF NOT EXISTS provider_quota_daily (
    provider        TEXT NOT NULL,
    date            DATE NOT NULL,
    requests_total  INTEGER NOT NULL DEFAULT 0,
    requests_success INTEGER NOT NULL DEFAULT 0,
    requests_failed INTEGER NOT NULL DEFAULT 0,
    requests_rate_limited INTEGER NOT NULL DEFAULT 0,
    credits_used    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (provider, date)
);

-- Provider health state
CREATE TABLE IF NOT EXISTS provider_health (
    provider            TEXT PRIMARY KEY,
    circuit_state       TEXT NOT NULL DEFAULT 'CLOSED',  -- CLOSED, OPEN, HALF_OPEN
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    last_success_at     TIMESTAMPTZ,
    last_failure_at     TIMESTAMPTZ,
    opened_at           TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
