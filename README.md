# Momentum Trading Bot

![Mr. Alpaca](logo.png "Mr. Alpaca")

Disclaimer: This code was written with AI and is a work in progress. Use only with paper-trading or you will lose money.

A modular momentum-trading system built in Go with a React operator dashboard.

The current implementation runs against live Alpaca infrastructure and persists trading telemetry to PostgreSQL.

Implemented backend flow:

- market data stream
- scanner filters for low-float momentum names
- feature-enriched breakout entries
- stop-loss and trailing-stop exits
- risk checks before execution
- Alpaca order submission and fill polling
- PostgreSQL event persistence
- broker position bootstrap on startup
- position and PnL tracking
- operator controls and live dashboard updates
- CSV-driven historical backtesting

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
- `CONTROL_PLANE_AUTH_TOKEN`
- `POSTGRES_DB`, `POSTGRES_USER`, and `POSTGRES_PASSWORD` if you are using `docker compose`

Mode selection:

- Paper trading: set `ALPACA_PAPER=true`
- Live trading: set `ALPACA_PAPER=false` and `ALPACA_LIVE_TRADING_ENABLED=true`

The live-trading arm flag is intentional. The service refuses to start in live mode unless it is explicitly enabled.

Optional overrides:

- `ALPACA_SYMBOLS=AAPL,TSLA` limits the stream to a watchlist; leaving it empty subscribes broadly

Control-plane access:

- The dashboard, `/api/*`, and `/ws` require HTTP Basic auth
- Username is always `operator`
- Password is the value of `CONTROL_PLANE_AUTH_TOKEN`
- `GET /healthz` and `GET /readyz` stay public for probes

Everything else is now inferred automatically. On startup the bot probes Alpaca, detects the market-data plan/feed, reads broker equity, and tunes risk/scanner settings toward conservative momentum trading defaults.

What the bot auto-tunes from Alpaca:

- data feed and historical hydration budget
- starting capital and broker-backed day PnL
- per-trade risk, daily loss limit, max open positions, and max exposure
- scanner thresholds for price, gap, relative volume, and premarket volume
- trailing-stop activation and entry-timing thresholds

## Run Locally

```sh
cd momentum-trading-bot
go test ./...
cd web
npm install
npm run build
cd ..
go run .
```

The backend and embedded dashboard are served from http://localhost:8080.

## Run A Backtest

Backtests replay historical minute bars through the same scanner, strategy, risk, and portfolio components used by live mode.

Default behavior:

- Alpaca is the default historical data source
- `-start` is the only required argument
- `-end` defaults to the current time
- there is no separate training or model-fitting step; the backtest replays the requested window directly
- the symbol universe defaults to Alpaca's active tradable US equities, which approximates the live wildcard scanner
- historical Alpaca fetches are cached automatically under `.cache/backtest/historical-bars` so repeat and overlapping runs reuse previously downloaded bar batches

Example:

```sh
go run . backtest -start 2026-03-10
```

Explicit end time:

```sh
go run . backtest -start 2026-03-10 -end 2026-03-31
```

Optional CSV fallback:

```sh
go run . backtest -data .\\data\\bars.csv -start 2026-03-10 -end 2026-03-31
```

Required CSV columns:

- `timestamp`
- `symbol`
- `open`
- `high`
- `low`
- `close`
- `volume`

Optional CSV columns:

- `prev_close`
- `catalyst`
- `catalyst_url`

## Run The Optimizer

The weekly optimizer is manual. It does not run automatically, and it does not auto-promote a strategy into live trading.

Use it to generate a ranked research report and a versioned candidate trading profile:

```sh
go run . optimize -as-of 2026-03-20
```

Optional flags:

- `-data /absolute/path/to/bars.csv` uses CSV bars instead of fetching Alpaca history
- `-out /absolute/path/to/output-dir` changes the artifact directory; default is `.cache/optimizer`

What the optimizer does:

- builds the most recent 20 completed trading weeks ending at the prior Friday close
- splits them into `12 weeks search`, `4 weeks validation`, and `4 weeks holdout`
- searches the supported strategy families and bounded config ranges
- writes a versioned JSON report and a recommended trading profile artifact

Artifacts written by default:

- `.cache/optimizer/latest-report.json`
- `.cache/optimizer/latest-candidate-profile.json`
- `.cache/optimizer/reports/...`
- `.cache/optimizer/profiles/...`

Promotion behavior:

- the optimizer always writes the top-ranked recommendation
- the recommendation includes a promotion status such as `pending-paper-validation` or `blocked-research-gates`
- the repo ships with a bundled default profile at `profiles/default.json`
- live startup and backtests load that bundled profile automatically unless `TRADING_PROFILE_PATH` explicitly points somewhere else
- Docker Compose pins `TRADING_PROFILE_PATH` to `/app/profiles/default.json` inside the container image

To start the bot with a selected profile:

```sh
TRADING_PROFILE_PATH=/absolute/path/to/.cache/optimizer/profiles/<version>.json go run .
```

Recommended operator workflow:

1. Run `go run . optimize -as-of YYYY-MM-DD`
2. Review `.cache/optimizer/latest-report.json`
3. If the candidate is acceptable, deploy the generated profile in paper mode first
4. Replace the bundled repo profile or point `TRADING_PROFILE_PATH` at the selected profile and restart the bot

Dashboard visibility:

- the operator dashboard shows the active profile/version
- it also shows the latest pending candidate profile, last optimizer run time, and paper-validation status

## Run With Docker Compose

Create a local `.env` file first. At minimum it must include:

- `ALPACA_API_KEY`
- `ALPACA_API_SECRET`
- `CONTROL_PLANE_AUTH_TOKEN`

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
- Orders are submitted to Alpaca with buffered limit prices and then polled until filled, rejected, canceled, expired, or timed out.
- PostgreSQL persists logs, scanner candidates, execution reports, closed trades, and periodic dashboard snapshots.
- Startup now fails fast if PostgreSQL is unreachable, Alpaca credentials are invalid, or live trading is selected without the explicit arming flag.
- If you leave `ALPACA_SYMBOLS` empty, wildcard streaming can still produce more candidate symbols than a basic market-data plan can hydrate. For tighter scanner feedback, prefer an explicit `ALPACA_SYMBOLS` list.
