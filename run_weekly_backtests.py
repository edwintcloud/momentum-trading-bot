import datetime
import subprocess
import csv
import re
import sys

def run_backtest(start_date, end_date):
    print(f"Running backtest from {start_date} to {end_date}...", flush=True)
    cmd = ["go", "run", ".", "backtest", "-start", start_date, "-end", end_date]
    result = subprocess.run(cmd, capture_output=True, text=True)

    # We need to extract net_pnl and ending_equity from the log output.
    output = result.stderr + result.stdout

    net_pnl_match = re.search(r'net_pnl=(-?\d+\.\d+)', output)
    ending_equity_match = re.search(r'ending_equity=(\d+\.\d+)', output)

    if not net_pnl_match or not ending_equity_match:
        print("Failed to find backtest results in output.", flush=True)
        return None, None

    net_pnl = float(net_pnl_match.group(1))
    ending_equity = float(ending_equity_match.group(1))

    # Calculate profit percentage
    # Initial equity = ending_equity - net_pnl
    initial_equity = ending_equity - net_pnl
    profit_percentage = 0.0
    if initial_equity > 0:
        profit_percentage = (net_pnl / initial_equity) * 100

    return net_pnl, profit_percentage

def main():
    start_date = datetime.date(2026, 1, 1)
    end_limit = datetime.date(2026, 3, 16)


    current_start = start_date
    while current_start < end_limit:
        results = []
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
        csv_file = "weekly_backtest_results.csv"
        with open(csv_file, mode='a', newline='') as file:
            writer = csv.DictWriter(file, fieldnames=["Week Start", "Week End", "Net Profit", "Profit Percentage"])
            writer.writeheader()
            for row in results:
                writer.writerow(row)


    print(f"Results saved to {csv_file}", flush=True)

if __name__ == "__main__":
    main()