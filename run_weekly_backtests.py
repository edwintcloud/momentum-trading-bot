import datetime
import subprocess
import csv
import os
import re
import sys

SUMMARY_PATTERN = re.compile(
    r"PnL\s+net=([+-]?\d+(?:\.\d+)?)\s+realized=([+-]?\d+(?:\.\d+)?)\s+"
    r"unrealized=([+-]?\d+(?:\.\d+)?)\s+ending_equity=([+-]?\d+(?:\.\d+)?)"
)
GO_CACHE_DIR = os.path.join(os.path.dirname(__file__), ".cache", "go-build")


def extract_backtest_metrics(output):
    match = SUMMARY_PATTERN.search(output)
    if not match:
        return None, None
    net_pnl = float(match.group(1))
    ending_equity = float(match.group(4))
    return net_pnl, ending_equity


def run_backtest(start_date, end_date):
    print(f"Running backtest from {start_date} to {end_date}...", flush=True)
    cmd = ["go", "run", ".", "backtest", "-start", start_date, "-end", end_date]
    env = os.environ.copy()
    env.setdefault("GOCACHE", GO_CACHE_DIR)
    result = subprocess.run(cmd, capture_output=True, text=True, env=env)

    output = result.stderr + result.stdout

    if result.returncode != 0:
        print(f"Backtest command failed with exit code {result.returncode}.", flush=True)
        if output.strip():
            print(output.strip(), flush=True)
        return None, None

    net_pnl, ending_equity = extract_backtest_metrics(output)
    if net_pnl is None or ending_equity is None:
        print("Failed to find backtest results in output.", flush=True)
        if output.strip():
            print(output.strip(), flush=True)
        return None, None

    initial_equity = ending_equity - net_pnl
    profit_percentage = 0.0
    if initial_equity > 0:
        profit_percentage = (net_pnl / initial_equity) * 100

    return net_pnl, profit_percentage

def main():
    today = datetime.date.today()
    end_limit = today
    start_date = today - datetime.timedelta(days=90)

    args = sys.argv[1:]
    i = 0
    while i < len(args):
        flag = args[i]
        if flag in ("-start", "--start"):
            if i + 1 >= len(args):
                print("Missing value for -start", flush=True)
                return
            try:
                start_date = datetime.datetime.strptime(args[i + 1], "%Y-%m-%d").date()
            except ValueError:
                print("Invalid -start format. Use YYYY-MM-DD", flush=True)
                return
            i += 2
        elif flag in ("-end", "--end"):
            if i + 1 >= len(args):
                print("Missing value for -end", flush=True)
                return
            try:
                end_limit = datetime.datetime.strptime(args[i + 1], "%Y-%m-%d").date()
            except ValueError:
                print("Invalid -end format. Use YYYY-MM-DD", flush=True)
                return
            i += 2
        else:
            print(f"Unknown argument: {flag}", flush=True)
            return
    results = []

    current_start = start_date
    while current_start < end_limit:
        current_end = current_start + datetime.timedelta(days=7)
        if current_end > end_limit:
            current_end = end_limit

        start_str = current_start.strftime("%Y-%m-%d")
        end_str = current_end.strftime("%Y-%m-%d")

        net_pnl, profit_percentage = run_backtest(start_str, end_str)
        if net_pnl is not None:
            results.append({
                "Week Start": start_str,
                "Week End": end_str,
                "Net Profit": round(net_pnl, 2),
                "Profit Percentage": round(profit_percentage, 2)
            })
            print(f"Result: {net_pnl:.2f} net profit, {profit_percentage:.2f}% return", flush=True)
        else:
            print(f"Failed to get result for {start_str} to {end_str}", flush=True)

        current_start += datetime.timedelta(days=7)

    CSV_FILE = f"weekly_backtest_results_{start_date.strftime('%m%d%y')}-{end_limit.strftime('%m%d%y')}.csv"
    with open(CSV_FILE, mode="w", newline="") as file:
        writer = csv.DictWriter(file, fieldnames=["Week Start", "Week End", "Net Profit", "Profit Percentage"])
        writer.writeheader()
        for row in results:
            writer.writerow(row)

    print(f"Results saved to {CSV_FILE}", flush=True)

if __name__ == "__main__":
    main()
