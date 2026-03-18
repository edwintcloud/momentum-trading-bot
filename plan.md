1. *Update `hydrateBatch` function in `internal/market/engine.go`.*
   - Refactor the code around line 363 to use concurrent goroutines when fetching premarket volumes for multiple days.
   - Use a `sync.WaitGroup` to wait for all the day requests to complete.
   - Use a `sync.Mutex` (or channels) to safely aggregate the results into the `volumes` map.
   - Wait for rate-limit slots appropriately without tying up multiple slots unexpectedly. Wait for hydration slot before launching the goroutine if necessary.
2. *Verify the changes in `internal/market/engine.go`.*
   - Use `read_file` to view `internal/market/engine.go` and verify the concurrency modifications were written correctly.
3. *Run the benchmark to measure performance improvement.*
   - Re-run `BenchmarkHydrateBatch` in `internal/market/engine_test.go` to ensure that it speeds up and establish a baseline and comparison.
4. *Run the full test suite.*
   - Execute all relevant tests (`go test ./...`) to guarantee no regressions were introduced.
5. *Complete pre commit steps.*
   - Complete pre-commit steps to ensure proper testing, verification, review, and reflection are done.
6. *Submit the change.*
   - Submit the PR with the performance improvements documented.
