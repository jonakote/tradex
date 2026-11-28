#!/usr/bin/env bash
# Watchdog for the live `tradex execute` demo-trading processes.
# Run periodically (via cron, see docs/MEANREVERSION_SPEC.md) — checks
# each one is actually running, restarts it if not, and logs every
# check/restart so a silent stop (the thing this script exists to catch)
# leaves a record instead of being invisible.
set -euo pipefail
cd "$(dirname "$0")/.."

LOG="data/watchdog.log"
mkdir -p data

check_and_restart() {
    local config="$1"
    local logfile="$2"
    if pgrep -f "bin/tradex execute $config" > /dev/null 2>&1; then
        echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) OK: $config is running" >> "$LOG"
    else
        echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) DOWN: $config is NOT running — restarting" >> "$LOG"
        nohup ./bin/tradex execute "$config" >> "$logfile" 2>&1 &
        disown
        sleep 2
        if pgrep -f "bin/tradex execute $config" > /dev/null 2>&1; then
            echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) RESTARTED: $config is back up" >> "$LOG"
        else
            echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) FAILED TO RESTART: $config — manual attention needed" >> "$LOG"
        fi
    fi
}

check_and_restart "docs/examples_watch_forward_daily_ibs_longonly_v2.json" "data/execute_longonly_v2.log"
check_and_restart "docs/examples_watch_forward_daily_rsi2_v2.json" "data/execute_rsi2_v2.log"
check_and_restart "docs/examples_watch_forward_daily_confluence_both_v2.json" "data/execute_confluence_both_v2.log"
check_and_restart "docs/examples_watch_forward_daily_confluence_longonly_v2.json" "data/execute_confluence_longonly_v2.log"
check_and_restart "docs/examples_watch_forward_daily_standalone_v2.json" "data/execute_standalone_v2.log"
check_and_restart "docs/examples_watch_forward_daily_rsi2_btc_v2.json" "data/execute_rsi2_btc_v2.log"
check_and_restart "docs/examples_watch_forward_daily_shares_v2.json" "data/execute_shares_v2.log"
# Added 2026-11-25: these two were deployed mid-session (crypto breadth
# round 8, XRPUSD forward test) and silently missed by this script for
# days -- both went down in a full outage around 2026-11-25T01:30-20:48Z
# and never came back because nothing was watching them. Caught only
# when the user said "too quiet" and a live process count turned up 7/9,
# not 9/9. Keep this list in sync with the actual deployed process set,
# not just whatever existed when the script was first written.
check_and_restart "docs/examples_watch_forward_daily_crypto_v2.json" "data/execute_crypto_v2.log"
check_and_restart "docs/examples_watch_forward_xrpusd_rsi2_execute_v1.json" "data/execute_xrpusd_rsi2_v1.log"
# Added 2026-11-26: MONDAYGAP, the first from-scratch (not literature)
# strategy this project found, per the user's explicit request to build
# one from basic price action. See execution.MeasuredMondayGapStats.
check_and_restart "docs/examples_watch_forward_mondaygap_v1.json" "data/execute_mondaygap_v1.log"
# Added 2026-11-28: the first intraday (15m) strategy this project has
# deployed -- RSI2, late-session (16-21 UTC), EURUSD/USDJPY.
check_and_restart "docs/examples_watch_forward_intraday_rsi2_v1.json" "data/execute_examples_watch_forward_intraday_rsi2_v1.log"
# Added 2026-11-28: second intraday strategy, overnight session (21-01
# UTC), EURJPY/EURGBP/USDCAD.
check_and_restart "docs/examples_watch_forward_intraday_rsi2_overnight_v1.json" "data/execute_examples_watch_forward_intraday_rsi2_overnight_v1.log"
