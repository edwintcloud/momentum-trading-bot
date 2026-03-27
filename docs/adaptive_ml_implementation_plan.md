# Adaptive ML Implementation Plan

Status: active
Owner: strategy/ML/backtest pipeline
Last updated: 2026-03-27

## Goal

Replace growing rule-specific edge cases with a more generalized, adaptive decision layer that:

- uses the existing scanner and playbooks as candidate generators
- learns which candidates are worth taking, sizing up, sizing down, or rejecting
- works identically in backtesting and live trading
- retrains automatically on rolling windows without lookahead bias

## Execution Rules

- Work this plan strictly in order.
- Do not start the next step until the current step is marked `[x]`.
- No ML feature or model is considered complete unless it runs through the backtest engine, not just live trading.
- All model training and promotion must be walk-forward and out-of-sample only.
- Backtest determinism must be preserved before and after each step.

## Core Principles

- Candidate generation remains rule-based at first. ML ranks and gates candidates instead of replacing the scanner outright.
- Use simple tabular models first. Prefer calibrated gradient-boosted trees over deep learning.
- Train separate models by side and, if needed later, by setup family or regime.
- Keep hard risk controls non-ML. ML may advise entry quality, sizing multipliers, and strategy-profile selection.
- Favor shadow mode first, then advisory mode, then guarded promotion into live decisioning.

## Required Regression Suite

Every step below must be checked against these before it is marked complete:

- Full backtest parity: the same code path must work in backtest and live mode.
- Key long regression: `ANNA` on `2026-03-20`.
- Key squeeze regression: `AFJK` on `2025-12-09`.
- Bear/bad-week checks:
  - `2025-06-25` to `2025-07-02`
  - `2025-09-17` to `2025-09-24`
  - `2025-10-08` to `2025-10-15`
- Annual validation target:
  - full-year `2025`, validated with weekly batch windows rather than one monolithic annual backtest
- Determinism check:
  - repeated runs on the same window must produce identical reports

## Phase 0: Lock The Baseline

- [x] Create a baseline report pack and save it under `.cache/backtest/plan_baseline/`.
- [x] Record the current profile version, model-free behavior, and regression outputs in a machine-readable summary file.
- [x] Add a single source of truth for the required regression windows and symbols so future automation uses the same suite every time.
- [x] Define promotion guardrails for ML artifacts, similar to auto-optimizer profile guardrails.

Completion criteria:

- baseline artifacts exist for all required windows
- repeated determinism checks pass
- guardrails for ML artifacts are written down and code-targeted

## Phase 1: Candidate Dataset Export

- [x] Add a backtest-first candidate export pipeline that writes one row per scanner/strategy candidate, regardless of whether it was traded.
- [x] Export the same feature schema for both backtest and live candidate evaluation paths.
- [x] Include:
  - symbol, timestamp, side, setup type, regime snapshot, feature vector, score, rejection reason, risk approval result
- [x] Save exports in a stable artifact format such as JSONL or parquet-ready JSONL.
- [x] Ensure exported features are strictly causal at candidate time.

Suggested code areas:

- [internal/scanner/scanner.go](../internal/scanner/scanner.go)
- [internal/strategy/strategy.go](../internal/strategy/strategy.go)
- [internal/backtest/engine.go](../internal/backtest/engine.go)

Completion criteria:

- backtest command can emit candidate datasets for arbitrary date windows
- live path can emit the same schema without special-case feature logic

Phase 1 completion notes:

- Backtest export is available via `go run . backtest ... -candidate-out <path>`.
- Shared pipeline emits identical `CandidateEvaluation` rows for backtest and live paths.
- Smoke artifact: [.cache/backtest/plan_baseline/anna_candidates.jsonl](../.cache/backtest/plan_baseline/anna_candidates.jsonl)

## Phase 2: Labeling And Outcome Join

- [x] Build an offline labeling job that attaches outcome labels to exported candidates.
- [x] Use triple-barrier labeling and meta-labeling first.
- [x] Add derived labels:
  - profitable vs unprofitable
  - expected return bucket
  - max favorable excursion
  - max adverse excursion
  - time-to-target and time-to-stop
- [x] Make labels reproducible from historical bars only.
- [x] Keep trade-linked outcomes and non-traded candidate outcomes separate.

Suggested code areas:

- [internal/ml/metalabel.go](../internal/ml/metalabel.go)
- new offline labeling package or command

Completion criteria:

- any exported candidate dataset can be labeled offline with no lookahead leaks
- labeled outputs are consumable by training jobs and by backtest diagnostics

Phase 2 completion notes:

- Offline labeling command is available via `go run . label-candidates ...`.
- Labeling uses historical 1-minute bars only and prefers the existing backtest cache before any API fetch.
- Trade-linked and non-traded candidates remain distinguishable via `tradeLinked` and `riskApproved`.
- Smoke artifacts:
  - [.cache/backtest/plan_baseline/anna_candidate_labels.jsonl](../.cache/backtest/plan_baseline/anna_candidate_labels.jsonl)
  - [.cache/backtest/plan_baseline/anna_candidate_labels_summary.json](../.cache/backtest/plan_baseline/anna_candidate_labels_summary.json)

## Phase 3: Rolling-Window Trainer

- [x] Add an automated trainer that uses rolling windows, not fixed in-sample optimization.
- [x] Train on trailing windows only, validate on the next unseen window, and roll forward.
- [x] Start with separate models for:
  - long candidates
  - short candidates
- [x] Train a simple tabular model first, with probability calibration.
- [x] Emit model artifacts plus validation reports.

Rolling-window requirements:

- training window: configurable trailing lookback, for example 3 to 6 months
- validation window: configurable next block, for example 1 to 2 weeks
- purge gap: required between training and validation windows
- no current-day data in training for that day’s decisions

Suggested implementation pattern:

- mirror the existing scheduler/promoter flow in [scheduler.go](../internal/autooptimize/scheduler.go)
- add ML artifact guardrails similar to [guardrails.go](../internal/autooptimize/guardrails.go)

Completion criteria:

- a training command can run rolling-window backtests automatically
- outputs include per-window metrics, aggregate out-of-sample metrics, and model artifacts

Phase 3 completion notes:

- Training command is available via `go run . train-ml ...`.
- Current baseline model is a native calibrated logistic SGD artifact, trained separately for `long` and `short`.
- Smoke artifacts:
  - [.cache/ml/june_smoke/training_report.json](../.cache/ml/june_smoke/training_report.json)
  - [.cache/ml/june_smoke/long_model.json](../.cache/ml/june_smoke/long_model.json)
  - [.cache/ml/june_smoke/short_model.json](../.cache/ml/june_smoke/short_model.json)
- Supporting smoke dataset artifacts:
  - [.cache/backtest/plan_baseline/june_candidates.jsonl](../.cache/backtest/plan_baseline/june_candidates.jsonl)
  - [.cache/backtest/plan_baseline/june_candidate_labels.jsonl](../.cache/backtest/plan_baseline/june_candidate_labels.jsonl)

## Phase 4: Backtest-Native Model Inference

- [x] Load trained model artifacts inside the backtest path first.
- [x] Add model inference to candidate evaluation in shadow mode.
- [x] Record:
  - model score
  - calibrated probability
  - model rank within bar/day
  - what the model would have vetoed or upsized
- [x] Confirm identical inference code is used in live trading.

Suggested code areas:

- [internal/ml/scorer.go](../internal/ml/scorer.go)
- [internal/strategy/strategy.go](../internal/strategy/strategy.go)
- [internal/backtest/engine.go](../internal/backtest/engine.go)

Completion criteria:

- backtests can run with model scoring on, using saved artifacts
- live and backtest use the same scorer interface and artifact format
- shadow-mode comparisons are visible in reports

Phase 4 completion notes:

- Shared pipeline now accepts a scorer and annotates `CandidateEvaluation` rows with ML shadow fields.
- Saved artifacts from `train-ml` load via `MLModelPath` or backtest CLI override `-ml-model`.
- Backtest shadow-mode smoke artifacts:
  - [.cache/backtest/plan_baseline/anna_ml_shadow_smoke.json](../.cache/backtest/plan_baseline/anna_ml_shadow_smoke.json)
  - [.cache/backtest/plan_baseline/anna_candidates_ml_shadow.jsonl](../.cache/backtest/plan_baseline/anna_candidates_ml_shadow.jsonl)
- Smoke validation kept ANNA trade behavior unchanged while logging ML shadow stats:
  - `scored=559`
  - `vetos=544`
  - `upsizes=15`

## Phase 5: Advisory Mode

- [x] Use model output only for ranking, vetoing the weakest candidates, and modest sizing changes.
- [x] Keep hard-coded playbooks, risk controls, and stops intact.
- [x] Add configuration for:
  - minimum ML probability
  - maximum veto strength
  - sizing multiplier bands
- [x] Run side-by-side comparisons:
  - rules only
  - rules + ML advisory

Completion criteria:

- advisory mode improves out-of-sample metrics versus rules-only on the regression suite
- annual `2025` results improve without losing `ANNA` and `AFJK`

Phase 5 progress notes:

- Shared pipeline advisory plumbing now exists in both backtest and live paths:
  - optional ML veto
  - optional ML downsize
  - optional ML upsize
- Advisory actions are recorded in `CandidateEvaluation` rows and surfaced in backtest diagnostics separately from Phase 4 shadow-mode fields.
- Backtest CLI now supports:
  - `-ml-advisory`
  - `-ml-advisory-min-prob`
  - `-ml-advisory-max-vetos`
- First advisory smoke runs:
  - aggressive default with veto enabled was too disruptive for `ANNA`
  - safer default profile now uses sizing-only advisory (`MLAdvisoryVetoEnabled=false`) until we validate vetoing with proper out-of-sample artifacts
- Current smoke artifacts:
  - [.cache/backtest/plan_baseline/anna_ml_advisory_smoke.json](../.cache/backtest/plan_baseline/anna_ml_advisory_smoke.json)
  - [.cache/backtest/plan_baseline/anna_ml_advisory_smoke_v2.json](../.cache/backtest/plan_baseline/anna_ml_advisory_smoke_v2.json)
- Current recommendation:
  - keep advisory enabled only in controlled backtests for now
  - treat veto as an opt-in experiment, not the default
  - complete Phase 5 only after a proper walk-forward artifact is evaluated on the regression suite
- `train-ml` can now optionally run a sequential post-train regression comparison using:
  - `-regression-suite`
  - `-regression-windows`
  - `-regression-out-dir`
  - `-regression-advisory`
- First automated comparison smoke artifact:
  - [.cache/ml/june_regression_smoke/regression/regression_summary.json](../.cache/ml/june_regression_smoke/regression/regression_summary.json)
- That smoke comparison is intentionally not a keep/promotion signal:
  - `ANNA rules-only net=+1298.18`
  - `ANNA advisory net=+925.79`
  - meaning the current June-trained advisory artifact is not ready for promotion
- Expanded multi-window regression pass with the same June-trained artifact also came back negative on the completed windows:
  - `ANNA`: `+1298.18 -> +925.79`
  - `AFJK`: `+7077.10 -> +5363.46`
  - `bear_week_june`: `+1215.06 -> +849.83`
  - `bear_week_october`: `+1014.17 -> +619.24`
  - `bear_week_september`: `-1374.32 -> -1097.96`
- Interpretation:
  - this model is learning something useful for a weak September tape
  - but it is still too blunt and is cutting into the strongest long regressions
  - next work should focus on broader training data and better walk-forward artifact selection, not promotion of this specific June-trained model
- A reusable broader-data builder now exists:
  - `go run . prepare-ml-dataset -suite ... -out-dir ...`
  - it backtests candidate exports and labels them sequentially, then writes a manifest for `train-ml -manifest ...`
- The dataset builder now also supports rolling calendar corpus generation:
  - `go run . prepare-ml-dataset -start YYYY-MM-DD -end YYYY-MM-DD -window-days N -step-days M -out-dir ...`
  - this keeps the same backtest-first candidate export and offline labeling flow, but removes the need to hand-curate every training window in a suite JSON file
  - smoke artifact:
    - [.cache/ml/calendar_dataset_smoke/manifest.json](../.cache/ml/calendar_dataset_smoke/manifest.json)
- Broader bear-week training corpus smoke:
  - manifest: [.cache/ml/bear_weeks_dataset/manifest.json](../.cache/ml/bear_weeks_dataset/manifest.json)
  - model/regression summary: [.cache/ml/bear_weeks_model/regression/regression_summary.json](../.cache/ml/bear_weeks_model/regression/regression_summary.json)
- That broader corpus reduced some damage versus the June-only model, but is still not promotable:
  - `ANNA`: `+1298.18 -> +976.08`
  - `AFJK`: `+7077.10 -> +5363.46`
- Key training-target improvement:
  - `train-ml` now supports `-sample-scope` and the strongest current behavior comes from narrower advisory-aligned rows rather than all scanner candidates.
  - `risk_approved` improved the strong winner regressions materially versus the earlier all-candidate artifacts.
- New scope-selection automation now exists:
  - `train-ml` supports `-sample-scope-grid ...`
  - it trains each scope sequentially, runs the regression suite for each artifact, and writes a recommendation summary.
  - tie-breaking now prefers narrower advisory-aligned scopes over broader ones when regression results are otherwise equal.
- Current scope-comparison smokes:
  - mixed winner/bear dataset with `all,trade_linked,risk_approved`:
    - [.cache/ml/scope_grid_smoke/scope_selection_summary.json](../.cache/ml/scope_grid_smoke/scope_selection_summary.json)
    - `all` failed `ANNA`
    - `trade_linked` and `risk_approved` both passed `ANNA` and `AFJK`
  - tie-break verification:
    - [.cache/ml/scope_grid_smoke_v2/scope_selection_summary.json](../.cache/ml/scope_grid_smoke_v2/scope_selection_summary.json)
    - recommendation: `risk_approved`
- Additional regression evidence for the promising `risk_approved` artifact:
  - October bad-week advisory replay improved from rules-only `+1014.17` to `+1206.11` in [.cache/ml/mixed_regression_model_risk_approved/regression/bear_week_october_ml_advisory.json](../.cache/ml/mixed_regression_model_risk_approved/regression/bear_week_october_ml_advisory.json)
- Full non-annual scope comparison is now complete:
  - summary: [.cache/ml/scope_grid_full_nonannual/scope_selection_summary.json](../.cache/ml/scope_grid_full_nonannual/scope_selection_summary.json)
  - recommendation: `risk_approved`
  - comparison result:
    - `trade_linked` net delta across `ANNA`, `AFJK`, June, September, October: `-12.62`
    - `risk_approved` net delta across the same five windows: `+201.91`
  - both scopes passed the non-annual must-pass regressions, but `risk_approved` was materially better because it preserved October and improved September more while giving back slightly less in June.
- Annual guardrail still fails for the current `risk_approved` artifact:
  - baseline full-year `2025`: [.cache/backtest/plan_baseline/annual_2025.json](../.cache/backtest/plan_baseline/annual_2025.json)
    - `net=-3103.13`, `roi=-12%`, `max_drawdown=16.73%`
  - advisory full-year `2025`: [.cache/ml/mixed_regression_model_risk_approved/regression/annual_2025_ml_advisory.json](../.cache/ml/mixed_regression_model_risk_approved/regression/annual_2025_ml_advisory.json)
    - `net=-3328.49`, `roi=-13.31%`, `max_drawdown=16.39%`
  - interpretation:
    - the model improves some targeted regression windows and slightly lowers annual drawdown
    - but it still worsens annual net PnL, so it is not promotable yet
- Current interpretation:
  - the advisory model should be trained and selected using rows closer to the actual entry decision boundary, not every scanner candidate
  - `all` is clearly too blunt
  - `risk_approved` is now the current best direction on the non-annual regression suite
  - `trade_linked` remains a useful comparison baseline, but it is weaker than `risk_approved`
  - full promotion is still blocked by annual `2025`, so the next work should focus on improving annual robustness rather than widening deployment
  - advisory applied only as downsizing on both windows
- Broader sampled-2025 rolling corpus is now available:
  - manifest: [.cache/ml/rolling_2025_sampled_dataset/manifest.json](../.cache/ml/rolling_2025_sampled_dataset/manifest.json)
  - built sequentially with `prepare-ml-dataset` calendar mode over sampled 14-day windows across `2025`
- First sampled-2025 `risk_approved` training pass did not create any usable walk-forward validation windows:
  - training report: [.cache/ml/rolling_2025_sampled_model_risk_approved/training_report.json](../.cache/ml/rolling_2025_sampled_model_risk_approved/training_report.json)
  - non-annual regression summary: [.cache/ml/rolling_2025_sampled_model_risk_approved/regression/regression_summary.json](../.cache/ml/rolling_2025_sampled_model_risk_approved/regression/regression_summary.json)
  - outcome:
    - `ANNA`: `+1298.18 -> +939.85`
    - `AFJK`: `+7077.10 -> +5363.46`
    - `bear_week_june`: `+1215.06 -> +1029.60`
    - `bear_week_september`: `-1374.32 -> -861.35`
    - `bear_week_october`: `+1014.17 -> +766.33`
- Wider sampled-2025 walk-forward splits fixed the validation-coverage issue:
  - training report: [.cache/ml/rolling_2025_sampled_model_risk_approved_wide/training_report.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/training_report.json)
  - regression summary: [.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression/regression_summary.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression/regression_summary.json)
  - walk-forward coverage:
    - long validation windows: `3`
    - short validation windows: `2`
  - aggregate validation metrics:
    - long `count=46`, `win_rate=36.96%`, `avg_return=-0.28%`
    - short `count=52`, `win_rate=26.92%`, `avg_return=-1.47%`
  - important finding:
    - regression behavior remained effectively unchanged versus the earlier sampled-2025 artifact, which means broader sampled data plus wider splits improved diagnostics but did not yet improve advisory decisions
- Updated next-step priority:
  - do not spend another annual `2025` replay on the sampled-2025 advisory artifact yet
  - next ML work should improve label design, feature quality, or advisory action logic so the model can distinguish elite momentum winners from mediocre candidates instead of mostly downsizing everything
- Advisory action logic is now less blunt:
  - shared pipeline advisory mode protects top-ranked ML candidates from automatic downsizing using day-rank and bar-rank context before quantity adjustment
  - current default profile protects:
    - top `1` candidate so far in the day
    - top `1` candidate in the current bar
- Initial validation of rank-protected advisory with the broader sampled-2025 `risk_approved` artifact:
  - `ANNA`: `+939.85 -> +1298.18`
    - advisory effect removed; rules-only timing and PnL are preserved again
  - `AFJK`: `+5363.46 -> +7139.50`
    - advisory no longer cuts the exceptional squeeze winner; result is slightly better than rules-only `+7077.10`
- Current kept advisory baseline is now:
  - top-ranked long entries remain protected from ML downsizing
  - long downsizing threshold stays at `0.62`
  - short downsizing threshold is relaxed, but only to `0.45` rather than the earlier too-loose `0.30`
- Annual and long-horizon validation should now use the Go-native batch runner instead of a single monolithic `backtest` window:
  - command: `go run . batch-backtest -start ... -end ... -window-days 7 -step-days 7 ...`
  - rationale:
    - matches the existing weekly validation style more closely
    - keeps annual sweeps observable and bounded per window
    - avoids treating an entire year as one giant contiguous backtest when the practical review unit is weekly
- Weekly annual validation is now complete on the current kept advisory baseline and should be treated as the canonical annual check:
  - ML advisory weekly annual `2025`:
    - [.cache/backtest/annual_2025_weekly/summary.json](../.cache/backtest/annual_2025_weekly/summary.json)
    - `totalNetPnL=-1633.09`
    - `winningWeeks=22`
    - `losingWeeks=31`
  - rules-only weekly annual `2025`:
    - [.cache/backtest/annual_2025_weekly_rules_only/summary.json](../.cache/backtest/annual_2025_weekly_rules_only/summary.json)
    - `totalNetPnL=-2073.21`
    - `winningWeeks=23`
    - `losingWeeks=30`
  - net improvement from advisory versus rules-only:
    - `+440.12`
  - interpretation:
    - current advisory is a keep versus rules-only on the proper annual weekly benchmark
    - Phase 5 is still not complete because the year remains negative overall
    - the next iteration should target the worst ML negative-delta weeks, not move on to Phase 6 yet
- Highest-priority negative-delta weekly windows from the annual comparison:
  - `2025-10-22` to `2025-10-28`
  - `2025-05-07` to `2025-05-13`
  - `2025-10-08` to `2025-10-14`
  - `2025-07-02` to `2025-07-08`
  - `2025-04-02` to `2025-04-08`
- Phase 5 current status:
  - implementation is functionally in place
  - validation is directionally positive on the annual weekly benchmark
  - promotion is still blocked because annual `2025` remains below breakeven
- Why this is the current keep:
  - it improved the sampled-2025 weak September window while also improving October relative to the prior long-only-rank-protected baseline, without disturbing the key winner regressions
  - June softened slightly, but the three-window combined result improved versus the prior kept baseline
- Current checkpoint artifacts:
  - `ANNA` unchanged:
    - [.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_side_thresholds_v045/anna_regression_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_side_thresholds_v045/anna_regression_ml_advisory.json)
    - `net=+1298.18`
  - `AFJK` unchanged from the stronger advisory path:
    - [.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_side_thresholds_v045/afjk_regression_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_side_thresholds_v045/afjk_regression_ml_advisory.json)
    - `net=+7139.50`
  - sampled-2025 advisory comparison with `short_downsize_threshold=0.45`:
    - June: [.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_side_thresholds_v045/bear_week_june_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_side_thresholds_v045/bear_week_june_ml_advisory.json)
      - `+859.25` vs prior kept advisory `+885.25`
    - September: [.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_side_thresholds_v045/bear_week_september_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_side_thresholds_v045/bear_week_september_ml_advisory.json)
      - `-859.76` vs prior kept advisory `-935.10`
    - October: [.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_side_thresholds_v045/bear_week_october_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_side_thresholds_v045/bear_week_october_ml_advisory.json)
      - `+731.47` vs prior kept advisory `+695.93`
- Rejected alternative from the same area:
  - `short_downsize_threshold=0.30` improved September and June more aggressively, but damaged October too much and was not kept
  - `bear_week_september`: `-861.35 -> -1110.08`
    - weaker than the prior sampled-2025 advisory run, but still better than rules-only `-1374.32`
  - interpretation:
    - rank protection materially improves winner preservation
    - sampled-2025 advisory is still not ready for promotion because its weak-tape protection softened at the same time
    - next ML iteration should target smarter downsize eligibility, not just more training windows
- Refined keep state:
  - rank protection now applies only to long entries, not shorts
  - this better matches the intended use:
    - preserve elite momentum longs like `ANNA` and `AFJK`
    - still allow weak short fades to be downsized aggressively
- Long-only rank-protected advisory results:
  - `ANNA`: [anna_regression_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_rank_protected_long_only/anna_regression_ml_advisory.json)
    - `+1298.18`, matching rules-only
  - `AFJK`: [afjk_regression_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_rank_protected_long_only/afjk_regression_ml_advisory.json)
    - `+7139.50`, slightly above rules-only `+7077.10`
  - `bear_week_september`: [bear_week_september_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_rank_protected_long_only/bear_week_september_ml_advisory.json)
    - `-935.10`
  - comparison:
    - better than rules-only `-1374.32`
    - better than all-sides rank protection `-1110.08`
    - slightly worse than the blunt sampled-2025 advisory `-861.35`
  - interpretation:
    - long-only rank protection is the strongest advisory behavior so far because it preserves the must-keep winner regressions while still keeping most of the weak-week benefit
- Rejected follow-up experiment:
  - short-side downsizing was made probability-aware so very low-confidence short entries would be cut more aggressively than borderline ones
  - that helped September materially:
    - `ANNA`: [anna_regression_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_rank_protected_long_only_v2/anna_regression_ml_advisory.json)
      - `+1298.18`
    - `AFJK`: [afjk_regression_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_rank_protected_long_only_v2/afjk_regression_ml_advisory.json)
      - `+7189.90`
    - `bear_week_september`: [bear_week_september_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_rank_protected_long_only_v2/bear_week_september_ml_advisory.json)
      - `-604.24`
  - but it degraded other weak-week checks too much:
    - `bear_week_june`: [bear_week_june_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_rank_protected_long_only_v2/bear_week_june_ml_advisory.json)
      - `+660.33`, below rules-only `+1215.06`
    - `bear_week_october`: [bear_week_october_ml_advisory.json](../.cache/ml/rolling_2025_sampled_model_risk_approved_wide/regression_rank_protected_long_only_v2/bear_week_october_ml_advisory.json)
      - weaker than both rules-only and the prior fixed-downsize advisory run
  - outcome:
    - rolled back
    - kept baseline remains the simpler long-only rank-protected advisory behavior

## Phase 6: Drift Detection And Safe Fallback

- [ ] Wire concept-drift checks into the artifact lifecycle.
- [ ] Use PSI and rolling performance decay to reduce or disable model influence.
- [ ] Add fallback behavior:
  - model confidence downweight
  - revert to rules-only mode
  - schedule retraining

Suggested code areas:

- [internal/ml/drift.go](../internal/ml/drift.go)
- [internal/runtime](../internal/runtime)

Completion criteria:

- drift can be detected from live and backtest feature distributions
- model weight can be reduced automatically without code changes

## Phase 7: Automatic Retraining And Promotion

- [ ] Add an ML training scheduler modeled after the auto-optimizer.
- [ ] Run retraining automatically on a fixed schedule, such as weekly on Saturday morning ET.
- [ ] The scheduler must:
  - export recent candidate data
  - label it
  - train rolling-window models
  - validate against guardrails
  - promote only if the candidate model beats the current production model
- [ ] Save status files and latest promoted model metadata, similar to the auto-optimizer.

Backtest requirement:

- the promoted artifact must be immediately runnable in backtests for exact replay
- promotion is invalid if the artifact only works in live mode

Completion criteria:

- ML retraining happens automatically on rolling windows
- promoted model artifacts are versioned and backtest-replayable

## Phase 8: Regime-Conditional Experts

- [ ] Split the advisory layer into separate experts when justified by data:
  - long squeeze expert
  - short fade expert
  - stand-aside or low-participation expert
- [ ] Use regime probability as a feature first and as a routing signal second.
- [ ] Validate that expert routing improves full-year robustness, not just a few windows.

Completion criteria:

- regime-conditional models outperform a single global model out of sample

## Phase 9: Profile Selection Layer

- [ ] Only after the above is stable, evaluate a constrained contextual-bandit layer.
- [ ] The bandit should choose among vetted strategy profiles or risk profiles, not raw buy/sell actions.
- [ ] Candidate actions may include:
  - long-friendly profile
  - short-friendly profile
  - conservative profile
  - no-new-entries profile

Completion criteria:

- profile selection improves robustness without breaking determinism or explainability

## Not In Scope For Early Phases

- end-to-end black-box trading models
- RL that directly emits buy/sell/exit decisions
- intraday self-training on the same day’s results
- any live-only model feature not reproducible in historical backtests

## Definition Of Done

This plan is complete only when all of the following are true:

- the ML layer improves out-of-sample full-year robustness
- the backtest engine can replay promoted ML artifacts exactly
- rolling-window retraining and promotion happen automatically
- drift handling can safely downweight or disable stale models
- `ANNA`, `AFJK`, and the bad-week regression suite are still explicitly tracked
