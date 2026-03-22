package alpaca

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFloatStore_SetGet(t *testing.T) {
	fs := NewFloatStore()
	if got := fs.Get("AAPL"); got != 0 {
		t.Errorf("Get unknown symbol: got %d, want 0", got)
	}

	fs.Set("AAPL", 5_000_000)
	if got := fs.Get("AAPL"); got != 5_000_000 {
		t.Errorf("Get after Set: got %d, want 5000000", got)
	}

	// Case-insensitive
	fs.Set("msft", 10_000_000)
	if got := fs.Get("MSFT"); got != 10_000_000 {
		t.Errorf("Case-insensitive Get: got %d, want 10000000", got)
	}
}

func TestFloatStore_Len(t *testing.T) {
	fs := NewFloatStore()
	if got := fs.Len(); got != 0 {
		t.Errorf("Len empty: got %d, want 0", got)
	}
	fs.Set("AAPL", 5_000_000)
	fs.Set("MSFT", 10_000_000)
	if got := fs.Len(); got != 2 {
		t.Errorf("Len after 2 sets: got %d, want 2", got)
	}
}

func TestFloatStore_Overwrite(t *testing.T) {
	fs := NewFloatStore()
	fs.Set("AAPL", 5_000_000)
	fs.Set("AAPL", 3_000_000)
	if got := fs.Get("AAPL"); got != 3_000_000 {
		t.Errorf("Overwrite: got %d, want 3000000", got)
	}
}

func TestFloatStore_LoadFromCSV(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "floats.csv")

	content := `symbol,float
AAPL,5000000
TSLA,3500000
# comment line
MSFT,10000000
INVALID_LINE
,100
GOOG,0
`
	if err := os.WriteFile(csvPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := NewFloatStore()
	if err := fs.LoadFromCSV(csvPath); err != nil {
		t.Fatalf("LoadFromCSV: %v", err)
	}

	tests := []struct {
		symbol string
		want   int64
	}{
		{"AAPL", 5_000_000},
		{"TSLA", 3_500_000},
		{"MSFT", 10_000_000},
		{"GOOG", 0},     // 0 shares not stored
		{"UNKNOWN", 0},  // not in CSV
	}
	for _, tt := range tests {
		if got := fs.Get(tt.symbol); got != tt.want {
			t.Errorf("Get(%s): got %d, want %d", tt.symbol, got, tt.want)
		}
	}

	if got := fs.Len(); got != 3 {
		t.Errorf("Len: got %d, want 3 (AAPL, TSLA, MSFT)", got)
	}
}

func TestFloatStore_LoadFromCSV_FloatValues(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "floats.csv")

	content := `symbol,float
AAPL,5000000.5
TSLA,3500000.9
`
	if err := os.WriteFile(csvPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := NewFloatStore()
	if err := fs.LoadFromCSV(csvPath); err != nil {
		t.Fatalf("LoadFromCSV: %v", err)
	}

	if got := fs.Get("AAPL"); got != 5_000_000 {
		t.Errorf("AAPL: got %d, want 5000000 (float truncated)", got)
	}
	if got := fs.Get("TSLA"); got != 3_500_000 {
		t.Errorf("TSLA: got %d, want 3500000 (float truncated)", got)
	}
}

func TestFloatStore_LoadFromCSV_NoHeader(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "floats.csv")

	content := `AAPL,5000000
TSLA,3500000
`
	if err := os.WriteFile(csvPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := NewFloatStore()
	if err := fs.LoadFromCSV(csvPath); err != nil {
		t.Fatalf("LoadFromCSV: %v", err)
	}

	if got := fs.Get("AAPL"); got != 5_000_000 {
		t.Errorf("AAPL: got %d, want 5000000", got)
	}
	if got := fs.Len(); got != 2 {
		t.Errorf("Len: got %d, want 2", got)
	}
}

func TestFloatStore_LoadFromCSV_MissingFile(t *testing.T) {
	fs := NewFloatStore()
	err := fs.LoadFromCSV("/nonexistent/path/floats.csv")
	if err == nil {
		t.Error("LoadFromCSV on missing file: expected error")
	}
}
