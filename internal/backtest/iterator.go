package backtest

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// InputBarIterator streams historical bars in timestamp order.
type InputBarIterator interface {
	Next() (InputBar, bool, error)
	Close() error
}

type sliceInputBarIterator struct {
	bars []InputBar
	next int
}

func newSliceInputBarIterator(input []InputBar, start, end time.Time) *sliceInputBarIterator {
	bars := make([]InputBar, 0, len(input))
	for _, item := range input {
		entry := normalizeInputBar(item)
		if !withinWindow(entry.Timestamp, start, end) {
			continue
		}
		bars = append(bars, entry)
	}
	sortInputBars(bars)
	return &sliceInputBarIterator{bars: bars}
}

func (it *sliceInputBarIterator) Next() (InputBar, bool, error) {
	if it.next >= len(it.bars) {
		return InputBar{}, false, nil
	}
	item := it.bars[it.next]
	it.next++
	return item, true, nil
}

func (it *sliceInputBarIterator) Close() error {
	return nil
}

func resolveBarIterator(runCfg RunConfig) (InputBarIterator, error) {
	switch {
	case runCfg.Iterator != nil:
		return runCfg.Iterator, nil
	case len(runCfg.Bars) > 0:
		return newSliceInputBarIterator(runCfg.Bars, runCfg.Start, runCfg.End), nil
	case runCfg.DataPath != "":
		bars, err := loadInputBars(runCfg.DataPath, runCfg.Start, runCfg.End)
		if err != nil {
			return nil, err
		}
		return &sliceInputBarIterator{bars: bars}, nil
	default:
		return nil, fmt.Errorf("either data path, historical bars, or a bar iterator is required")
	}
}

func loadInputBars(path string, start, end time.Time) ([]InputBar, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[strings.ToLower(strings.TrimSpace(name))] = index
	}

	bars := make([]InputBar, 0, 4096)
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		entry, parseErr := parseInputBar(columns, row)
		if parseErr != nil {
			return nil, parseErr
		}
		if !withinWindow(entry.Timestamp, start, end) {
			continue
		}
		bars = append(bars, entry)
	}

	sortInputBars(bars)
	return bars, nil
}

func parseInputBar(columns map[string]int, row []string) (InputBar, error) {
	timestamp, err := parseTimestamp(cell(columns, row, "timestamp", "time", "datetime"))
	if err != nil {
		return InputBar{}, err
	}
	open, err := strconv.ParseFloat(cell(columns, row, "open"), 64)
	if err != nil {
		return InputBar{}, err
	}
	high, err := strconv.ParseFloat(cell(columns, row, "high"), 64)
	if err != nil {
		return InputBar{}, err
	}
	low, err := strconv.ParseFloat(cell(columns, row, "low"), 64)
	if err != nil {
		return InputBar{}, err
	}
	closePrice, err := strconv.ParseFloat(cell(columns, row, "close"), 64)
	if err != nil {
		return InputBar{}, err
	}
	volume, err := strconv.ParseInt(cell(columns, row, "volume"), 10, 64)
	if err != nil {
		return InputBar{}, err
	}

	prevClose := 0.0
	if rawPrevClose := cell(columns, row, "prev_close", "previous_close"); rawPrevClose != "" {
		prevClose, _ = strconv.ParseFloat(rawPrevClose, 64)
	}

	return normalizeInputBar(InputBar{
		Timestamp:   timestamp.UTC(),
		Symbol:      cell(columns, row, "symbol"),
		Open:        open,
		High:        high,
		Low:         low,
		Close:       closePrice,
		Volume:      volume,
		PrevClose:   prevClose,
		Catalyst:    cell(columns, row, "catalyst", "headline"),
		CatalystURL: cell(columns, row, "catalyst_url", "headline_url", "url"),
	}), nil
}

func normalizeInputBar(item InputBar) InputBar {
	item.Timestamp = item.Timestamp.UTC()
	item.Symbol = strings.ToUpper(strings.TrimSpace(item.Symbol))
	return item
}

func sortInputBars(bars []InputBar) {
	sort.Slice(bars, func(i, j int) bool {
		if bars[i].Timestamp.Equal(bars[j].Timestamp) {
			return bars[i].Symbol < bars[j].Symbol
		}
		return bars[i].Timestamp.Before(bars[j].Timestamp)
	})
}
