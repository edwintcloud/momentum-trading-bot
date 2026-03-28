# Momentum Trading Bot

A modular momentum-trading system built in Go with a React operator dashboard. The full quant stack includes intraday alpha signals (OFI, VPIN, OBV, ORB, dollar bars), VaR/CVaR risk management, GARCH volatility forecasting, mean-variance optimization, risk parity portfolio construction, VWAP/TWAP execution algorithms, an ML scoring pipeline with meta-labeling and drift detection, a weekly auto-optimizer with guardrails and hot-reload, backtesting with walk-forward/CPCV validation, and live paper/real trading via the Alpaca Markets API.

> **Alpaca paid subscription required** — the bot uses the SIP data feed for real-time and historical market data.

## ⚠️ Legal & Financial Disclaimer

**This software is for educational and research purposes only. Do not use this code to make actual financial decisions with real money.**

### Not Financial Advice
The code, documentation, and algorithms provided in this repository do not constitute financial advice, investment advice, trading advice, or any other sort of advice. You should not treat any of the repository's content as such. 

### Risk of Loss
Trading equities involves a high degree of risk, particularly when executing active strategies like day trading, swing trading, or momentum trading. Market volatility can lead to substantial financial losses. You could lose some or all of your initial investment. Always conduct your own due diligence and consult with a licensed financial advisor before making any investment decisions.

### Software "As Is"
This trading bot is provided "as is" and "as available" without warranty of any kind, either express or implied, including, but not limited to, the implied warranties of merchantability and fitness for a particular purpose. The authors and contributors make no representations about the accuracy, reliability, or completeness of the software.

### Technical Limitations & Bugs
Algorithmic trading depends on complex systems, including third-party broker APIs, external charting webhooks, and live market data feeds. System failures, network outages, rate limits, or bugs in this code can result in unintended trades, orphaned orders, and significant financial loss. 

### Past Performance
Any backtesting results or simulated performance metrics included in this repository are hypothetical. Past performance of any trading system, indicator, or methodology is not indicative of future results. Live market conditions, including slippage and liquidity constraints, will often yield different outcomes than historical tests.

### Assumption of Liability
Under no circumstances will the authors, contributors, or copyright holders be held liable for any claim, damages, or other liability, whether in an action of contract, tort, or otherwise, arising from, out of, or in connection with the software or the use or other dealings in the software. By running this bot, you assume all responsibility for any trading losses you may incur.

## Architecture

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  React Dashboard (Vite + Tailwind + Recharts)                                │
│  Overview · Positions · Scanner · Trades · Logs · Controls                   │
└───────────────────────────┬──────────────────────────────────────────────────┘
                            │ REST + WebSocket
┌───────────────────────────▼──────────────────────────────────────────────────┐
│  Go Backend                                                                  │
│                                                                              │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐     │
│  │ Scanner  │→ │ Strategy │→ │   Risk   │→ │Portfolio │→ │Execution │     │
│  └──────────┘  └──────────┘  └──────────┘  │Construct.│  │  Router  │     │
│                                             └──────────┘  └──────────┘     │
│                                                                              │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐     │
│  │ Signals  │  │   ML     │  │ Regime   │  │ Backtest │  │Optimizer │     │
│  │(OFI,VPIN│  │(Scoring +│  │(HMM +   │  │ Engine   │  │(LHS +   │     │
│  │OBV, ORB) │  │MetaLabel+│  │threshold)│  │          │  │Bayesian) │     │
│  │          │  │Drift Det)│  │          │  │          │  │          │     │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  └──────────┘     │
│                                                                              │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐     │
│  │VaR/CVaR │  │  GARCH   │  │Risk Budg.│  │Analytics │  │Walk-Fwd /│     │
│  │Limits   │  │Volatility│  │(Dynamic) │  │(Factor   │  │CPCV /MHT│     │
│  │          │  │Forecast  │  │          │  │Decomp.)  │  │          │     │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  └──────────┘     │
│                                                                              │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                                  │
│  │  Auto-   │  │ Config   │  │ Impact   │                                  │
│  │Optimizer │  │ Watcher  │  │ Model    │                                  │
│  │(Sched. + │  │(Hot-     │  │(Almgren- │                                  │
│  │Guardrail)│  │ Reload)  │  │ Chriss)  │                                  │
│  └──────────┘  └──────────┘  └──────────┘                                  │
└──────────────────────────────────────────────────────────────────────────────┘
                            │
                 ┌──────────▼───────────┐
                 │  Alpaca Markets API  │
                 │  (Streaming + Hist.) │
                 └──────────────────────┘
```

## Features

### Intraday Alpha Signals
- **Order Flow Imbalance (OFI)** — real-time buy/sell aggression measurement with anti-spoofing persistence filter
- **VPIN** — Volume-Synchronized Probability of Informed Trading for flow toxicity detection
- **OBV Divergence** — On-Balance Volume divergence detection (bullish/bearish)
- **Dollar Bars / Volume Bars** — volume-clock sampling for ML feature engineering
- **Opening Range Breakout (ORB)** — with gap filter and volume confirmation
- **Signal Aggregator** — combines multiple signal sources with configurable weights

### Trading Engine
- **Two-Path Momentum Scanner** — scans for candidates via two independent paths:
  - **Path 1: Gap Scanner** — traditional overnight gap filter (`MinGapPercent`), relative volume, premarket volume
  - **Path 2: HOD Momo Scanner** — detects intraday momentum runners (stocks up `HODMomoMinIntradayPct`%+ from open with high relative volume), bypasses gap and premarket volume requirements. Catches stocks like ANNA that gap small but run 50-100% intraday.
  - Both paths share price, float, and HOD proximity filters
  - New `hod_breakout` setup type when price is within 1% of session high with strong intraday move
  - New `hod_pullback` setup type when price is between `HODMomoMaxDistFromHigh` and `HODMomoPullbackMaxDist` of HOD — catches pullback entries on momentum runners (e.g., ANNA 7.3% below HOD)
  - HOD momo qualified stocks bypass the general `MaxDistanceFromHighPct` filter
- **Scanner Filters** — price, float (`MaxFloat`/`MinFloat`), minimum daily volume (`MinPrevDayVolume`), HOD proximity, RSI overbought/oversold
- **Dual-Direction Strategy** — long breakouts/pullbacks, short breakdowns
- **Four Playbook Types** — Breakout, Pullback, Continuation, Reversal — each with its own exit parameters
- **Market Regime Detection** — threshold-based (default) and HMM regime detector
- **Confidence-Based Entry Scoring** — with regime gating and ML score integration
- **Improved Diagnostics** — candidate rejection reasons include `market-closed`, `regime-gated`, `past-entry-deadline`, `cooldown`, `existing-position`, `loss-cooldown` (replaces generic `no-signal`)

### Risk Management
- Portfolio heat tracking with alert thresholds
- Graduated daily loss response (moderate / severe / halt tiers)
- Sector concentration limits (max positions + exposure per sector; stocks with unknown/empty GICS sector bypass sector limits)
- Correlation-aware position approval
- Kelly Criterion position sizing
- Volatility-target position sizing (with configurable max vol estimate clamp via `MaxVolEstimate`)
- Position size floor (`MinPositionNotionalPct`) — prevents vol-target from sizing momentum positions to near-zero
- Defensive stops for broker-seeded positions — on restart, existing broker positions automatically get stop prices computed from the previous day's low (via Alpaca snapshots) or a configurable ATR fallback percentage (`EntryATRPercentFallback`), ensuring no position is ever unprotected
- Drawdown-based risk reduction (linear scale to max acceptable drawdown)
- Per-minute entry throttle (`MaxEntriesPerMinute`)
- **Value-at-Risk (VaR)** — parametric and historical simulation
- **Conditional VaR (CVaR / Expected Shortfall)**
- **Intraday VaR limit monitoring**
- **GARCH(1,1) volatility forecasting**
- **Dynamic risk budgeting** with volatility-targeted sizing

### Portfolio Construction
- **Mean-Variance Optimization (Markowitz)** with Ledoit-Wolf shrinkage
- **Risk Parity** with EWMA volatility tracking
- **Factor-Neutral construction** (beta hedging vs SPY)
- **HHI diversification metric** with concentration alerts
- **Long-Short balancing** (dollar-neutral, beta-neutral, sector-neutral)

### Execution Optimization
- **Exit order retry with widening slippage** — exit orders (close/partial) retry up to 5 times with progressively wider limit price slippage (1x → 3x → 5x → 8x → 12x). Market orders are never used — limit-only for pre/post-market compatibility and price control. Entry orders are not retried (missing an entry is acceptable).
- **VWAP execution** — volume-profile-weighted order slicing
- **TWAP execution** — equal time-slice distribution
- **Adaptive limit pricing** — auto-widening with max slippage control
- **Execution router** — auto-selects VWAP/TWAP/direct based on order size
- Almgren-Chriss market impact model
- Percentage-based slippage model (liquid / mid / illiquid tiers)

### Machine Learning Pipeline
- **Feature engineering** — 17 causal features (relative volume, gap %, returns, breakout %, VWAP distance, EMA alignment, RSI, ATR, consolidation range, pullback depth, time-of-day, regime probability, MACD histogram, etc.)
- **Rule-based scorer** — heuristic scoring engine with direction-aware feature weighting
- **Meta-labeling** with triple barrier method (wired into strategy)
- **ML scoring** with confidence threshold (wired into strategy)
- **Concept drift detection** (PSI + rolling Sharpe decay)

### Trade Management
- RSI overbought/oversold filter
- Time-of-day adaptive parameters with configurable midday score multiplier
- **Entry deadline** — block new entries after configurable minutes from open (e.g., 120 min = 2 hours)
- **Risk/reward pre-check** — reject trades where estimated reward < configurable R:R ratio × risk
- Partial exit framework (two trigger levels with configurable percentages) — partial exits use a distinct `IntentPartial` intent that correctly routes to `ReducePosition` instead of `ClosePosition`
- Adaptive trailing stops with volatility factor
- Mean-reversion overlay (Bollinger bands + ADX filter)

### Scanner Quality Filters
- **HOD proximity filter** — longs must be within configurable % of high of day (buying strength, not catching knives)
- **Volume-on-pullback scoring** — decreasing volume on pullback candles = score bonus; increasing volume = penalty (distribution detection)

### Backtesting & Validation
- Backtest engine with streaming bar iterator (memory efficient)
- Monte Carlo simulation (configurable number of sims)
- Bootstrap significance testing
- Transaction cost model (commissions, SEC/TAF fees, spread costs)
- Deflated Sharpe Ratio
- Walk-forward analysis (in-sample / out-of-sample with purge gap)
- CPCV — Combinatorial Purged Cross-Validation
- **Bonferroni correction** for multiple hypothesis testing
- **Benjamini-Hochberg FDR** procedure
- Integrated MHT corrections with optimizer output

### Optimization
- Latin Hypercube Sampling (LHS) grid search
- Bayesian optimization with Gaussian Process surrogate + Expected Improvement
- Sensitivity analysis
- Strategy profiles: `baseline_breakout` (default), with support for custom profiles
- Optimizer artifact output with promotion workflow (paper → live)

### Auto-Optimizer
- Weekly automatic optimization (Saturday 6 AM ET)
- Guardrail validation (Sharpe, win rate, drawdown, trade count, DSR, improvement)
- Automatic profile promotion with atomic file writes and backups
- Hot-reload — live bot picks up new profile without restart
- Telegram notifications for optimizer events and live trade executions

### Dashboard
- **Mobile-friendly** — Collapsible sidebar with hamburger menu, responsive card layouts for data tables, touch-friendly navigation
- **Overview** — Account summary, PnL chart, regime status, recent activity
- **Positions** — Live positions with unrealized PnL, side indicators, and exit status
- **Scanner** — Real-time candidate scoring with momentum metrics
- **Trades** — Historical trade view with date picker, CSV export, entry/exit time tooltips, and mobile-optimized date navigation
- **Logs** — Filterable system log viewer
- **Controls** — Pause/resume, emergency stop, close all positions

### API
- `GET  /api/dashboard` — Full dashboard snapshot
- `GET  /api/status` — System status
- `GET  /api/positions` — Open positions
- `GET  /api/candidates` — Scanner candidates
- `GET  /api/trades` — Today's closed trades (live)
- `GET  /api/trades/history?date=YYYY-MM-DD` — Historical trades for a specific date
- `GET  /api/trades/export?date=YYYY-MM-DD` — CSV export of trades for a specific date
- `GET  /api/trades/dates` — List of dates with trade activity (up to 90 days)
- `GET  /api/logs` — System logs
- `GET  /api/config` — Get trading config
- `POST /api/config` — Update trading config (hot-reload)
- `GET  /api/performance` — Performance metrics
- `POST /api/pause` — Pause trading
- `POST /api/resume` — Resume trading
- `POST /api/close-all` — Close all positions
- `POST /api/emergency-stop` — Emergency stop
- `GET  /ws` — Real-time dashboard updates
- `GET  /healthz` — Liveness probe (public)
- `GET  /readyz` — Readiness probe (public)

## Quant Research

The system's quantitative methodologies are documented in the comprehensive research document:
[`docs/quant_research_findings.md`](docs/quant_research_findings.md)

This ~1600-line document covers:
- **Section 1** — Intraday Alpha Factors & Signals (OFI, VPIN, momentum, mean-reversion, volume signals, ORB, sentiment)
- **Section 2** — Risk Management (Kelly, VaR/CVaR, drawdown, correlation, HMM/GARCH, dynamic risk budgeting)
- **Section 3** — Portfolio Construction (Markowitz MVO, risk parity, factor-neutral, HHI, L/S balancing)
- **Section 4** — Execution Optimization (VWAP, TWAP, Almgren-Chriss, adaptive limit pricing)
- **Section 5** — Statistical Validation (walk-forward, CPCV, MHT corrections, DSR, regime-conditional backtesting)
- **Section 6** — Machine Learning (feature engineering, XGBoost/LightGBM, online learning, meta-labeling, ensembles)
- **Section 7** — Implementation Priorities & Integration Notes

Each section includes formulas, implementation guidance, key parameters, and academic references.

## Auto-Optimizer

The auto-optimizer runs as a sidecar process that automatically tunes trading parameters on a weekly schedule.

### How It Works

1. **Saturday 6 AM ET** — fetches the last 3 months of market data from Alpaca
2. Runs the full optimizer (LHS grid search + Bayesian optimization)
3. Validates the candidate against guardrails:
   - Sharpe ratio ≥ 0.5
   - Win rate ≥ 30%
   - Max drawdown ≤ 20%
   - At least 20 trades
   - DSR > 0.50
   - Must improve on current profile by ≥ 10% (configurable)
4. If all checks pass: backs up the current profile and writes the new one
5. The live bot detects the file change within 10 seconds and hot-reloads the config
6. Sends Telegram notification with metrics summary

### CLI Usage

```bash
# Default: weekly schedule, update profiles/default.json
go run . auto-optimize -profile profiles/default.json

# Run immediately, then continue on schedule
go run . auto-optimize -profile profiles/default.json -now

# Run a single optimization and exit (no scheduling loop)
go run . auto-optimize -profile profiles/default.json -once

# Daily schedule with custom guardrails
go run . auto-optimize -profile profiles/default.json -schedule daily -min-sharpe 0.7 -min-winrate 0.35

# All flags
go run . auto-optimize \
  -profile profiles/default.json \
  -schedule weekly \
  -out .cache/optimizer \
  -min-sharpe 0.5 \
  -min-winrate 0.30 \
  -max-drawdown 0.20 \
  -require-improvement \
  -max-symbols 500 \
  -now \
  -once
```

| Flag | Description | Default |
|---|---|---|
| `-profile` | Path to the active profile to update | `profiles/default.json` |
| `-schedule` | Schedule: `weekly` or `daily` | `weekly` |
| `-out` | Optimizer output directory | `.cache/optimizer` |
| `-min-sharpe` | Minimum Sharpe ratio (profit factor) | `0.5` |
| `-min-winrate` | Minimum win rate | `0.30` |
| `-max-drawdown` | Maximum drawdown percentage | `0.20` |
| `-require-improvement` | Require improvement over current profile | `true` |
| `-max-symbols` | Maximum symbols for optimization (0=unlimited) | `500` |
| `-now` | Run optimization immediately, then continue on schedule | `false` |
| `-once` | Run a single optimization and exit (no scheduling loop) | `false` |

### Docker

The auto-optimizer and auto-train-ml services both run automatically as sidecars in Docker Compose with the `-now` flag, so they each run an immediate cycle on startup and then continue on their weekly schedules. The bot, optimizer, and ML trainer share the `profiles/`, `artifacts/`, `.cache/`, and `docs/` volumes so promoted profiles and ML artifacts are available to live trading and remain backtest-replayable.

```bash
docker compose up -d  # starts postgres + bot + auto-optimizer + auto-train-ml
```

## Telegram Notifications

Telegram notifications can be used for both automation events and live trading events.

### Setup

1. Create a Telegram bot via [@BotFather](https://t.me/BotFather) and get the bot token
2. Send a message to your bot, then get your chat ID from `https://api.telegram.org/bot<TOKEN>/getUpdates`
3. Add to `.env`:
   ```
   TELEGRAM_BOT_TOKEN=123456:ABC-DEF...
   TELEGRAM_CHAT_ID=987654321
   ```

### What Gets Sent

- Auto-optimizer lifecycle updates: start, completion, promotion, and rejection
- Live trade open notifications after a broker-confirmed opening fill, including side, fill price, and stop price
- Live trade close notifications after the position is fully closed, including the exit reason
- End-of-day summary notifications at 8:00 PM ET with net profit, ROI, and trade count

Notifications are optional. If the env vars are not set, the optimizer and live bot both run silently and only write local logs.

## Quick Start

### Prerequisites
- Go 1.24+
- Node.js 22+ (for dashboard)
- PostgreSQL 16+ (optional — falls back to filesystem storage)
- Alpaca Markets account with paid subscription (SIP data feed required)

### Local Development

```bash
git clone https://github.com/edwintcloud/momentum-trading-bot.git
cd momentum-trading-bot
cp .env.example .env
# Edit .env with your Alpaca API keys

# Build dashboard
cd web && npm install && npm run build && cd ..

# Live paper trading
go run . live

# Backtest (auto-fetches bars from Alpaca, cached to .cache/bars/)
go run . backtest -start 2025-01-01 -end 2025-03-01

# Backtest with debug output for specific symbols
go run . backtest -start 2025-01-01 -end 2025-03-01 -debug AAPL,TSLA

# Backtest from local CSV
go run . backtest -start 2025-01-01 -end 2025-03-01 -data path/to/bars.csv

# Optimization
go run . optimize -as-of 2025-06-01

# Optimization with explicit start and max symbols
go run . optimize -as-of 2025-06-01 -start 2025-01-01 -max-symbols 200

# Auto-optimizer (runs on schedule)
go run . auto-optimize -profile profiles/default.json

# Weekly rolling backtests (Python helper)
python3 run_weekly_backtests.py -start 2025-01-01 -end 2025-06-01
```

> The backtest and optimize commands automatically fetch historical data from Alpaca. The `-symbols` flag is **not** required — the system discovers symbols automatically. Data is cached to `.cache/bars/` for fast subsequent runs.

### Docker

```bash
cp .env.example .env
# Edit .env with your Alpaca API keys

docker compose up -d
```

This starts PostgreSQL + the bot + the auto-optimizer + auto-train-ml. The `.cache` directory is mounted as a volume so cached data persists across container restarts. The auto-optimizer runs as a sidecar with `-now` for an immediate first optimization, then continues on the weekly schedule. The ML auto-trainer also runs as a sidecar with `-now`, trains on rolling windows, validates candidates against regression and annual guardrails, and only promotes model artifacts when they pass. The services share `profiles/` and `artifacts/` so live trading can hot-reload both profile and ML model changes. Dashboard at `http://localhost:8080`.

## Configuration

### Environment Variables

| Variable | Description | Default |
|---|---|---|
| `ALPACA_API_KEY` | Alpaca API key | (required) |
| `ALPACA_API_SECRET` | Alpaca API secret | (required) |
| `ALPACA_PAPER` | Use paper trading | `true` |
| `ALPACA_LIVE_TRADING_ENABLED` | Enable live trading (arming flag) | `false` |
| `ALPACA_SYMBOLS` | Comma-separated symbol list | (all — wildcard) |
| `DATABASE_URL` | PostgreSQL connection string | (filesystem fallback) |
| `CONTROL_PLANE_AUTH_TOKEN` | API auth token | (required) |
| `LISTEN_ADDR` | HTTP server listen address | `:8080` |
| `TRADING_PROFILE_PATH` | Path to JSON trading profile | `profiles/default.json` |
| `POSTGRES_DB` | PostgreSQL database name (Docker) | `momentum` |
| `POSTGRES_USER` | PostgreSQL user (Docker) | `momentum` |
| `POSTGRES_PASSWORD` | PostgreSQL password (Docker) | `momentum` |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token for optimizer and trade notifications | (optional) |
| `TELEGRAM_CHAT_ID` | Telegram chat ID for optimizer and trade notifications | (optional) |

> Alpaca paid subscription is required for the SIP data feed. The live-trading arming flag is intentional — the bot refuses to start in live mode unless `ALPACA_LIVE_TRADING_ENABLED=true`.

Control-plane access:
- The dashboard, `/api/*`, and `/ws` require HTTP Basic auth (username: `operator`, password: `CONTROL_PLANE_AUTH_TOKEN`)
- `GET /healthz` and `GET /readyz` stay public for probes

### Trading Profile

The bot uses versioned JSON trading profiles stored in `profiles/`:

| Profile | Description |
|---|---|
| `baseline_breakout` | Default balanced profile |

- Profile loaded from `TRADING_PROFILE_PATH` env var or bundled `profiles/default.json`
- `TuneTradingConfig()` fills any missing fields with sensible defaults based on broker equity and plan limits
- See `profiles/default.json` for the full config fields (~200 fields across Scanner, Strategy, Risk, Execution, Portfolio, Backtest, Alpha, ML, and Regime sections)

### Key Config Categories

**Core Risk** — `RiskPerTradePct`, `DailyLossLimitPct`, `MaxTradesPerDay`, `MaxOpenPositions`, `MaxExposurePct`, `MaxEntriesPerMinute`

**Scanner** — `MinPrice`, `MaxPrice`, `MinGapPercent`, `MinRelativeVolume`, `MinPremarketVolume`, `MinATRBars`, `MaxFloat`, `MinFloat`, `MinPrevDayVolume`, `FloatOverrideURL`

**Trade Management** — `TrailActivationR`, `ProfitTargetR`, `PartialExitsEnabled`, `EntryStopATRMultiplier`, `TrailATRMultiplier`, `TightTrailTriggerR`, `EntryDeadlineMinutesAfterOpen`, `MinRiskRewardRatio`

**Scanner Quality** — `MaxDistanceFromHighPct`, `VolumeOnPullbackEnabled`

**HOD Momo Scanner** — `HODMomoEnabled` (default: false), `HODMomoMinIntradayPct` (10%), `HODMomoMinRelativeVolume` (5x), `HODMomoMaxDistFromHigh` (5% — breakout range), `HODMomoPullbackMaxDist` (10% — pullback range), `HODMomoMinMinutesSinceOpen` (5 min)

**Position Sizing** — `MinPositionNotionalPct` (0 = disabled, 0.02 = 2% of equity floor), `MaxVolEstimate` (5.0 = cap annualized vol at 500%)

**Quant Features** — enable/disable flags for each feature: `EnableMarketRegime`, `KellySizingEnabled`, `VolTargetSizingEnabled`, `CorrelationCheckEnabled`, `FactorAnalysisEnabled`, `ImpactModelEnabled`, `HMMRegimeEnabled`

**Optimization** — `OptimizerSamples`, `OptimizerUseLHS`, `BayesianOptEnabled`, `WalkForwardEnabled`, `CPCVEnabled`

See `profiles/default.json` for the complete field reference.

## Project Structure

```
├── main.go                          # Entry point + CLI commands
├── backtest_command.go              # Backtest CLI command
├── backtest_dataset_iterator.go     # Streaming bar iterator
├── backtest_fetch.go                # Alpaca historical data fetcher
├── historical_cache_codec.go        # Bar cache read/write (binary+gzip codec)
├── instrument_universe.go           # ETF/derivative filtering for scanner universe
├── profile_runtime.go               # Runtime profile management
├── run_weekly_backtests.py          # Weekly rolling backtest helper (Python)
├── internal/
│   ├── alpaca/                      # Alpaca Markets integration
│   │   ├── client.go                # REST client + snapshot API
│   │   ├── float.go                 # Float data store (CSV loader)
│   │   ├── float_sec.go             # SEC EDGAR float data fetching
│   │   ├── historical.go            # Historical bar fetching
│   │   └── stream.go                # Real-time streaming
│   ├── analytics/
│   │   └── factors.go               # Fama-French factor decomposition
│   ├── api/server.go                # REST + WebSocket API
│   ├── autooptimize/                # Auto-optimizer sidecar
│   │   ├── scheduler.go             # Weekly/daily scheduler (cron-style)
│   │   ├── guardrails.go            # Candidate validation gates
│   │   ├── promoter.go              # Atomic profile promotion + backup
│   │   └── notifier.go              # Telegram notification integration
│   ├── backtest/                    # Backtesting & validation
│   │   ├── engine.go                # Core backtest engine
│   │   ├── iterator.go              # Bar iterator interface
│   │   ├── bootstrap.go             # Bootstrap significance testing
│   │   ├── cpcv.go                  # Combinatorial Purged Cross-Validation
│   │   ├── costs.go                 # Transaction cost model
│   │   ├── dsr.go                   # Deflated Sharpe Ratio
│   │   ├── mht.go                   # Multiple Hypothesis Testing corrections
│   │   ├── montecarlo.go            # Monte Carlo simulation
│   │   └── walkforward.go           # Walk-forward analysis
│   ├── config/                      # Configuration
│   │   ├── app.go                   # Environment config
│   │   ├── backtest.go              # Backtest run config
│   │   ├── config.go                # TradingConfig (~200 fields)
│   │   ├── profile.go               # Profile loading/saving
│   │   ├── tuning.go                # Auto-tuning defaults
│   │   └── watcher.go               # File watcher for hot-reload
│   ├── domain/                      # Core types
│   │   ├── types.go                 # Domain models
│   │   ├── trading.go               # Intent/side/direction helpers
│   │   └── regime.go                # Regime types
│   ├── execution/                   # Order execution
│   │   ├── execution.go             # Alpaca order execution
│   │   ├── paper.go                 # Paper broker for backtests (OHLC fills + slippage)
│   │   ├── impact.go                # Almgren-Chriss impact model
│   │   ├── vwap.go                  # VWAP execution algorithm
│   │   ├── twap.go                  # TWAP execution algorithm
│   │   ├── adaptivelimit.go         # Adaptive limit pricing
│   │   └── router.go                # Execution router (auto-select algo)
│   ├── market/normalizer.go         # Tick normalization + snapshot seeding
│   ├── markethours/hours.go         # ET market hours + holidays
│   ├── ml/                          # Machine learning pipeline
│   │   ├── scorer.go                # ML scoring (features, rule-based scorer, stub scorer)
│   │   ├── metalabel.go             # Meta-labeling (triple barrier)
│   │   └── drift.go                 # Concept drift detection (PSI + Sharpe)
│   ├── optimizer/                   # Parameter optimization
│   │   ├── optimizer.go             # LHS grid search + orchestration
│   │   ├── bayesian.go              # Bayesian optimization (GP + EI)
│   │   ├── sensitivity.go           # Sensitivity analysis
│   │   └── artifacts.go             # Optimizer output artifacts
│   ├── pipeline/
│   │   └── pipeline.go              # Channel-based trading pipeline (stages: normalize → scan → strategy → risk → execute)
│   ├── portfolio/                   # Portfolio management & construction
│   │   ├── manager.go               # Position tracking & PnL
│   │   ├── constructor.go           # Portfolio construction orchestrator
│   │   ├── meanvariance.go          # Mean-Variance Optimization (Markowitz)
│   │   ├── riskparity.go            # Risk Parity with EWMA
│   │   ├── factorneutral.go         # Factor-Neutral construction
│   │   ├── hhi.go                   # HHI diversification metric
│   │   └── longshort.go             # Long-Short balancing
│   ├── regime/                      # Market regime detection
│   │   ├── tracker.go               # Threshold-based regime tracker
│   │   └── hmm.go                   # HMM regime detector
│   ├── risk/                        # Risk management
│   │   ├── risk.go                  # Risk engine (all gates)
│   │   ├── correlation.go           # Correlation tracker
│   │   ├── volatility.go            # Volatility estimator
│   │   ├── var.go                   # VaR / CVaR (parametric + historical)
│   │   ├── garch.go                 # GARCH(1,1) volatility forecasting
│   │   └── riskbudget.go            # Dynamic risk budgeting
│   ├── runtime/state.go             # Runtime state management
│   ├── scanner/scanner.go           # Momentum scanner
│   ├── sector/lookup.go             # Sector classification
│   ├── signals/                     # Intraday alpha signals
│   │   ├── signals.go               # Signal aggregator
│   │   ├── ofi.go                   # Order Flow Imbalance
│   │   ├── vpin.go                  # VPIN (flow toxicity)
│   │   ├── obv.go                   # OBV divergence detection
│   │   ├── dollarbars.go            # Dollar bars / volume bars
│   │   └── orb.go                   # Opening Range Breakout
│   ├── storage/                     # Persistence
│   │   ├── postgres.go              # PostgreSQL
│   │   └── filesystem.go            # Filesystem fallback
│   ├── strategy/                    # Entry/exit logic
│   │   ├── strategy.go              # Strategy engine
│   │   └── tradeplan.go             # Trade plan types
│   ├── telemetry/                   # Logging
│   │   ├── logger.go                # Event logger
│   │   ├── composite.go             # Composite logger
│   │   └── telegram.go              # Telegram trade notifications
│   └── volumeprofile/profile.go     # Volume analysis
├── docs/
│   └── quant_research_findings.md   # Quantitative research document
├── web/                             # React dashboard (Vite + Tailwind)
├── logs/
│   └── executions.jsonl             # Trade execution log
├── profiles/default.json            # Default trading profile (~200 config fields)
├── Dockerfile                       # Multi-stage build (Node 22 → Go 1.24 → Alpine 3.20)
├── docker-compose.yml               # PostgreSQL + bot + auto-optimizer
├── go.mod                           # Go module (gorilla/websocket, godotenv, lib/pq)
└── .env.example                     # Environment template
```

## Development

### Run Tests

```bash
export PATH=$PATH:/usr/local/go/bin
go test ./...
```

### Dashboard Development

```bash
cd web
npm run dev
```

Vite dev server starts on port 5173 with hot reload. API requests proxy to `localhost:8080`.

## License

MIT
