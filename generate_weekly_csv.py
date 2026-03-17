import subprocess
import csv
import re
from datetime import datetime, timedelta

start_date = datetime(2026, 1, 5)
end_date = datetime(2026, 1, 30)

# Generate list of weeks (Monday to Friday)
weeks = []
current = start_date
while current <= end_date:
    week_start = current
    week_end = current + timedelta(days=4)
    if week_end <= end_date:
        weeks.append((week_start.strftime("%Y-%m-%d"), week_end.strftime("%Y-%m-%d")))
    current += timedelta(days=7)

print("Starting weekly backtests...")

# Pattern to extract net profit and ending equity
import re
log_pattern = re.compile(r"net_pnl=([-\d.]+)\s+ending_equity=([-\d.]+)")

with open("weekly_results.csv", "w", newline='') as f:
    writer = csv.writer(f)
    writer.writerow(["Week Start", "Week End", "Net Profit", "Profit Percentage"])

for w_start, w_end in weeks:
    print(f"Running backtest for {w_start} to {w_end}...", flush=True)
    cmd = ["go", "run", ".", "backtest", "-start", w_start, "-end", w_end]
    result = subprocess.run(cmd, capture_output=True, text=True)
    
    # Combined stdout and stderr
    output = result.stdout + result.stderr
    
    match = log_pattern.search(output)
    if match:
        net_pnl = float(match.group(1))
        ending_equity = float(match.group(2))
        initial_equity = ending_equity - net_pnl
        profit_pct = (net_pnl / initial_equity) * 100 if initial_equity != 0 else 0
        row = [w_start, w_end, f"${net_pnl:.2f}", f"{profit_pct:.2f}%"]
        print(f"  Result: ${net_pnl:.2f} ({profit_pct:.2f}%)", flush=True)
    else:
        print(f"  Result: Failed to parse output", flush=True)
        row = [w_start, w_end, "ERROR", "ERROR"]
        
    with open("weekly_results.csv", "a", newline='') as f:
        writer = csv.writer(f)
        writer.writerow(row)

print("Saved all backtest results to weekly_results.csv")
