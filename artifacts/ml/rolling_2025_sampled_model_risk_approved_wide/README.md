## Current Kept ML Artifact

This directory contains the current kept advisory model artifact that was previously only stored under `.cache/ml/rolling_2025_sampled_model_risk_approved_wide`.

Files:
- `long_model.json`
- `short_model.json`
- `training_report.json`
- `regression_summary.json`

Notes:
- runtime/backtest inference loads only `long_model.json` and `short_model.json` from `MLModelPath`
- `training_report.json` is provenance/diagnostic metadata; its `inputPaths` may still reference the original `.cache` training dataset locations and are not required at runtime

Current advisory baseline paired with this artifact:
- `MLAdvisoryLongDownsizeThreshold = 0.62`
- `MLAdvisoryShortDownsizeThreshold = 0.45`
- top-ranked long entries remain protected from automatic ML downsizing

Validation status at the time this artifact was copied here:
- weekly annual `2025` rules-only: `-2073.21`
- weekly annual `2025` with ML advisory: `-1633.09`
- annual weekly net improvement: `+440.12`

The heavier per-window backtest outputs remain under `.cache/backtest/` because they are generated artifacts, but this directory is the committed source-of-truth model artifact path for the current checkpoint.
