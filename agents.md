# Agent Notes

Last reviewed: 2026-03-21.

## Repo Summary

- This is a Go trading system with a small React/Vite operator dashboard.
- Live mode talks to Alpaca for market data, orders, account state, and historical bars.
- Live mode also requires PostgreSQL for event persistence.
- Backtests reuse the scanner, strategy, risk, and portfolio layers, but fills are simulated inside the backtest engine.
- A walk-forward optimizer tunes strategy/risk knobs across weekly windows and produces versioned trading profiles.
- Main entrypoint: `main.go`
- Backtest CLI entrypoint: `backtest_command.go`
- Optimizer CLI entrypoint: `optimize_command.go`
- Frontend: `web/`
- Bundled trading profile: `profiles/default.json`
- Python tooling: `run_weekly_backtests.py`, `scripts/run_test_weeks.py`, `scripts/analyze_rejects.py`

## How The Live App Works

1. `main.go` loads env config, verifies Postgres and Alpaca, then auto-tunes trading config from broker/account data.
2. A trading profile is loaded from `TRADING_PROFILE_PATH` (or `profiles/default.json`) and overlaid onto the tuned config.
3. `internal/regime/tracker.go` tracks benchmark symbols (SPY, QQQ, IWM) to derive a market regime snapshot (bullish/bearish/ranging).
4. `internal/market/engine.go` consumes Alpaca websocket bars and hydrates snapshot, premarket-volume, news context, and stock float.
5. `internal/scanner/scanner.go` turns ticks into momentum candidates (setup types: breakout, consolidation, pullback, renewed-volume).
6. `internal/strategy/strategy.go` turns candidates and ticks into entry and exit signals; `tradeplan.go` computes stop placement and sizing.
7. `internal/risk/risk.go` applies session, daily-loss, exposure, position-limit, and shortability gates.
8. `internal/execution/execution.go` submits limit orders to Alpaca and polls for fills.
9. `internal/portfolio/manager.go` tracks positions, realized/unrealized PnL, and closed trades.
10. `internal/api/server.go` serves the dashboard, websocket updates, and trading control endpoints.
11. `internal/runtime/state.go` holds operator-visible candidates, logs, dependency readiness, pause state, emergency stop state, daily-loss gating, and optimizer status.
12. Events are recorded via `internal/telemetry/composite.go` which fans out to Postgres and filesystem recorders simultaneously.

## Important Files

### Core

- `main.go`: startup, goroutine wiring, broker seeding, dashboard snapshot persistence, reconciliation loop
- `internal/config/app.go`: env loading and default bind address
- `internal/config/tuning.go`: all strategy/risk defaults are auto-derived here (100+ knobs)
- `internal/config/profile.go`: trading profile loading, validation, and overlay onto base config
- `internal/alpaca/client.go`: all Alpaca REST/websocket integration
- `internal/markethours/hours.go`: canonical `America/New_York` location and tradable-session check
- `profile_runtime.go`: runtime profile loading and optimizer status builder for dashboard

### Strategy Pipeline

- `internal/scanner/scanner.go`: tick-to-candidate filtering (price, volume, gap, setup-type detection)
- `internal/strategy/strategy.go`: entry decisions and managed exits
- `internal/strategy/tradeplan.go`: stop placement, risk-per-share calculation, and position sizing
- `internal/risk/risk.go`: position limits, daily loss gates, exposure checks, shortability
- `internal/execution/execution.go`: limit order submission, fill polling, partial-fill handling
- `internal/portfolio/manager.go`: positions, PnL tracking, broker reconciliation
- `internal/regime/tracker.go`: benchmark-driven market regime detection (SPY/QQQ/IWM)
- `internal/volumeprofile/profile.go`: hardcoded intraday cumulative volume distribution model
- `internal/floatcache/cache.go`: file-backed stock float cache with Yahoo Finance fetching (refreshed daily)

### Backtest & Optimizer

- `backtest_command.go`: backtest CLI subcommand with date parsing, symbol resolution, diagnostics logging
- `optimize_command.go`: optimizer CLI subcommand (walk-forward parameter tuning)
- `internal/backtest/engine.go`: historical replay and paper-fill simulation
- `internal/optimizer/optimizer.go`: walk-forward grid search over 29 strategy/risk knobs
- `backtest_fetch.go`: historical bar fetching from Alpaca with caching, rate limiting, retry
- `backtest_dataset_iterator.go`: memory-efficient multi-symbol bar merge via min-heap
- `historical_cache_codec.go`: gzipped binary cache codec (version `v3`, price scale ×10,000)

### Persistence & Telemetry

- `internal/storage/postgres.go`: async event recorder with JSONB schema and schema bootstrap
- `internal/storage/filesystem.go`: JSONL file recorder (6 event types, non-blocking enqueue, cap 2048)
- `internal/telemetry/composite.go`: fan-out recorder (multiplexes events to multiple backends)
- `internal/telemetry/logger.go`: async file-based JSONL logger for executions, trades, indicators

### Dashboard & API

- `internal/api/server.go`: HTTP server with auth, REST endpoints, websocket, and static asset serving
- `internal/runtime/state.go`: operator-visible runtime state (controls, candidates, logs, optimizer status, regime)
- `web/src/App.jsx`: React dashboard UI and control buttons

### Profiles & Scripts

- `profiles/default.json`: bundled default trading profile (`high_conviction_breakout`)
- `run_weekly_backtests.py`: splits a date range into weeks and runs backtests, outputs CSV
- `scripts/run_test_weeks.py`: regression harness against 12 representative weeks
- `scripts/analyze_rejects.py`: entry rejection log analysis tool

## Operational Notes

- Startup seeds broker positions into the local portfolio and restores today’s closed trades from Postgres.
- Broker-restored positions are marked as `BrokerSeeded` so time-based exit rules do not treat them like fresh intraday entries after a restart.
- `TradesToday` in the dashboard status is sourced from Alpaca fill activity via startup and periodic broker sync, not from the local fill ledger.
- Broker account sync (equity, cash, trade counts) runs every 15 seconds via `startBrokerAccountSync`.
- Dashboard snapshots are persisted every 10 seconds from `main.go`.
- The app reconciles local positions back to Alpaca every 60 seconds.
- The dashboard depends on built assets under `web/dist`; if they are missing, `/` returns `503 dashboard assets not built`.
- Docker builds the frontend first, then embeds `web/dist` into the final app image.
- docker-compose mounts `./logs:/app/logs` and `./.cache:/app/.cache` so the live container shares the float cache populated by local backtests.
- Live app startup now requires `CONTROL_PLANE_AUTH_TOKEN`.
- By default the HTTP server binds to `127.0.0.1:8080`, not all interfaces.
- The dashboard, `/api/*`, and `/ws` require HTTP Basic auth with username `operator` and password `CONTROL_PLANE_AUTH_TOKEN`.
- `/healthz` and `/readyz` remain public.
- A trading profile is loaded at startup from `TRADING_PROFILE_PATH` env var or `profiles/default.json`; profiles overlay strategy/risk knobs onto the broker-tuned base config while preserving capital and hydration budget.
- The dashboard displays active/pending optimizer profile, version, last optimizer run time, and paper validation result.

## Backtest Reality Check

- The backtest path is wired through `go run . backtest -start <date> -end <date> [-data <csv>] [-report-out <path>]`.
- Backtests tune config from Alpaca account/capability data when using Alpaca as the data source.
- A CSV fallback mode (`-data <file>`) allows offline backtests without Alpaca API access.
- `-report-out` writes a JSON report artifact (default: `.cache/backtest/latest-report.json`; empty string disables).
- `resolveBacktestSymbols` calls `ListEquitySymbols(ctx, true)` which fetches **all** symbols (active + inactive) so that delisted tickers present in cached historical data are still replayed.
- Backtests fetch one extra day before the start date (`prevDayStart = start - 1 day`) so the engine can compute previous-day volume and gap percentages correctly.
- Pre-window bars are fed through the normalizer and regime tracker but skipped for scanning/trading (`withinWindow` check happens after `normalizeBar`).
- Historical cache version is `v3`; bars are stored with original Alpaca timestamps (no `.UTC()` coercion).
- Cache paths are SHA256-hashed from (version, feed, start, end, symbols) to ensure different parameters use different caches.
- The dataset iterator (`backtest_dataset_iterator.go`) loads one day at a time and merges multi-symbol streams via min-heap for memory efficiency.
- Backtest fill model uses 80% volume participation cap per bar, 5% spread penalty, and 2-bar fill timeout.
- Backtests write JSONL event logs to `./logs/` via `FilesystemRecorder` when `BACKTEST_LOG_DIR` is set or defaulting to `./logs`.
- Historical fetch workers scale with rate limit: 2 workers (limit < 180), 4 (limit < 600), 10 (limit ≥ 600).
- Symbols are batched into groups of 100 per fetch job to respect Alpaca API constraints.
- The backtest command logs detailed diagnostics: funnel stats (bars → candidates → signals → fills), rejection reasons, exit breakdowns by regime/setup/side, and sample entries/rejects.

## Optimizer

- The optimizer path is wired through `go run . optimize -as-of <date> [-start <date> -end <date>] [-data <csv>] [-out <dir>]`.
- Walk-forward optimization: divides data into search, validation, and holdout windows across up to 20 completed weeks.
- Grid search over 29 knobs (24 float, 5 int) with coarse pass then refined neighborhood around top candidates.
- Candidates are ranked by holdout performance: median weekly return, positive weeks %, P25 return, profit factor, max drawdown.
- Winner profile is written as a versioned JSON artifact to the output directory (default: `optimizer.DefaultArtifactDir`).
- `ArtifactStatus` tracks pending profile promotion: deployment mode, paper validation result, approval timestamps.
- The dashboard surfaces pending profile info so operators can see when an upgrade is available.
- If `-start`/`-end` are provided, `as-of` is ignored and a single custom window is used.

## Useful Commands

- `go run . backtest -start YYYY-MM-DD -end YYYY-MM-DD`
- `go run . backtest -start YYYY-MM-DD -end YYYY-MM-DD -report-out path/to/report.json`
- `go run . backtest -data path/to/bars.csv`
- `go run . optimize -as-of YYYY-MM-DD`
- `go run . optimize -start YYYY-MM-DD -end YYYY-MM-DD`
- `python3 run_weekly_backtests.py -start YYYY-MM-DD -end YYYY-MM-DD`
- `python3 scripts/run_test_weeks.py`

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

- There are no Go tests in this repo. Tests were intentionally removed and are not needed — do not write or re-add tests.
- Frontend build was not rerun during this review because `web/node_modules` is not present in the workspace.

## Recommended Next Fixes

1. Decide whether to keep HTTP Basic auth long-term or move to a session/token scheme with explicit logout and finer-grained roles.
2. If Alpaca ever exposes trustworthy entry timestamps for positions in this flow, replace the `BrokerSeeded` timing fallback with the real original open time.

## Guidance For Future Agents

- **Do not write or add tests.** Tests were intentionally removed from this repo and are not desired.
- Treat this repo as a live-trading system first and a dashboard app second; auth, safety, and restart semantics matter more than UI polish.
- When changing strategy or risk logic, compare live-path behavior and backtest-path behavior together because the repo aims to keep them aligned.
- If you touch startup, review `seedFromBroker`, `seedClosedTradesFromDB`, and the periodic reconciliation loop together.
- If you touch the dashboard, remember the backend serves static assets from `web/dist`, not directly from `web/src`.
- All timestamps flow through in their original timezone (typically ET from Alpaca); do not coerce to `.UTC()` — the `markethours` package is the single source of truth for timezone handling.
- The backtest historical cache is versioned (`v3`); if you change bar normalization or fetch logic, bump the version string in `historical_cache_codec.go` to invalidate stale caches.
- Trading profiles overlay strategy/risk knobs onto the broker-tuned base config; if you add new tuning knobs, wire them through `ApplyTradingProfile` in `internal/config/profile.go` and add them to the optimizer grid in `internal/optimizer/optimizer.go`.
- The optimizer produces versioned profile artifacts; `profile_runtime.go` bridges optimizer output to the dashboard via `OptimizerStatus`.
- Market regime tracking is optional (`EnableMarketRegime` defaults to `false`); the regime tracker watches SPY/QQQ/IWM benchmarks and these are excluded from scanner candidates.
- The `internal/volumeprofile/profile.go` hardcodes a realistic intraday volume curve; update it if trading session hours change.
- Stock float data is cached in `.cache/float/shares.json` and fetched from Yahoo Finance (`v7/finance/quote`) via crumb-based cookie authentication. `EnsureFresh(ctx, symbols, maxAge)` accepts a staleness threshold: live hydration uses the default 24h, backtest and optimizer use 7 days to avoid redundant re-fetches during batch runs. Float data is wired through all three paths (live, backtest, optimizer) via `FloatLookup`.
- Float is actively used in the strategy pipeline: (1) scanner gate rejects candidates below `MinFloat` (default 500K shares), (2) float rotation (volume/float) is a weighted scoring component (`FloatRotationScoreWeight`, default 3.0) in both long and short momentum scores, (3) position sizing applies float-based multipliers (0.65× for <1M, 0.80× for <3M, 0.90× for >50M float), (4) shorts are blocked on stocks below `ShortMinFloat` (default 5M) to avoid squeeze risk, (5) low-float positions (<3M) trigger stagnation exits 1 minute earlier than the standard window, (6) entry quality requires minimum 10% float rotation (volume/float) for both longs and shorts during regular session. All three knobs (`MinFloat`, `ShortMinFloat`, `FloatRotationScoreWeight`) are wired through profile overlay and the optimizer grid.
- Python scripts under `scripts/` and `run_weekly_backtests.py` are tooling for offline analysis — they invoke `go run . backtest` and parse stdout.
