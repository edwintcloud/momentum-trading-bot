package alpaca

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FloatStore stores float (shares available to trade) data per symbol.
// Float values of 0 mean "unknown" and should not be used for filtering.
type FloatStore struct {
	floats sync.Map
}

// NewFloatStore creates an empty FloatStore.
func NewFloatStore() *FloatStore {
	return &FloatStore{
		floats: sync.Map{},
	}
}

// Set stores the float for a symbol.
func (fs *FloatStore) Set(symbol string, shares int64) {
	fs.floats.Store(strings.ToUpper(symbol), shares)
}

// Get returns the float for a symbol, or 0 if unknown.
func (fs *FloatStore) Get(symbol string) int64 {
	v, ok := fs.floats.Load(strings.ToUpper(symbol))
	if !ok {
		return 0
	}
	return v.(int64)
}

// Len returns the number of symbols with float data.
func (fs *FloatStore) Len() int {
	count := 0
	fs.floats.Range(func(key, value any) bool {
		count++
		return true
	})
	return count
}

// LoadFromCSV loads float data from a CSV source (file path or URL).
// Expected format: one header row (optional, skipped if first field is not numeric),
// then lines of "SYMBOL,FLOAT" where FLOAT is an integer share count.
func (fs *FloatStore) LoadFromCSV(source string) error {
	var reader io.ReadCloser

	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		httpClient := &http.Client{Timeout: 30 * time.Second}
		resp, err := httpClient.Get(source)
		if err != nil {
			return fmt.Errorf("fetch float CSV: %w", err)
		}
		reader = resp.Body
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("fetch float CSV: status %d", resp.StatusCode)
		}
	} else {
		f, err := os.Open(source)
		if err != nil {
			return fmt.Errorf("open float CSV: %w", err)
		}
		reader = f
	}
	defer reader.Close()

	loaded := 0
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ",", 2)
		if len(parts) != 2 {
			continue
		}
		symbol := strings.TrimSpace(parts[0])
		valueStr := strings.TrimSpace(parts[1])
		if symbol == "" || valueStr == "" {
			continue
		}
		// Skip header row
		if strings.EqualFold(symbol, "symbol") {
			continue
		}
		shares, err := strconv.ParseInt(valueStr, 10, 64)
		if err != nil {
			// Try parsing as float and truncate
			f, ferr := strconv.ParseFloat(valueStr, 64)
			if ferr != nil {
				continue
			}
			shares = int64(f)
		}
		if shares > 0 {
			fs.Set(symbol, shares)
			loaded++
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan float CSV: %w", err)
	}

	return nil
}
