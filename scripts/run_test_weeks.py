#!/usr/bin/env python3
"""Run backtests on representative weeks and compare to v2 baseline."""
import csv
import json
import os
import re
import subprocess
import sys

SUMMARY_PATTERN = re.compile(
    r"PnL\s+(?:roi=[^\s]+\s+)?net=([+-]?\d+(?:\.\d+)?)\s+realized=([+-]?\d+(?:\.\d+)?)\s+"
    r"unrealized=([+-]?\d+(?:\.\d+)?)\s+ending_equity=([+-]?\d+(?:\.\d+)?)"
)
GO_CACHE_DIR = os.path.join(os.path.dirname(os.path.dirname(__file__)), ".cache", "go-build")

# Representative weeks from v2 baseline
TEST_WEEKS = [
    # Big winners
    ("2025-01-22", "2025-01-29", 30648.27),  # +39.69%
    ("2025-06-25", "2025-07-02", 19442.70),  # +25.18%
    ("2025-09-03", "2025-09-10", 17398.40),  # +22.53%
    ("2025-10-29", "2025-11-05", 15760.91),  # +20.41%
    # Moderate winners
    ("2025-01-29", "2025-02-05", 8161.63),   # +10.57%
    ("2025-07-23", "2025-07-30", 5674.07),   # +7.35%
    # Flat/small
    ("2025-02-05", "2025-02-12", 270.00),    # +0.35%
    ("2025-12-03", "2025-12-10", -462.85),   # -0.60% (our iterative week)
    # Big losers
    ("2025-04-02", "2025-04-09", -6295.75),  # -8.15%
    ("2025-05-07", "2025-05-14", -6399.29),  # -8.29%
    ("2025-11-05", "2025-11-12", -13307.52), # -17.23%
    ("2025-11-26", "2025-12-03", -6820.07),  # -8.83%
]


def run_backtest(start, end):
    report_path = f".cache/backtest/weekly/{start}_{end}.json"
    cmd = ["go", "run", ".", "backtest", "-start", start, "-end", end, "-report-out", report_path]
    env = os.environ.copy()
    env.setdefault("GOCACHE", GO_CACHE_DIR)
    result = subprocess.run(cmd, capture_output=True, text=True, env=env)
    output = result.stderr + result.stdout
    if result.returncode != 0:
        return None, None, None
    match = SUMMARY_PATTERN.search(output)
    if not match:
        return None, None, None
    net_pnl = float(match.group(1))
    ending_equity = float(match.group(4))
    initial = ending_equity - net_pnl
    pct = (net_pnl / initial * 100) if initial > 0 else 0.0
    # Read trade count from report
    trades = 0
    try:
        with open(report_path) as f:
            rpt = json.load(f)
            trades = rpt.get("Trades", 0)
    except Exception:
        pass
    return net_pnl, pct, trades


def main():
    os.makedirs(".cache/backtest/weekly", exist_ok=True)
    print(f"{'Week':<25} {'v2 PnL':>10} {'Current PnL':>12} {'Delta':>10} {'v2 %':>8} {'Cur %':>8} {'Trades':>7}")
    print("-" * 90)

    total_v2 = 0.0
    total_cur = 0.0

    for start, end, v2_pnl in TEST_WEEKS:
        net_pnl, pct, trades = run_backtest(start, end)
        if net_pnl is None:
            print(f"{start} → {end}   {'FAILED':>10}")
            continue
        delta = net_pnl - v2_pnl
        total_v2 += v2_pnl
        total_cur += net_pnl
        label = f"{start} → {end}"
        trades_str = str(trades) if trades else "?"
        print(f"{label:<25} {v2_pnl:>+10.0f} {net_pnl:>+12.0f} {delta:>+10.0f} {(v2_pnl/77219*100):>+7.1f}% {pct:>+7.1f}% {trades_str:>7}")

    print("-" * 90)
    print(f"{'TOTAL':<25} {total_v2:>+10.0f} {total_cur:>+12.0f} {(total_cur - total_v2):>+10.0f}")
    v2_pct = total_v2 / 77219 * 100
    cur_pct = total_cur / 77219 * 100
    print(f"{'ROI':<25} {v2_pct:>+9.1f}% {cur_pct:>+11.1f}%")


if __name__ == "__main__":
    main()
