package backtest

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
)

// LoadCSV reads bars from a CSV file.
func LoadCSV(path string, start, end time.Time) ([]domain.Tick, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read csv header: %w", err)
	}

	colMap := make(map[string]int)
	for i, col := range header {
		colMap[strings.TrimSpace(strings.ToLower(col))] = i
	}

	required := []string{"timestamp", "symbol", "open", "high", "low", "close", "volume"}
	for _, col := range required {
		if _, ok := colMap[col]; !ok {
			return nil, fmt.Errorf("missing required column: %s", col)
		}
	}

	var bars []domain.Tick
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		ts, err := parseTimestamp(record[colMap["timestamp"]])
		if err != nil {
			continue
		}
		if ts.Before(start) || ts.After(end) {
			continue
		}

		open, _ := strconv.ParseFloat(record[colMap["open"]], 64)
		high, _ := strconv.ParseFloat(record[colMap["high"]], 64)
		low, _ := strconv.ParseFloat(record[colMap["low"]], 64)
		close_, _ := strconv.ParseFloat(record[colMap["close"]], 64)
		volume, _ := strconv.ParseInt(record[colMap["volume"]], 10, 64)

		tick := domain.Tick{
			Symbol:    strings.ToUpper(strings.TrimSpace(record[colMap["symbol"]])),
			Price:     close_,
			BarOpen:   open,
			BarHigh:   high,
			BarLow:    low,
			Open:      open,
			HighOfDay: high,
			Volume:    volume,
			Timestamp: ts,
		}

		if idx, ok := colMap["prev_close"]; ok && idx < len(record) {
			prevClose, _ := strconv.ParseFloat(record[idx], 64)
			if prevClose > 0 {
				tick.GapPercent = (open - prevClose) / prevClose * 100
			}
		}
		if idx, ok := colMap["catalyst"]; ok && idx < len(record) {
			tick.Catalyst = record[idx]
		}
		if idx, ok := colMap["catalyst_url"]; ok && idx < len(record) {
			tick.CatalystURL = record[idx]
		}

		bars = append(bars, tick)
	}

	return bars, nil
}

func parseTimestamp(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	s = strings.TrimSpace(s)
	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp: %s", s)
}
