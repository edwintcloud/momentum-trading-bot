# Momentum Trading Bot

A modular momentum-trading system built in Go with a React operator dashboard. The full quant stack includes intraday alpha signals (OFI, VPIN, OBV, ORB, dollar bars), VaR/CVaR risk management, GARCH volatility forecasting, mean-variance optimization, risk parity portfolio construction, VWAP/TWAP execution algorithms, an ML inference pipeline with meta-labeling and ensemble scoring, a weekly auto-optimizer with guardrails and hot-reload, backtesting with walk-forward/CPCV validation, and live paper/real trading via the Alpaca Markets API.

> **Alpaca paid subscription required** — the bot uses the SIP data feed for real-time and historical market data.

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
│  │OBV, ORB) │  │Ensemble +│  │threshold)│  │          │  │Bayesian) │     │
│  │          │  │MetaLabel)│  │          │  │          │  │          │     │
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
- **Scanner Filters** — price, float (`MaxFloat`/`MinFloat`), HOD proximity, RSI overbought/oversold
- **Dual-Direction Strategy** — long breakouts/pullbacks, short breakdowns
- **Four Playbook Types** — Breakout, Pullback, Continuation, Reversal — each with its own exit parameters
- **Market Regime Detection** — threshold-based (default) and HMM regime detector
- **Confidence-Based Entry Scoring** — with regime gating and ML score integration
- **Improved Diagnostics** — candidate rejection reasons include `market-closed`, `regime-gated`, `past-entry-deadline`, `cooldown`, `same-side-today`, `loss-cooldown` (replaces generic `no-signal`)

### Risk Management
- Portfolio heat tracking with alert thresholds
- Graduated daily loss response (moderate / severe / halt tiers)
- Sector concentration limits (max positions + exposure per sector)
- Correlation-aware position approval
- Kelly Criterion position sizing
- Volatility-target position sizing
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
- **VWAP execution** — volume-profile-weighted order slicing
- **TWAP execution** — equal time-slice distribution
- **Adaptive limit pricing** — auto-widening with max slippage control
- **Execution router** — auto-selects VWAP/TWAP/direct based on order size
- Almgren-Chriss market impact model
- Percentage-based slippage model (liquid / mid / illiquid tiers)

### Machine Learning Pipeline
- **Feature engineering** — 20+ causal, stationary features
- **Fractional differentiation** for stationarity preservation
- **Pure-Go inference engine** (linear models + gradient-boosted stumps)
- **Meta-labeling** with triple barrier method (wired into strategy)
- **ML scoring** with confidence threshold (wired into strategy)
- **Ensemble methods** (equal weight, IR-weighted, regime-conditional)
- **Concept drift detection** (PSI + rolling Sharpe decay)

### Trade Management
- RSI overbought/oversold filter
- Time-of-day adaptive parameters with configurable midday score multiplier
- **Entry deadline** — block new entries after configurable minutes from open (e.g., 120 min = 2 hours)
- **Risk/reward pre-check** — reject trades where estimated reward < configurable R:R ratio × risk
- Partial exit framework (two trigger levels with configurable percentages)
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
- Four strategy profiles: `baseline_breakout`, `high_conviction_breakout`, `continuation_breakout`, `momentum_cameron`
- Optimizer artifact output with promotion workflow (paper → live)

### Auto-Optimizer
- Weekly automatic optimization (Saturday 6 AM ET)
- Guardrail validation (Sharpe, win rate, drawdown, trade count, DSR, improvement)
- Automatic profile promotion with atomic file writes and backups
- Hot-reload — live bot picks up new profile without restart
- Telegram notifications for optimizer events

### Dashboard
- **Overview** — Account summary, PnL chart, regime status, recent activity
- **Positions** — Live positions with unrealized PnL, side indicators, and exit status
- **Scanner** — Real-time candidate scoring with momentum metrics
- **Trades** — Closed trade history with PnL, duration, and setup type
- **Logs** — Filterable system log viewer
- **Controls** — Pause/resume, emergency stop, close all positions

### API
- `GET  /api/dashboard` — Full dashboard snapshot
- `GET  /api/status` — System status
- `GET  /api/positions` — Open positions
- `GET  /api/candidates` — Scanner candidates
- `GET  /api/trades` — Trade history
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

The auto-optimizer runs automatically as a sidecar in Docker Compose with the `-now` flag, so it runs an immediate optimization on startup and then continues on the weekly schedule. Both the bot and optimizer share the `profiles/` volume, so promoted profiles are picked up via hot-reload.

```bash
docker compose up -d  # starts postgres + bot + auto-optimizer
```

## Telegram Notifications

The auto-optimizer can send notifications to a Telegram chat when it starts, completes, promotes a profile, or rejects a candidate.

### Setup

1. Create a Telegram bot via [@BotFather](https://t.me/BotFather) and get the bot token
2. Send a message to your bot, then get your chat ID from `https://api.telegram.org/bot<TOKEN>/getUpdates`
3. Add to `.env`:
   ```
   TELEGRAM_BOT_TOKEN=123456:ABC-DEF...
   TELEGRAM_CHAT_ID=987654321
   ```

Notifications are optional — if the env vars are not set, the optimizer runs silently (logs only).

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

# Clear bar cache
go run . backtest -start 2025-01-01 -clear-cache
```

> The backtest and optimize commands automatically fetch historical data from Alpaca. The `-symbols` flag is **not** required — the system discovers symbols automatically. Data is cached to `.cache/bars/` for fast subsequent runs.

### Docker

```bash
cp .env.example .env
# Edit .env with your Alpaca API keys

docker compose up -d
```

This starts PostgreSQL + the bot + the auto-optimizer. The `.cache` directory is mounted as a volume so cached data persists across container restarts. The auto-optimizer runs as a sidecar with `-now` for an immediate first optimization, then continues on the weekly schedule. It shares the `profiles/` volume with the bot for seamless hot-reload. Dashboard at `http://localhost:8080`.

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
| `FLOAT_DATA_URL` | URL or file path to CSV with `symbol,float` per line | (optional) |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token for notifications | (optional) |
| `TELEGRAM_CHAT_ID` | Telegram chat ID for notifications | (optional) |

> Alpaca paid subscription is required for the SIP data feed. The live-trading arming flag is intentional — the bot refuses to start in live mode unless `ALPACA_LIVE_TRADING_ENABLED=true`.

Control-plane access:
- The dashboard, `/api/*`, and `/ws` require HTTP Basic auth (username: `operator`, password: `CONTROL_PLANE_AUTH_TOKEN`)
- `GET /healthz` and `GET /readyz` stay public for probes

### Trading Profile

The bot uses versioned JSON trading profiles stored in `profiles/`. Four strategy profiles are supported:

| Profile | Description |
|---|---|
| `baseline_breakout` | Default balanced profile |
| `high_conviction_breakout` | Higher conviction, fewer trades |
| `continuation_breakout` | Continuation-focused entries |
| `momentum_cameron` | Ross Cameron-inspired momentum day trading (long-only, strict filters) |

- Profile loaded from `TRADING_PROFILE_PATH` env var or bundled `profiles/default.json`
- `TuneTradingConfig()` fills any missing fields with sensible defaults based on broker equity and plan limits
- See `profiles/default.json` for the full ~125 config fields

### Key Config Categories

**Core Risk** — `RiskPerTradePct`, `DailyLossLimitPct`, `MaxTradesPerDay`, `MaxOpenPositions`, `MaxExposurePct`, `MaxEntriesPerMinute`

**Scanner** — `MinPrice`, `MaxPrice`, `MinGapPercent`, `MinRelativeVolume`, `MinPremarketVolume`, `MinATRBars`, `MaxFloat`, `MinFloat`, `FloatOverrideURL`

**Trade Management** — `TrailActivationR`, `ProfitTargetR`, `PartialExitsEnabled`, `EntryStopATRMultiplier`, `TrailATRMultiplier`, `TightTrailTriggerR`, `EntryDeadlineMinutesAfterOpen`, `MinRiskRewardRatio`, `MidDayScoreMultiplier`

**Scanner Quality** — `MaxDistanceFromHighPct`, `VolumeOnPullbackEnabled`

**HOD Momo Scanner** — `HODMomoEnabled` (default: false), `HODMomoMinIntradayPct` (10%), `HODMomoMinRelativeVolume` (5x), `HODMomoMaxDistFromHigh` (5%), `HODMomoMinMinutesSinceOpen` (5 min). Enabled in `momentum_cameron` profile.

**Quant Features** — enable/disable flags for each feature: `EnableMarketRegime`, `KellySizingEnabled`, `VolTargetSizingEnabled`, `CorrelationCheckEnabled`, `FactorAnalysisEnabled`, `ImpactModelEnabled`, `HMMRegimeEnabled`

**Optimization** — `OptimizerSamples`, `OptimizerUseLHS`, `BayesianOptEnabled`, `WalkForwardEnabled`, `CPCVEnabled`

See `profiles/default.json` for the complete field reference.

### Momentum Cameron Profile

The `momentum_cameron` profile implements Ross Cameron's momentum day trading methodology combined with the bot's quant infrastructure. It is designed for small accounts ($25k) focused on high-probability intraday momentum trades.

**Key differences from `baseline_breakout`:**
- **Strict stock selection** — MaxPrice $20 (vs $200), MinRelativeVolume 5x (vs 2x), MinGapPercent 5% (vs 3%), float filter (500k–20M shares)
- **Long-only** — shorts disabled; Cameron's edge is exclusively long-biased
- **Morning-only** — entry deadline 120 minutes after open; midday score multiplier 2x
- **Conservative risk** — 1% risk per trade, max 6 trades/day, max 3 open positions, 2:1 minimum R:R requirement
- **Tighter exits** — Breakout target 2.5R (vs 4.0R), faster trailing stops, partial exits at 1R (50%) and 2R (25%)
- **HOD Momo scanner enabled** — catches intraday momentum runners that gap small but run 50-100%+ from open (e.g., ANNA 2026-03-20)
- **Momentum signals enabled** — OFI, VPIN, ORB, OBV divergence for order flow confirmation
- **Portfolio construction disabled** — MVO, risk parity, factor-neutral off (not applicable for 1–3 position momentum)
- **ML disabled** — until models are trained on momentum-specific data

**Usage:**
```bash
TRADING_PROFILE_PATH=profiles/momentum_cameron.json go run . live
```

**Recommended for:** small accounts ($25k), momentum day trading, paper trading validation before live deployment.

## Project Structure

```
├── main.go                          # Entry point + CLI commands
├── backtest_command.go              # Backtest CLI command
├── backtest_dataset_iterator.go     # Streaming bar iterator
├── backtest_fetch.go                # Alpaca historical data fetcher
├── historical_cache_codec.go        # Bar cache read/write
├── profile_runtime.go               # Runtime profile management
├── internal/
│   ├── alpaca/                      # Alpaca Markets integration
│   │   ├── client.go                # REST client
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
│   │   ├── config.go                # TradingConfig (~125 fields)
│   │   ├── profile.go               # Profile loading/saving
│   │   ├── tuning.go                # Auto-tuning defaults
│   │   └── watcher.go               # File watcher for hot-reload
│   ├── domain/                      # Core types
│   │   ├── types.go                 # Domain models
│   │   ├── trading.go               # Intent/side/direction helpers
│   │   └── regime.go                # Regime types
│   ├── execution/                   # Order execution
│   │   ├── execution.go             # Alpaca order execution
│   │   ├── impact.go                # Almgren-Chriss impact model
│   │   ├── vwap.go                  # VWAP execution algorithm
│   │   ├── twap.go                  # TWAP execution algorithm
│   │   ├── adaptivelimit.go         # Adaptive limit pricing
│   │   └── router.go                # Execution router (auto-select algo)
│   ├── market/normalizer.go         # Tick normalization
│   ├── markethours/hours.go         # ET market hours + holidays
│   ├── ml/                          # Machine learning pipeline
│   │   ├── features.go              # Feature engineering (20+ features)
│   │   ├── fracdiff.go              # Fractional differentiation
│   │   ├── training.go              # Pure-Go inference engine
│   │   ├── scorer.go                # ML scoring with confidence threshold
│   │   ├── metalabel.go             # Meta-labeling (triple barrier)
│   │   ├── ensemble.go              # Ensemble methods (equal/IR/regime)
│   │   └── drift.go                 # Concept drift detection (PSI + Sharpe)
│   ├── optimizer/                   # Parameter optimization
│   │   ├── optimizer.go             # LHS grid search + orchestration
│   │   ├── bayesian.go              # Bayesian optimization (GP + EI)
│   │   ├── sensitivity.go           # Sensitivity analysis
│   │   └── artifacts.go             # Optimizer output artifacts
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
│   │   └── composite.go             # Composite logger
│   └── volumeprofile/profile.go     # Volume analysis
├── docs/
│   └── quant_research_findings.md   # Quantitative research document
├── web/                             # React dashboard (Vite + Tailwind)
├── profiles/default.json            # Default trading profile (~125 config fields)
├── Dockerfile                       # Multi-stage build (Node → Go → Alpine)
├── docker-compose.yml               # PostgreSQL + bot + auto-optimizer
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
