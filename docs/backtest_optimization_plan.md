## Plan: Backtest Runtime Acceleration

Prioritize runtime reduction without changing command-line interface or output semantics. Use internal optimizations across simulation hot path, historical fetch/cache pipeline, and recording I/O. Sequence work so low-risk, high-impact wins land first, then deeper compute optimizations.

**Steps**
1. Phase 0 - Baseline and guardrails
1.1 Capture baseline timings using identical command inputs and dataset windows for 3 representative runs (short window, medium window, long window).
1.2 Capture stage timings by instrumenting elapsed duration around key regions: historical dataset preparation in cmd/backtest_command.go RunBacktest, per-day iterator startup in internal/backtest/backtest_dataset_iterator.go openHistoricalDayIterator, bar loop in internal/backtest/engine.go Run.
1.3 Add lightweight counters (not new flags) for bars processed, candidate evaluations, correlation checks, cache hits/misses, JSONL writes. Persist to existing logs only.

2. Phase 1 - Logging and recorder I/O optimization (parallel with Step 3)
2.1 Refactor internal/storage/filesystem.go appendJSON to reuse long-lived file handles per target file and buffered writers instead of open/write/close per record.
2.2 Preserve JSONL content/order semantics by flushing on recorder Close and at deterministic checkpoints in backtest completion path.
2.3 Remove duplicate serialization paths where candidate evaluation gets marshaled more than once in composite recording chain, while preserving both destinations when configured.
2.4 Keep writeBacktestReport behavior identical in cmd/backtest_command.go writeBacktestReport, but reduce allocations by streaming encode where possible while preserving equivalent JSON structure.

3. Phase 2 - Historical fetch/cache throughput (parallel with Step 2)
3.1 Eliminate duplicate sorting between internal/backtest/backtest_fetch.go fetchHistoricalJobFromAPI and internal/backtest/historical_cache_codec.go saveHistoricalJobCache. Sort exactly once.
3.2 Decouple fetch worker progression from cache write latency: fetch workers enqueue completed payloads to dedicated cache-writer workers.
3.3 Keep current cache key and artifact compatibility in internal/backtest/historical_cache_codec.go historicalCachePath; optimize internal encode/write path only.
3.4 Reduce per-bar encoding overhead in saveHistoricalJobCache by batching varint writes into reusable buffers.
3.5 Tune worker scheduling in internal/backtest/backtest_fetch.go PrepareHistoricalDataset so API-bound and disk-bound work are balanced under detected rate limits.

4. Phase 3 - Simulation hot path compute reduction (depends on Steps 2 and 3)
4.1 In internal/scanner/scanner.go computeMetrics and related indicator routines, convert repeated full-window recomputation to incremental rolling state updates stored on symbol state.
4.2 Ensure scanner state reset boundaries remain correct at day/session changes; keep signal semantics unchanged by validating incremental outputs against current formulas.
4.3 In internal/risk/correlation.go and internal/risk/risk.go, add per-timestamp correlation memoization so repeated entry checks reuse pairwise calculations until price state changes.
4.4 Reorder risk gate checks in internal/risk/risk.go Evaluate to fail fast on cheap/high-rejection guards before expensive exposure/correlation math, preserving logical equivalence.

5. Phase 4 - Pipeline and diagnostics overhead trim (depends on Step 4)
5.1 In internal/backtest/engine.go diagnostic callbacks, reduce mutex contention by minimizing critical-section size and using local accumulators before merge.
5.2 In internal/pipeline/pipeline.go callback path, avoid repeated object construction for diagnostics fields that are not changing within a bar.
5.3 Keep all existing diagnostics fields and output lines in cmd/backtest_command.go logBacktestDiagnostics unchanged.

6. Phase 5 - Integration and performance sign-off (depends on all prior steps)
6.1 Re-run the exact baseline scenarios and compute deltas for total runtime and phase-specific timings.
6.2 Confirm no user-facing behavior changes: same CLI options, same report schema, same recorder file names/formats, same summary/diagnostic line structure.
6.3 Produce a short performance report with before/after timings and hotspot reductions.

**Parallelism and dependencies**
1. Phase 1 and Phase 2 can run in parallel once baseline instrumentation is in place.
2. Phase 3 should start after Phases 1-2 land to avoid confounded profiling and reduce rework.
3. Phase 4 follows Phase 3 since diagnostics contention profile depends on hot-path compute changes.

**Relevant files**
- /Users/ecloud/dev/personal/momentum-trading-bot/cmd/backtest_command.go - RunBacktest orchestration, report writing, diagnostics logging contract
- /Users/ecloud/dev/personal/momentum-trading-bot/internal/backtest/engine.go - main bar loop, callback wiring, diagnostics aggregation
- /Users/ecloud/dev/personal/momentum-trading-bot/internal/backtest/backtest_fetch.go - job worker model, API pagination/retry path, timeout/rate-limit interactions
- /Users/ecloud/dev/personal/momentum-trading-bot/internal/backtest/historical_cache_codec.go - cache reader/writer codec, compression/encoding overhead
- /Users/ecloud/dev/personal/momentum-trading-bot/internal/backtest/backtest_dataset_iterator.go - per-day iterator setup, heap merge mechanics
- /Users/ecloud/dev/personal/momentum-trading-bot/internal/storage/filesystem.go - recorder write path and JSONL append behavior
- /Users/ecloud/dev/personal/momentum-trading-bot/internal/pipeline/pipeline.go - candidate/decision callback and recording flow
- /Users/ecloud/dev/personal/momentum-trading-bot/internal/scanner/scanner.go - indicator computation hotspot and symbol state lifecycle
- /Users/ecloud/dev/personal/momentum-trading-bot/internal/risk/risk.go - risk gate ordering and correlation check call site
- /Users/ecloud/dev/personal/momentum-trading-bot/internal/risk/correlation.go - pairwise correlation computation/memoization target

**Verification**
1. Run three fixed backtest commands (short, medium, long windows) and compare total wall-clock runtime before and after.
2. Compare stage timing logs: dataset preparation, iterator startup, simulation loop.
3. Compare artifact compatibility: report JSON schema shape, JSONL line format, filenames in logs directory.
4. Compare summary and diagnostics line structure and key counters for equivalence on a fixed seed/window.
5. Validate no new CLI flags or changed defaults were introduced.

**Decisions**
- Included scope: simulation CPU path, fetch/cache throughput, logging overhead.
- Excluded scope: strategy/risk policy behavior changes, feature additions, CLI surface changes, test-suite work.
- Constraint honored: maximize runtime speed while preserving current observable outputs and options.

**Further Considerations**
1. If strict numerical identity is required for indicator values, use a tolerance policy and explicit acceptance criteria before merging incremental math changes.
2. If disk throughput is the dominant bottleneck on the host machine, prioritize Phase 1 and Phase 2 completion before Phase 3.
3. If API rate-limit stalls dominate, prioritize worker scheduling and backoff tuning in fetch pipeline first.
