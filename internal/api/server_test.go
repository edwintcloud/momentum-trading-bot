package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

func TestOptimizerStatus_EmptyDir(t *testing.T) {
	s := &Server{optimizerDir: t.TempDir()}

	status := s.optimizerStatus()
	if status.PendingProfileName != "" {
		t.Errorf("expected empty PendingProfileName, got %q", status.PendingProfileName)
	}
	if !status.LastOptimizerRun.IsZero() {
		t.Errorf("expected zero LastOptimizerRun, got %v", status.LastOptimizerRun)
	}
}

func TestOptimizerStatus_CachesFor60Seconds(t *testing.T) {
	dir := t.TempDir()
	s := &Server{optimizerDir: dir}

	// Write a status file
	statusData := map[string]any{
		"lastRun":  time.Date(2026, 3, 20, 6, 0, 0, 0, time.UTC),
		"promoted": true,
	}
	data, _ := json.MarshalIndent(statusData, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "latest-status.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// First call loads from disk
	status1 := s.optimizerStatus()
	if status1.LastOptimizerRun.IsZero() {
		t.Fatal("expected non-zero LastOptimizerRun after first call")
	}

	// Update the file with a new timestamp
	statusData["lastRun"] = time.Date(2026, 3, 21, 6, 0, 0, 0, time.UTC)
	data, _ = json.MarshalIndent(statusData, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "latest-status.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Second call should return cached result (same as first)
	status2 := s.optimizerStatus()
	if !status2.LastOptimizerRun.Equal(status1.LastOptimizerRun) {
		t.Errorf("expected cached result: got %v, want %v", status2.LastOptimizerRun, status1.LastOptimizerRun)
	}

	// Simulate cache expiry by backdating cachedArtifactAt
	s.cachedArtifactAt = time.Now().Add(-61 * time.Second)

	// Third call should re-read from disk
	status3 := s.optimizerStatus()
	expected := time.Date(2026, 3, 21, 6, 0, 0, 0, time.UTC)
	if !status3.LastOptimizerRun.Equal(expected) {
		t.Errorf("expected refreshed result: got %v, want %v", status3.LastOptimizerRun, expected)
	}
}

func TestOptimizerStatus_ReadsArtifactFields(t *testing.T) {
	dir := t.TempDir()
	s := &Server{optimizerDir: dir}

	// Write a report JSON in the format LoadArtifactStatus expects
	reportData := map[string]any{
		"generatedAt": time.Date(2026, 3, 20, 6, 0, 0, 0, time.UTC),
		"recommendation": map[string]any{
			"profileName": "high_conviction_breakout",
			"config": map[string]any{
				"strategyProfileVersion": "v3",
			},
		},
	}

	data, _ := json.MarshalIndent(reportData, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "latest-report.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	status := s.optimizerStatus()
	if status.PendingProfileName != "high_conviction_breakout" {
		t.Errorf("PendingProfileName = %q, want %q", status.PendingProfileName, "high_conviction_breakout")
	}
	if status.PendingProfileVersion != "v3" {
		t.Errorf("PendingProfileVersion = %q, want %q", status.PendingProfileVersion, "v3")
	}
}

// mockHistoryLoader implements domain.HistoryLoader for tests.
type mockHistoryLoader struct {
	trades map[string][]domain.ClosedTrade
	dates  []string
}

func (m *mockHistoryLoader) LoadClosedTradesByDate(date time.Time) ([]domain.ClosedTrade, error) {
	key := date.In(markethours.Location()).Format("2006-01-02")
	return m.trades[key], nil
}

func (m *mockHistoryLoader) ListTradeDates() ([]string, error) {
	return m.dates, nil
}

func TestHandleTradeHistory_ValidDate(t *testing.T) {
	loc := markethours.Location()
	loader := &mockHistoryLoader{
		trades: map[string][]domain.ClosedTrade{
			"2026-03-20": {
				{Symbol: "AAPL", Side: "long", PnL: 500, ClosedAt: time.Date(2026, 3, 20, 10, 0, 0, 0, loc)},
				{Symbol: "TSLA", Side: "short", PnL: -100, ClosedAt: time.Date(2026, 3, 20, 14, 0, 0, 0, loc)},
			},
		},
	}
	s := &Server{historyLoader: loader}

	req := httptest.NewRequest("GET", "/api/trades/history?date=2026-03-20", nil)
	w := httptest.NewRecorder()
	s.handleTradeHistory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var trades []domain.ClosedTrade
	if err := json.NewDecoder(w.Body).Decode(&trades); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(trades))
	}
	if trades[0].Symbol != "AAPL" {
		t.Errorf("expected first trade AAPL, got %s", trades[0].Symbol)
	}
}

func TestHandleTradeHistory_InvalidDate(t *testing.T) {
	s := &Server{historyLoader: &mockHistoryLoader{}}

	req := httptest.NewRequest("GET", "/api/trades/history?date=bad-date", nil)
	w := httptest.NewRecorder()
	s.handleTradeHistory(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid date, got %d", w.Code)
	}
}

func TestHandleTradeExport_CSV(t *testing.T) {
	loc := markethours.Location()
	loader := &mockHistoryLoader{
		trades: map[string][]domain.ClosedTrade{
			"2026-03-20": {
				{
					Symbol: "AAPL", Side: "long", Quantity: 100,
					EntryPrice: 150, ExitPrice: 155, PnL: 500,
					RMultiple: 2.0, SetupType: "breakout", ExitReason: "profit-target",
					Playbook: "breakout", MarketRegime: "trending", Sector: "Technology",
					OpenedAt: time.Date(2026, 3, 20, 9, 30, 0, 0, loc),
					ClosedAt: time.Date(2026, 3, 20, 10, 30, 0, 0, loc),
				},
			},
		},
	}
	s := &Server{historyLoader: loader}

	req := httptest.NewRequest("GET", "/api/trades/export?date=2026-03-20", nil)
	w := httptest.NewRecorder()
	s.handleTradeExport(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Verify Content-Type
	ct := w.Header().Get("Content-Type")
	if ct != "text/csv" {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}

	// Verify Content-Disposition
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "trades_2026-03-20.csv") {
		t.Errorf("Content-Disposition = %q, want to contain trades_2026-03-20.csv", cd)
	}

	// Verify CSV content has header + 1 data row
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 CSV lines (header + 1 row), got %d", len(lines))
	}

	// Header should have 15 columns
	headerCols := strings.Split(lines[0], ",")
	if len(headerCols) != 15 {
		t.Errorf("expected 15 CSV header columns, got %d", len(headerCols))
	}

	// Data row should also have 15 columns
	dataCols := strings.Split(lines[1], ",")
	if len(dataCols) != 15 {
		t.Errorf("expected 15 CSV data columns, got %d", len(dataCols))
	}

	// Verify duration column is populated
	durationCol := dataCols[14]
	if durationCol == "" {
		t.Error("expected non-empty duration column")
	}
}

func TestHandleTradeExport_InvalidDate(t *testing.T) {
	s := &Server{historyLoader: &mockHistoryLoader{}}

	req := httptest.NewRequest("GET", "/api/trades/export?date=not-a-date", nil)
	w := httptest.NewRecorder()
	s.handleTradeExport(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid date, got %d", w.Code)
	}
}

func TestHandleTradeDates(t *testing.T) {
	loader := &mockHistoryLoader{
		dates: []string{"2026-03-21", "2026-03-20", "2026-03-18"},
	}
	s := &Server{historyLoader: loader}

	req := httptest.NewRequest("GET", "/api/trades/dates", nil)
	w := httptest.NewRecorder()
	s.handleTradeDates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var dates []string
	if err := json.NewDecoder(w.Body).Decode(&dates); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(dates) != 3 {
		t.Fatalf("expected 3 dates, got %d", len(dates))
	}
	if dates[0] != "2026-03-21" {
		t.Errorf("expected first date 2026-03-21, got %s", dates[0])
	}
}

func TestHandleTradeDates_NoHistoryLoader(t *testing.T) {
	s := &Server{} // no historyLoader set

	req := httptest.NewRequest("GET", "/api/trades/dates", nil)
	w := httptest.NewRecorder()
	s.handleTradeDates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var dates []string
	if err := json.NewDecoder(w.Body).Decode(&dates); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Should return today's date as fallback
	if len(dates) != 1 {
		t.Fatalf("expected 1 fallback date, got %d", len(dates))
	}
}
