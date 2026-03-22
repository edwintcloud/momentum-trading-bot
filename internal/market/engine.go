package market

import (
	"context"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
)

// DataSource abstracts market data providers.
type DataSource interface {
	StreamBars(ctx context.Context, symbols []string, out chan<- domain.Tick) error
}

// Engine normalizes market data from any source.
type Engine struct {
	source DataSource
}

// NewEngine creates a market data engine.
func NewEngine(source DataSource) *Engine {
	return &Engine{source: source}
}

// Start begins streaming market data into the pipeline.
func (e *Engine) Start(ctx context.Context, symbols []string, out chan<- domain.Tick) error {
	return e.source.StreamBars(ctx, symbols, out)
}

// TickFromBar converts an Alpaca-style bar into a normalized Tick.
func TickFromBar(symbol string, open, high, low, close float64, volume int64, ts time.Time) domain.Tick {
	return domain.Tick{
		Symbol:    symbol,
		Price:     close,
		BarOpen:   open,
		BarHigh:   high,
		BarLow:    low,
		Open:      open,
		HighOfDay: high,
		Volume:    volume,
		Timestamp: ts,
	}
}
