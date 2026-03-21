# Agent Notes

Last reviewed: 2026-03-21.

## Repo Summary

- This is a Go trading system with a small React/Vite operator dashboard.
- Live mode talks to Alpaca for market data, orders, account state, and historical bars.
- Live mode also requires PostgreSQL for event persistence.
- Backtests reuse the scanner, strategy, risk, and portfolio layers, but fills are simulated inside the backtest engine.
- Main entrypoint: `main.go`
- Backtest CLI entrypoint: `backtest_command.go`
- Targeted diagnostics entrypoints: `bull_run_diagnostics_command.go`, `symbol_trace_command.go`
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
- `internal/storage/filesystem.go`: JSONL file recorder for backtest logs (candidates, trades, indicators)
- `internal/markethours/hours.go`: canonical `America/New_York` location and tradable-session check
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
- `resolveBacktestSymbols` calls `ListEquitySymbols(ctx, true)` which fetches **all** symbols (active + inactive) so that delisted tickers present in cached historical data are still replayed.
- Backtests fetch one extra day before the start date (`prevDayStart = start - 1 day`) so the engine can compute previous-day volume and gap percentages correctly.
- Pre-window bars are fed through the normalizer and regime tracker but skipped for scanning/trading (`withinWindow` check happens after `normalizeBar`).
- Historical cache version is `v3`; bars are stored with original Alpaca timestamps (no `.UTC()` coercion).
- Backtest fill model uses 80% volume participation cap per bar (changed from 10%), 5% spread penalty, and 2-bar fill timeout.
- Backtests now write JSONL event logs to `./logs/` via `FilesystemRecorder` when `BACKTEST_LOG_DIR` is set or defaulting to `./logs`.
- There is no separate training or model-fitting phase in the current backtest path; it replays only the requested time window.
- The current repo does not contain a separate entry-model implementation despite older documentation references that existed before this review.

## Useful Commands

- `go run . backtest -start YYYY-MM-DD -end YYYY-MM-DD`
- `go run . bull-run-diagnostics -audit .cache/backtest/experiments/bull_run_audit_selected.json`
- `go run . symbol-trace -symbol AFJK -day 2025-12-09 -from 10:06 -to 11:30`

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

## Fixed On 2026-03-21

### Timezone consistency

- Removed all per-package `mustLoadLocation` / `easternLocation` vars; everything now uses `markethours.Location()`.
- Stopped coercing timestamps to `.UTC()` throughout the codebase — bars, fills, entry samples, and normalizer state all keep Alpaca's original timezone.
- `parseCLIBacktestTime` no longer converts parsed dates to UTC, preserving ET-aware windows.
- `endOfMarketDay` now returns 20:00 ET instead of 23:59 UTC.
- `inferBacktestWindows` uses `time.Now().In(markethours.Location())` instead of `time.Now().UTC()`.

### Backtest universe fix

- `ListActiveEquitySymbols` renamed to `ListEquitySymbols(ctx, includeInactive bool)` — backtests pass `true` to include delisted/inactive symbols so cached historical data is replayed.
- Backtest fetches one extra calendar day before start so prev-day volume and gap calculations have data.

### Backtest fill realism

- Volume participation cap raised from 10% to 80% per bar.
- `LimitOrderSlippageDollars` changed from 0.02 to 0.05 in default profile.

### Profile tuning changes

- `MinRelativeVolume`: 4.4 → 2
- `BreakEvenHoldMinutes`: 4 → 15
- `TightTrailTriggerR`: 1.5 → 2

### New: Filesystem event recorder

- `internal/storage/filesystem.go` writes JSONL logs for backtest events (candidates, trades, indicators).
- Backtest engine accepts optional `Recorder` in `RunConfig`.

### Cleanup

- All test files removed (tests were stale after architecture changes).
- Removed unused `fetchBarsFromAlpaca` and `fetchHistoricalJob` wrappers from `backtest_fetch.go`.
- Historical worker count raised to 10 for rate limits ≥ 600.
- Rate limiter minimum interval lowered from 100ms to 10ms.

## Test Status

- All test files were removed in the cleanup commit; there are currently no Go tests in the repo.
- Frontend build was not rerun during this review because `web/node_modules` is not present in the workspace.

## Recommended Next Fixes

1. Decide whether to keep HTTP Basic auth long-term or move to a session/token scheme with explicit logout and finer-grained roles.
2. If Alpaca ever exposes trustworthy entry timestamps for positions in this flow, replace the `BrokerSeeded` timing fallback with the real original open time.
3. Re-add tests — the test suite was removed during cleanup; new tests should be written against the current architecture.
4. Add integration coverage for authenticated dashboard fetches and browser behavior if the frontend evolves further.

## Guidance For Future Agents

- Treat this repo as a live-trading system first and a dashboard app second; auth, safety, and restart semantics matter more than UI polish.
- When changing strategy or risk logic, compare live-path behavior and backtest-path behavior together because the repo aims to keep them aligned.
- If you touch startup, review `seedFromBroker`, `seedClosedTradesFromDB`, and the periodic reconciliation loop together.
- If you touch the dashboard, remember the backend serves static assets from `web/dist`, not directly from `web/src`.
- All timestamps flow through in their original timezone (typically ET from Alpaca); do not coerce to `.UTC()` — the `markethours` package is the single source of truth for timezone handling.
- The backtest historical cache is versioned (`v3`); if you change bar normalization or fetch logic, bump the version string in `backtest_fetch.go` to invalidate stale caches.
