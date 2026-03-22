# Momentum Trading Bot

A modular momentum-trading system built in Go with a React operator dashboard. Supports long and short positions with regime-aware strategy selection, full quant finance stack (Kelly sizing, volatility-target sizing, correlation gates, market impact modeling, factor analysis, Monte Carlo simulation, bootstrap significance testing, walk-forward analysis, CPCV, Bayesian optimization), backtesting, optimization, and live paper/real trading via the Alpaca Markets API.

> **Alpaca paid subscription required** — the bot uses the SIP data feed for real-time and historical market data.

## Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│  React Dashboard (Vite + Tailwind + Recharts)                        │
│  Overview · Positions · Scanner · Trades · Logs · Controls           │
└───────────────────────────┬──────────────────────────────────────────┘
                            │ REST + WebSocket
┌───────────────────────────▼──────────────────────────────────────────┐
│  Go Backend                                                          │
│                                                                      │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────────┐         │
│  │ Scanner  │→ │ Strategy │→ │   Risk   │→ │  Execution  │         │
│  └──────────┘  └──────────┘  └──────────┘  └─────────────┘         │
│                                                                      │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────────┐         │
│  │ Regime   │  │Portfolio │  │ Backtest │  │  Optimizer  │         │
│  │(HMM +   │  │ Manager  │  │ Engine   │  │ (LHS +     │         │
│  │threshold)│  │          │  │          │  │  Bayesian) │         │
│  └──────────┘  └──────────┘  └──────────┘  └─────────────┘         │
│                                                                      │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────────┐         │
│  │Analytics │  │   ML     │  │Volatility│  │ Correlation │         │
│  │(Factor   │  │(Scoring +│  │Estimator │  │  Tracker   │         │
│  │Decomp.)  │  │MetaLabel)│  │          │  │            │         │
│  └──────────┘  └──────────┘  └──────────┘  └─────────────┘         │
│                                                                      │
│  ┌──────────┐  ┌──────────┐                                         │
│  │ Impact   │  │Walk-Fwd /│                                         │
│  │ Model    │  │  CPCV    │                                         │
│  └──────────┘  └──────────┘                                         │
└──────────────────────────────────────────────────────────────────────┘
                            │
                 ┌──────────▼───────────┐
                 │  Alpaca Markets API  │
                 │  (Streaming + Hist.) │
                 └──────────────────────┘
```

## Features

### Trading Engine
- **Momentum Scanner** — gap filter, price filter, relative volume, premarket volume, volume rate, VWAP distance
- **Dual-Direction Strategy** — long breakouts/pullbacks, short breakdowns
- **Four Playbook Types** — Breakout, Pullback, Continuation, Reversal — each with its own exit parameters
- **Market Regime Detection** — threshold-based (default) and HMM regime detector
- **Confidence-Based Entry Scoring** — with regime gating

### Risk Management (Phase 2)
- Portfolio heat tracking with alert thresholds
- Graduated daily loss response (moderate / severe / halt tiers)
- Sector concentration limits (max positions + exposure per sector)
- Correlation-aware position approval
- Kelly Criterion position sizing
- Volatility-target position sizing
- Drawdown-based risk reduction (linear scale to max acceptable drawdown)
- Per-minute entry throttle (`MaxEntriesPerMinute`)

### Trade Management (Phase 3)
- RSI overbought/oversold filter
- Time-of-day adaptive parameters
- Partial exit framework (two trigger levels with configurable percentages)
- Adaptive trailing stops with volatility factor
- Mean-reversion overlay (Bollinger bands + ADX filter)
- Percentage-based slippage model (liquid / mid / illiquid tiers)

### Backtesting & Validation (Phase 4)
- Backtest engine with streaming bar iterator (memory efficient)
- Monte Carlo simulation (configurable number of sims)
- Bootstrap significance testing
- Transaction cost model (commissions, SEC/TAF fees, spread costs)
- Deflated Sharpe Ratio
- Walk-forward analysis (in-sample / out-of-sample with purge gap)
- CPCV — Combinatorial Purged Cross-Validation

### Optimization
- Latin Hypercube Sampling (LHS) grid search
- Bayesian optimization with Gaussian Process surrogate + Expected Improvement
- Sensitivity analysis
- Three strategy profiles: `baseline_breakout`, `high_conviction_breakout`, `continuation_breakout`
- Optimizer artifact output with promotion workflow (paper → live)

### Quant Finance (Phase 5)
- HMM regime detection with Forward algorithm and Baum-Welch training
- Factor analysis (Fama-French-style return decomposition)
- Almgren-Chriss market impact model
- ML scoring stubs (disabled by default — require trained models)
- Meta-labeling stubs (disabled by default)

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
- `POST /api/pause` — Pause trading
- `POST /api/resume` — Resume trading
- `POST /api/close-all` — Close all positions
- `POST /api/emergency-stop` — Emergency stop
- `GET  /ws` — Real-time dashboard updates
- `GET  /healthz` — Liveness probe (public)
- `GET  /readyz` — Readiness probe (public)

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

This starts PostgreSQL + the bot. The `.cache` directory is mounted as a volume so cached data persists across container restarts. Dashboard at `http://localhost:8080`.

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
| `TRADING_PROFILE_PATH` | Path to JSON trading profile | bundled `profiles/default.json` |

> Alpaca paid subscription is required for the SIP data feed. The live-trading arming flag is intentional — the bot refuses to start in live mode unless `ALPACA_LIVE_TRADING_ENABLED=true`.

Control-plane access:
- The dashboard, `/api/*`, and `/ws` require HTTP Basic auth (username: `operator`, password: `CONTROL_PLANE_AUTH_TOKEN`)
- `GET /healthz` and `GET /readyz` stay public for probes

### Trading Profile

The bot uses versioned JSON trading profiles stored in `profiles/`. Three strategy profiles are supported:

| Profile | Description |
|---|---|
| `baseline_breakout` | Default balanced profile |
| `high_conviction_breakout` | Higher conviction, fewer trades |
| `continuation_breakout` | Continuation-focused entries |

- Profile loaded from `TRADING_PROFILE_PATH` env var or bundled `profiles/default.json`
- `TuneTradingConfig()` fills any missing fields with sensible defaults based on broker equity and plan limits
- See `profiles/default.json` for the full ~125 config fields

### Key Config Categories

**Core Risk** — `RiskPerTradePct`, `DailyLossLimitPct`, `MaxTradesPerDay`, `MaxOpenPositions`, `MaxExposurePct`, `MaxEntriesPerMinute`

**Scanner** — `MinPrice`, `MaxPrice`, `MinGapPercent`, `MinRelativeVolume`, `MinPremarketVolume`, `MinATRBars`

**Trade Management** — `TrailActivationR`, `ProfitTargetR`, `PartialExitsEnabled`, `EntryStopATRMultiplier`, `TrailATRMultiplier`, `TightTrailTriggerR`

**Quant Features** — enable/disable flags for each Phase 2-5 feature: `EnableMarketRegime`, `KellySizingEnabled`, `VolTargetSizingEnabled`, `CorrelationCheckEnabled`, `FactorAnalysisEnabled`, `ImpactModelEnabled`, `HMMRegimeEnabled`

**Optimization** — `OptimizerSamples`, `OptimizerUseLHS`, `BayesianOptEnabled`, `WalkForwardEnabled`, `CPCVEnabled`

See `profiles/default.json` for the complete field reference.

## Project Structure

```
├── main.go                          # Entry point + optimize command
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
│   ├── backtest/                    # Backtesting & validation
│   │   ├── engine.go                # Core backtest engine
│   │   ├── iterator.go              # Bar iterator interface
│   │   ├── bootstrap.go             # Bootstrap significance testing
│   │   ├── cpcv.go                  # Combinatorial Purged Cross-Validation
│   │   ├── costs.go                 # Transaction cost model
│   │   ├── dsr.go                   # Deflated Sharpe Ratio
│   │   ├── montecarlo.go            # Monte Carlo simulation
│   │   └── walkforward.go           # Walk-forward analysis
│   ├── config/                      # Configuration
│   │   ├── app.go                   # Environment config
│   │   ├── backtest.go              # Backtest run config
│   │   ├── config.go                # TradingConfig (~125 fields)
│   │   ├── profile.go               # Profile loading/saving
│   │   └── tuning.go                # Auto-tuning defaults
│   ├── domain/                      # Core types
│   │   ├── types.go                 # Domain models
│   │   ├── trading.go               # Intent/side/direction helpers
│   │   └── regime.go                # Regime types
│   ├── execution/                   # Order execution
│   │   ├── execution.go             # Alpaca order execution
│   │   └── impact.go                # Almgren-Chriss impact model
│   ├── market/normalizer.go         # Tick normalization
│   ├── markethours/hours.go         # ET market hours + holidays
│   ├── ml/                          # Machine learning stubs
│   │   ├── scorer.go                # ML scoring (disabled by default)
│   │   └── metalabel.go             # Meta-labeling (disabled by default)
│   ├── optimizer/                   # Parameter optimization
│   │   ├── optimizer.go             # LHS grid search + orchestration
│   │   ├── bayesian.go              # Bayesian optimization (GP + EI)
│   │   ├── sensitivity.go           # Sensitivity analysis
│   │   └── artifacts.go             # Optimizer output artifacts
│   ├── portfolio/manager.go         # Position tracking & PnL
│   ├── regime/                      # Market regime detection
│   │   ├── tracker.go               # Threshold-based regime tracker
│   │   └── hmm.go                   # HMM regime detector
│   ├── risk/                        # Risk management
│   │   ├── risk.go                  # Risk engine (all gates)
│   │   ├── correlation.go           # Correlation tracker
│   │   └── volatility.go            # Volatility estimator
│   ├── runtime/state.go             # Runtime state management
│   ├── scanner/scanner.go           # Momentum scanner
│   ├── sector/lookup.go             # Sector classification
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
├── web/                             # React dashboard (Vite + Tailwind)
├── profiles/default.json            # Default trading profile (~125 config fields)
├── Dockerfile                       # Multi-stage build (Node → Go → Alpine)
├── docker-compose.yml               # PostgreSQL + bot + cache volume
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
