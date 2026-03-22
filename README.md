# Momentum Trading Bot

A modular momentum trading system built in Go with a React dashboard. Supports both long and short positions with regime-aware strategy selection, walk-forward optimization, and backtesting.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  React Dashboard (Vite + Tailwind + Recharts)               │
│  Overview · Positions · Scanner · Trades · Logs · Controls  │
└────────────────────────┬────────────────────────────────────┘
                         │ REST + WebSocket
┌────────────────────────▼────────────────────────────────────┐
│  Go Backend                                                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────────┐  │
│  │ Scanner  │→│ Strategy  │→│   Risk    │→│ Execution   │  │
│  └──────────┘  └──────────┘  └──────────┘  └────────────┘  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────────┐  │
│  │ Regime   │  │Portfolio  │  │ Backtest │  │ Optimizer   │  │
│  └──────────┘  └──────────┘  └──────────┘  └────────────┘  │
└─────────────────────────────────────────────────────────────┘
                         │
              ┌──────────▼──────────┐
              │  Alpaca Markets API  │
              └─────────────────────┘
```

## Features

### Trading Engine
- **Momentum Scanner** — Scores candidates by price velocity, volume surge, and spread quality
- **Dual-Direction Strategy** — Long breakouts and short breakdowns with trailing stops
- **Market Regime Detection** — Benchmark-driven regime tracking (trending, mean-reverting, volatile, calm)
- **Adaptive Risk Management** — Position sizing, daily loss limits, max exposure, and short-specific limits
- **Volume Profile Analysis** — Point of control, value area, and volume distribution
- **Auto-Tuning** — Dynamic config adjustment based on account equity and daily PnL

### Strategy Profiles
Three built-in profiles for different market conditions:
- `conservative` — Lower risk, tighter stops, fewer positions
- `moderate` (default) — Balanced risk/reward
- `aggressive` — Higher risk tolerance, wider stops, more positions

### Backtesting & Optimization
- **Backtest Engine** — Replay historical data with full strategy simulation
- **Performance Metrics** — Sharpe ratio, Sortino ratio, max drawdown, win rate, profit factor
- **Walk-Forward Optimization** — In-sample/out-of-sample parameter optimization
- **Grid Search** — Systematic exploration of parameter space

### Dashboard
- **Overview** — Account summary, PnL chart, regime status, recent activity
- **Positions** — Live positions with unrealized PnL, side indicators, and exit status
- **Scanner** — Real-time candidate scoring with momentum metrics
- **Trades** — Closed trade history with PnL, duration, and setup type
- **Logs** — Filterable system log viewer
- **Controls** — Pause/resume, emergency stop, profile switching, force close all

### API
- `GET  /api/dashboard` — Full dashboard snapshot
- `GET  /api/positions` — Open positions
- `GET  /api/closedtrades` — Trade history
- `GET  /api/candidates` — Scanner candidates
- `GET  /api/performance` — Performance metrics
- `GET  /api/config` — Current configuration
- `POST /api/pause` — Pause trading
- `POST /api/resume` — Resume trading
- `POST /api/stop` — Emergency stop
- `POST /api/closeall` — Close all positions
- `POST /api/profile/switch` — Switch strategy profile
- `WS   /ws` — Real-time dashboard updates

## Quick Start

### Prerequisites
- Go 1.22+
- Node.js 20+
- PostgreSQL 15+ (optional — falls back to filesystem storage)
- [Alpaca Markets](https://alpaca.markets/) account (paper or live)

### 1. Clone and configure

```bash
git clone https://github.com/edwintcloud/momentum-trading-bot.git
cd momentum-trading-bot
cp .env.example .env
# Edit .env with your Alpaca API keys
```

### 2. Build the dashboard

```bash
cd web
npm install
npm run build
cd ..
```

### 3. Run

```bash
# Live trading mode
go run . live

# Backtest — fetch bars from Alpaca (auto-cached to .cache/bars/)
go run . backtest -start 2025-01-01 -end 2025-06-01 -symbols AAPL,TSLA,NVDA,META,SPY

# Backtest — with 5-minute bars
go run . backtest -start 2025-01-01 -end 2025-06-01 -symbols AAPL -timeframe 5Min

# Backtest — from local CSV (legacy)
go run . backtest -start 2025-01-01 -end 2025-06-01 -data path/to/bars.csv

# Walk-forward optimization — fetch from Alpaca
go run . optimize -as-of 2025-06-01 -symbols AAPL,TSLA,NVDA,META,SPY

# Walk-forward optimization — from local CSV
go run . optimize -as-of 2025-06-01 -data path/to/bars.csv

# Clear the bar cache
go run . backtest -start 2025-01-01 -symbols AAPL -clear-cache
```

The dashboard will be available at `http://localhost:8080`.

### Historical Data Caching

When fetching bars from Alpaca, the system automatically caches results to `.cache/bars/` as CSV files. Subsequent runs with the same symbol, timeframe, and date range load instantly from cache without hitting the API. Use `-clear-cache` to force a fresh fetch, or `-cache /path/to/dir` to use a custom cache directory.

### Docker

```bash
cp .env.example .env
# Edit .env with your Alpaca API keys

docker compose up -d
```

This starts the bot and a PostgreSQL instance. Dashboard at `http://localhost:8080`.

## Configuration

### Environment Variables

| Variable | Description | Default |
|---|---|---|
| `ALPACA_API_KEY` | Alpaca API key | (required) |
| `ALPACA_API_SECRET` | Alpaca API secret | (required) |
| `ALPACA_PAPER` | Use paper trading | `true` |
| `ALPACA_LIVE_TRADING_ENABLED` | Enable live trading | `false` |
| `ALPACA_SYMBOLS` | Comma-separated symbol list | (all) |
| `DATABASE_URL` | PostgreSQL connection string | (filesystem fallback) |
| `CONTROL_PLANE_AUTH_TOKEN` | API auth token | `changeme` |
| `TRADING_PROFILE_PATH` | Path to JSON trading profile | (default config) |
| `LISTEN_ADDR` | HTTP listen address | `:8080` |

### Trading Profiles

Custom profiles are JSON files that override default trading parameters:

```json
{
  "name": "conservative",
  "version": "1.0",
  "description": "Lower risk profile for volatile markets",
  "config": {
    "maxOpenPositions": 5,
    "maxTradesPerDay": 10,
    "dailyLossLimitPct": 0.01,
    "maxPositionSizePct": 0.08
  }
}
```

See `profiles/default.json` for a full example.

## Project Structure

```
├── main.go                     # Entry point (live/backtest/optimize)
├── internal/
│   ├── alpaca/client.go        # Alpaca REST client
│   ├── api/server.go           # REST + WebSocket API
│   ├── backtest/               # Backtesting engine
│   │   ├── engine.go
│   │   └── iterator.go
│   ├── config/                 # Configuration management
│   │   ├── app.go              # App-level env config
│   │   ├── config.go           # Trading parameters
│   │   ├── profile.go          # Strategy profiles
│   │   └── tuning.go           # Auto-tuning logic
│   ├── domain/                 # Core types
│   │   ├── types.go            # All domain types
│   │   ├── trading.go          # Trading interfaces
│   │   └── regime.go           # Regime types
│   ├── execution/execution.go  # Order execution
│   ├── market/engine.go        # Market data normalization
│   ├── markethours/hours.go    # Market hours utility
│   ├── optimizer/optimizer.go  # Walk-forward optimization
│   ├── portfolio/manager.go    # Position management & PnL
│   ├── regime/tracker.go       # Regime detection
│   ├── risk/risk.go            # Risk management
│   ├── runtime/state.go        # Runtime state (pause/stop/logs)
│   ├── scanner/scanner.go      # Momentum scanner
│   ├── storage/                # Data persistence
│   │   ├── postgres.go         # PostgreSQL storage
│   │   └── filesystem.go       # Filesystem fallback
│   ├── strategy/               # Entry/exit logic
│   │   ├── strategy.go
│   │   └── tradeplan.go
│   ├── telemetry/logger.go     # Event recording
│   └── volumeprofile/profile.go # Volume analysis
├── web/                        # React dashboard
│   ├── src/
│   │   ├── App.jsx             # Main app with routing
│   │   ├── components/         # Reusable UI components
│   │   ├── hooks/              # useWebSocket hook
│   │   ├── lib/                # Formatting utilities
│   │   └── pages/              # Dashboard pages
│   └── ...
├── profiles/                   # Trading profile presets
├── Dockerfile                  # Multi-stage build
├── docker-compose.yml          # Full stack with PostgreSQL
└── .env.example                # Environment template
```

## Development

### Dashboard Development

```bash
cd web
npm run dev
```

Vite dev server starts on port 5173 with hot reload. API requests proxy to `localhost:8080`.

### Adding a New Strategy Profile

1. Create a JSON file in `profiles/`
2. Set `TRADING_PROFILE_PATH` to your file
3. Or switch at runtime via `POST /api/profile/switch`

## License

MIT
