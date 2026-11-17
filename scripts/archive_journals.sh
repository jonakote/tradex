#!/usr/bin/env bash
# Commits and pushes the real-trade/signal journals -- the only durable
# record of what the live system actually did, not reproducible from the
# vendor. Run daily via cron; a no-op commit when nothing changed is fine
# (git exits cleanly either way, checked below).
set -euo pipefail
cd "$(dirname "$0")/.."

git add data/demo_executions.jsonl data/forward_signals_daily_*.jsonl 2>/dev/null || true

if git diff --cached --quiet; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) no journal changes to archive" >> data/journal_archive.log
    exit 0
fi

git commit -m "Archive journals $(date -u +%Y-%m-%d)" >/dev/null
git push origin main >/dev/null 2>&1
echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) archived journal changes, pushed to main" >> data/journal_archive.log
