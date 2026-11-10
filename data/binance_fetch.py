#!/usr/bin/env python3
"""Fetch Binance spot klines to the contlab CSV format. Keyless."""
import json, sys, time, urllib.request

def klines(symbol, interval, start_ms, end_ms):
    out = []
    cur = start_ms
    while cur < end_ms:
        url = (f"https://api.binance.com/api/v3/klines?symbol={symbol}"
               f"&interval={interval}&startTime={cur}&limit=1000")
        for attempt in range(5):
            try:
                with urllib.request.urlopen(url, timeout=30) as r:
                    rows = json.loads(r.read())
                break
            except Exception as e:
                if attempt == 4:
                    raise
                time.sleep(2)
        if not rows:
            break
        out.extend(rows)
        cur = rows[-1][0] + 1
        if len(rows) < 1000:
            break
        time.sleep(0.25)
    return out

def main():
    symbol, interval, out_path = sys.argv[1], sys.argv[2], sys.argv[3]
    start = int(time.mktime((2019, 1, 1, 0, 0, 0, 0, 0, 0))) * 1000
    end = int(time.time()) * 1000
    rows = klines(symbol, interval, start, end)
    seen = set()
    with open(out_path, "w") as f:
        f.write("open_time_ms,open,high,low,close,volume\n")
        for k in rows:
            t = k[0]
            if t in seen:
                continue
            seen.add(t)
            # open,high,low,close,volume
            f.write(f"{t},{k[1]},{k[2]},{k[3]},{k[4]},{int(float(k[5]))}\n")
    print(f"{symbol} {interval}: {len(seen)} bars -> {out_path}")

if __name__ == "__main__":
    main()
