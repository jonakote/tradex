#!/usr/bin/env bash
# Watchdog for the two live `tradex execute` demo-trading processes.
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

check_and_restart "docs/examples_watch_forward_daily_ibs_v2.json" "data/execute_v2.log"
check_and_restart "docs/examples_watch_forward_daily_ibs_longonly_v2.json" "data/execute_longonly_v2.log"
check_and_restart "docs/examples_watch_forward_daily_rsi2_v2.json" "data/execute_rsi2_v2.log"
