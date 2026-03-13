# Momentum Trading Bot

Disclaimer: This code was written with AI and is a work in progress. Use only with paper-trading or you will lose money.

A modular momentum-trading system built in Go with a React operator dashboard.

The current implementation runs against live Alpaca infrastructure and persists trading telemetry to PostgreSQL.

Implemented backend flow:

- market data stream
- scanner filters for low-float momentum names
- breakout strategy entries
- stop-loss, profit-target, and trailing-stop exits
- risk checks before execution
- Alpaca order submission and fill polling
- PostgreSQL event persistence
- broker position bootstrap on startup
- position and PnL tracking
- operator controls and live dashboard updates

## Prerequisites

- Go 1.24+
- Node.js 20+
- PostgreSQL
- Alpaca account with market-data and trading API credentials

## Environment Setup

Create a local `.env` file from `.env.example` and fill in at least:

- `ALPACA_API_KEY`
- `ALPACA_API_SECRET`
- `DATABASE_URL`

Mode selection:

- Paper trading: set `ALPACA_PAPER=true`
- Live trading: set `ALPACA_PAPER=false` and `ALPACA_LIVE_TRADING_ENABLED=true`

The live-trading arm flag is intentional. The service refuses to start in live mode unless it is explicitly enabled.

Market-data hydration controls:

- `MARKET_DATA_HYDRATION_REQUESTS_PER_MIN` defaults to `120`
- `MARKET_DATA_HYDRATION_RETRY_SEC` defaults to `300`
- `MARKET_DATA_HYDRATION_QUEUE_SIZE` defaults to `512`

These are intentionally conservative. Alpaca documents `200 / min` historical market-data calls on the basic Trading API plan, and this bot uses a lower default to leave headroom for other API activity and avoid `429 Too Many Requests` responses.

Feed and plan detection:

- `ALPACA_DATA_FEED=auto` lets the bot probe Alpaca on startup
- Basic plans stay on `iex` and use a lower hydration budget
- AlgoTrader Plus upgrades to `sip` automatically and increases the hydration budget based on the detected `X-RateLimit-Limit` header

## Run Locally

```sh
cd momentum-trading-bot
go test ./...
cd web
npm install
npm run build
cd ..
go run ./...
```

The backend and embedded dashboard are served from http://localhost:8080.

## Run With Docker Compose

Create a local `.env` file first. At minimum it must include:

- `ALPACA_API_KEY`
- `ALPACA_API_SECRET`

The compose stack provisions PostgreSQL automatically and injects a container-safe `DATABASE_URL` for the app service.

Start the stack:

```sh
docker compose up --build
```

Run detached:

```sh
docker compose up --build -d
```

Stop the stack:

```sh
docker compose down
```

Remove the database volume too:

```sh
docker compose down -v
```

The stack exposes:

- app: http://localhost:8080
- postgres: localhost:5432

Operational endpoints:

- `GET /healthz` for process liveness
- `GET /readyz` for dependency readiness

Files added for containerized runtime:

- `Dockerfile` — multi-stage build for the web dashboard and Go server
- `docker-compose.yml` — app + PostgreSQL stack
- `.dockerignore` — trims build context

On startup the application will:

- load `.env`
- connect to PostgreSQL and auto-create persistence tables
- connect to Alpaca market data and trading APIs
- load existing broker positions into in-memory state
- serve the React dashboard and HTTP API

## Dashboard Features

- live status and PnL snapshot
- scanner candidates
- open positions
- closed trades
- structured logs
- pause trading
- resume trading
- close all positions
- emergency stop

The dashboard consumes `/api/dashboard` for the initial snapshot and `/ws` for live updates.

## API Endpoints

- `GET /api/status`
- `GET /healthz`
- `GET /readyz`
- `GET /api/positions`
- `GET /api/candidates`
- `GET /api/trades`
- `GET /api/logs`
- `GET /api/dashboard`
- `POST /api/pause`
- `POST /api/resume`
- `POST /api/close-all`
- `POST /api/emergency-stop`
- `GET /ws`

## Project Structure

- `internal/config` — shared runtime parameters
- `internal/alpaca` — Alpaca REST and WebSocket client
- `internal/domain` — shared trading models
- `internal/runtime` — pause, emergency stop, candidates, logs
- `internal/market` — live Alpaca market-data normalization
- `internal/scanner` — scanner rule evaluation
- `internal/strategy` — breakout entries and managed exits
- `internal/risk` — trade gating and account limits
- `internal/execution` — live Alpaca order execution
- `internal/portfolio` — positions, exposure, PnL, trade history
- `internal/storage` — PostgreSQL event recorder and schema bootstrap
- `internal/api` — REST and WebSocket dashboard API
- `web` — React dashboard source and build output

## Notes

- Live market data is consumed from Alpaca stock WebSockets using bars, updated bars, and trading-status events.
- Snapshot and premarket-volume hydration are rate-limited through a single queue so the bot stays under Alpaca market-data limits during wildcard subscriptions.
- Orders are submitted to Alpaca with market orders and then polled until filled, rejected, canceled, expired, or timed out.
- PostgreSQL persists logs, scanner candidates, execution reports, closed trades, and periodic dashboard snapshots.
- Startup now fails fast if PostgreSQL is unreachable, Alpaca credentials are invalid, or live trading is selected without the explicit arming flag.
- If you run with `ALPACA_STREAM_ALL=true`, wildcard streaming can still produce more candidate symbols than the hydration queue can enrich on a basic plan. For tighter scanner feedback, prefer an explicit `ALPACA_SYMBOLS` list.
