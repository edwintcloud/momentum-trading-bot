# Agent Notes

Last reviewed: 2026-03-18.

## Repo Summary

- This is a Go trading system with a small React/Vite operator dashboard.
- Live mode talks to Alpaca for market data, orders, account state, and historical bars.
- Live mode also requires PostgreSQL for event persistence.
- Backtests reuse the scanner, strategy, risk, and portfolio layers, but fills are simulated inside the backtest engine.
- Main entrypoint: `main.go`
- Backtest CLI entrypoint: `backtest_command.go`
- Frontend: `web/`

## How The Live App Works

1. `main.go` loads env config, verifies Postgres and Alpaca, then auto-tunes trading config from broker/account data.
2. `internal/market/engine.go` consumes Alpaca websocket bars and hydrates snapshot, premarket-volume, and news context.
3. `internal/scanner/scanner.go` turns ticks into momentum candidates.
4. `internal/strategy/strategy.go` turns candidates and ticks into entry and exit signals.
5. `internal/risk/risk.go` applies session, daily-loss, exposure, and position-limit gates.
6. `internal/execution/execution.go` submits limit orders to Alpaca and polls for fills.
7. `internal/portfolio/manager.go` tracks positions, realized/unrealized PnL, and closed trades.
8. `internal/api/server.go` serves the dashboard, websocket updates, and trading control endpoints.
9. `internal/runtime/state.go` holds operator-visible candidates, logs, dependency readiness, pause state, emergency stop state, and daily-loss gating.

## Important Files

- `main.go`: startup, goroutine wiring, broker seeding, dashboard snapshot persistence, reconciliation loop
- `internal/config/app.go`: env loading and default bind address
- `internal/config/tuning.go`: all strategy/risk defaults are auto-derived here
- `internal/alpaca/client.go`: all Alpaca REST/websocket integration
- `internal/backtest/engine.go`: historical replay and paper-fill simulation
- `internal/storage/postgres.go`: async event recorder and schema bootstrap
- `web/src/App.jsx`: dashboard UI and control buttons

## Operational Notes

- Startup seeds broker positions into the local portfolio and restores today’s closed trades from Postgres.
- Broker-restored positions are marked as `BrokerSeeded` so time-based exit rules do not treat them like fresh intraday entries after a restart.
- `TradesToday` in the dashboard status is sourced from Alpaca fill activity via startup and periodic broker sync, not from the local fill ledger.
- Dashboard snapshots are persisted every 10 seconds from `main.go`.
- The app reconciles local positions back to Alpaca every 60 seconds.
- The dashboard depends on built assets under `web/dist`; if they are missing, `/` returns `503 dashboard assets not built`.
- Docker builds the frontend first, then embeds `web/dist` into the final app image.
- Live app startup now requires `CONTROL_PLANE_AUTH_TOKEN`.
- By default the HTTP server binds to `127.0.0.1:8080`, not all interfaces.
- The dashboard, `/api/*`, and `/ws` require HTTP Basic auth with username `operator` and password `CONTROL_PLANE_AUTH_TOKEN`.
- `/healthz` and `/readyz` remain public.

## Backtest Reality Check

- The backtest path is wired through `go run . backtest ...`.
- Backtests tune config from Alpaca account/capability data when using Alpaca as the data source.
- There is no separate training or model-fitting phase in the current backtest path; it replays only the requested time window.
- The current repo does not contain a separate entry-model implementation despite older documentation references that existed before this review.

## Fixed On 2026-03-18

### Control plane hardening

- Operator routes now require auth and the default bind address is localhost-only.
- Websocket upgrades enforce same-origin checks.

### Restart semantics

- Broker-seeded positions are explicitly marked and strategy timing logic treats them as preexisting instead of brand new after restart.

### Backtest/docs parity

- Dead backtest training-window plumbing was removed and the README now matches the implementation.

### Dashboard trade count

- `StatusSnapshot.TradesToday` is broker-backed and refreshed from Alpaca fill activity instead of using the local entry/fill counters.

## Test Status

- Ran `env GOCACHE=/Users/ecloud/dev/personal/momentum-trading-bot/.cache/go-build go test ./...`
- Result: passing
- Packages without tests: `internal/execution`, `internal/market`, `internal/storage`, `internal/domain`, `internal/volumeprofile`
- Frontend build was not rerun during this review because `web/node_modules` is not present in the workspace

## Recommended Next Fixes

1. Decide whether to keep HTTP Basic auth long-term or move to a session/token scheme with explicit logout and finer-grained roles.
2. If Alpaca ever exposes trustworthy entry timestamps for positions in this flow, replace the `BrokerSeeded` timing fallback with the real original open time.
3. Add integration coverage for authenticated dashboard fetches and browser behavior if the frontend evolves further.
4. Add tests for packages that still have little or no coverage, especially `internal/api`, `internal/execution`, `internal/market`, and `internal/storage`.

## Guidance For Future Agents

- Treat this repo as a live-trading system first and a dashboard app second; auth, safety, and restart semantics matter more than UI polish.
- When changing strategy or risk logic, compare live-path behavior and backtest-path behavior together because the repo aims to keep them aligned.
- If you touch startup, review `seedFromBroker`, `seedClosedTradesFromDB`, and the periodic reconciliation loop together.
- If you touch the dashboard, remember the backend serves static assets from `web/dist`, not directly from `web/src`.
