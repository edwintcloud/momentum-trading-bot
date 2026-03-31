package alpaca

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
)

const (
	sipStreamURL       = "wss://stream.data.alpaca.markets/v2/sip"
	statsLogInterval   = 60 * time.Second
	subscribeBatchSize = 500
	subscribeBatchWait = 100 * time.Millisecond
)

// StreamConfig holds WebSocket streaming configuration.
type StreamConfig struct {
	APIKey    string
	APISecret string
}

// streamStats tracks debug counters for stream health monitoring.
type streamStats struct {
	mu             sync.Mutex
	barsReceived   int64
	updatedBars    int64
	dailyBarsRecv  int64
	tradesReceived int64
	subscriptions  int64
	droppedBars    int64
	lastBarAt      time.Time
}

func (s *streamStats) recordBar(t time.Time) {
	s.mu.Lock()
	s.barsReceived++
	s.lastBarAt = t
	s.mu.Unlock()
}

func (s *streamStats) recordUpdatedBar() {
	s.mu.Lock()
	s.updatedBars++
	s.mu.Unlock()
}

func (s *streamStats) recordDailyBar() {
	s.mu.Lock()
	s.dailyBarsRecv++
	s.mu.Unlock()
}

func (s *streamStats) recordTrade() {
	s.mu.Lock()
	s.tradesReceived++
	s.mu.Unlock()
}

func (s *streamStats) recordSubscription() {
	s.mu.Lock()
	s.subscriptions++
	s.mu.Unlock()
}

func (s *streamStats) recordDrop() {
	s.mu.Lock()
	s.droppedBars++
	s.mu.Unlock()
}

func (s *streamStats) snapshot() (bars, updated, daily, trades, subs, dropped int64, lastBar time.Time) {
	s.mu.Lock()
	bars = s.barsReceived
	updated = s.updatedBars
	daily = s.dailyBarsRecv
	trades = s.tradesReceived
	subs = s.subscriptions
	dropped = s.droppedBars
	lastBar = s.lastBarAt
	s.mu.Unlock()
	return
}

// Stream manages a real-time WebSocket connection to Alpaca market data.
type Stream struct {
	config       StreamConfig
	mu           sync.Mutex
	barSymbols   map[string]bool
	tradeSymbols map[string]bool
	bars         chan stream.Bar
	trades       chan stream.Trade
	dailyBars    chan stream.Bar
	stats        streamStats
	streamClient *stream.StocksClient
}

// NewStream creates a new Alpaca market data stream.
func NewStream(cfg StreamConfig, bufSize int) *Stream {
	if bufSize <= 0 {
		bufSize = 4096
	}
	return &Stream{
		config:       cfg,
		barSymbols:   make(map[string]bool),
		tradeSymbols: make(map[string]bool),
		bars:         make(chan stream.Bar, bufSize),
		trades:       make(chan stream.Trade, 2048),
		dailyBars:    make(chan stream.Bar, 1024),
		streamClient: stream.NewStocksClient(dataFeed,
			stream.WithCredentials(cfg.APIKey, cfg.APISecret),
		),
	}
}

// DailyBars returns the channel for receiving daily bar updates.
func (s *Stream) DailyBars() <-chan stream.Bar {
	return s.dailyBars
}

// Trades returns the channel for receiving real-time trade ticks.
func (s *Stream) Trades() <-chan stream.Trade {
	return s.trades
}

// Start connects to the WebSocket, authenticates, and begins reading market data.
// Bars are sent to the returned channel. Handles reconnection automatically.
func (s *Stream) Start(ctx context.Context) (<-chan stream.Bar, error) {
	if err := s.connect(ctx); err != nil {
		return nil, fmt.Errorf("initial connection: %w", err)
	}

	go s.statsLoop(ctx)

	return s.bars, nil
}

// Subscribe adds symbols to the active subscription.
// Subscribes to bars, updatedBars (automatic with bars), and dailyBars.
func (s *Stream) Subscribe(ctx context.Context, symbols []string) error {
	s.mu.Lock()
	for _, sym := range symbols {
		s.barSymbols[sym] = true
	}
	s.mu.Unlock()
	if err := s.writeSymbolBatches("subscribe", symbols, "bars", "dailyBars"); err != nil {
		return err
	}
	log.Printf("stream: subscribed to %d symbols (bars + dailyBars)", len(symbols))
	return nil
}

// SyncTradeSubscriptions keeps trade subscriptions aligned with the current open positions.
func (s *Stream) SyncTradeSubscriptions(ctx context.Context, symbols []string) error {
	desired := make(map[string]bool, len(symbols))
	for _, sym := range symbols {
		if sym == "" {
			continue
		}
		desired[sym] = true
	}

	subscribe := make([]string, 0)
	unsubscribe := make([]string, 0)

	s.mu.Lock()
	for sym := range desired {
		if !s.tradeSymbols[sym] {
			subscribe = append(subscribe, sym)
		}
	}
	for sym := range s.tradeSymbols {
		if !desired[sym] {
			unsubscribe = append(unsubscribe, sym)
		}
	}
	for _, sym := range subscribe {
		s.tradeSymbols[sym] = true
	}
	for _, sym := range unsubscribe {
		delete(s.tradeSymbols, sym)
	}
	s.mu.Unlock()

	if err := s.writeSymbolBatches("subscribe", subscribe, "trades"); err != nil {
		return err
	}
	if err := s.writeSymbolBatches("unsubscribe", unsubscribe, "trades"); err != nil {
		return err
	}
	return nil
}

func (s *Stream) connect(ctx context.Context) error {
	log.Printf("stream: connecting to %s\n", sipStreamURL)

	err := s.streamClient.Connect(ctx)
	if err != nil {
		return fmt.Errorf("stream: %w", err)
	}

	log.Println("stream: connected and authenticated")
	return nil
}

func (s *Stream) onMinuteBar(bar stream.Bar) {
	// Log the very first bar received
	s.stats.mu.Lock()
	isFirst := s.stats.barsReceived == 0
	s.stats.mu.Unlock()
	if isFirst {
		log.Printf("stream: first bar received — %s at %s price=%.2f vol=%d",
			bar.Symbol, bar.Timestamp.Format("15:04:05"), bar.Close, bar.Volume)
	}

	select {
	case s.bars <- bar:
	default:
		s.stats.recordDrop()
	}
	s.stats.recordBar(bar.Timestamp)
}

func (s *Stream) onUpdatedBar(bar stream.Bar) {
	select {
	case s.bars <- bar:
	default:
		s.stats.recordDrop()
	}
	s.stats.recordUpdatedBar()
}

func (s *Stream) onDailyBar(bar stream.Bar) {
	select {
	case s.dailyBars <- bar:
	default:
		// Drop if dailyBars consumer is backed up
	}
	s.stats.recordDailyBar()
}

func (s *Stream) onTrade(trade stream.Trade) {
	select {
	case s.trades <- trade:
	default:
	}
	s.stats.recordTrade()
}

func (s *Stream) onQuote(quote stream.Quote) {
	// No-op, we don't consume quotes but need to subscribe to get limit price updates for orders
}

func (s *Stream) writeSymbolBatches(action string, symbols []string, fields ...string) error {
	if len(symbols) == 0 {
		return nil
	}

	totalBatches := (len(symbols) + subscribeBatchSize - 1) / subscribeBatchSize
	for i := 0; i < len(symbols); i += subscribeBatchSize {
		end := i + subscribeBatchSize
		if end > len(symbols) {
			end = len(symbols)
		}
		batch := symbols[i:end]
		batchNum := i/subscribeBatchSize + 1

		for _, field := range fields {
			var err error
			switch field {
			case "bars":
				if action == "unsubscribe" {
					err = s.streamClient.UnsubscribeFromBars(batch...)
				} else {
					err = s.streamClient.SubscribeToBars(s.onMinuteBar, batch...)
					s.stats.recordSubscription()
				}
			case "dailyBars":
				if action == "unsubscribe" {
					err = s.streamClient.UnsubscribeFromDailyBars(batch...)
				} else {
					err = s.streamClient.SubscribeToDailyBars(s.onDailyBar, batch...)
					s.stats.recordSubscription()
				}
			case "trades":
				if action == "unsubscribe" {
					err = s.streamClient.UnsubscribeFromTrades(batch...)
				} else {
					err = s.streamClient.SubscribeToTrades(s.onTrade, batch...)
					s.stats.recordSubscription()
				}
			case "updatedBars":
				if action == "unsubscribe" {
					err = s.streamClient.UnsubscribeFromUpdatedBars(batch...)
				} else {
					err = s.streamClient.SubscribeToUpdatedBars(s.onUpdatedBar, batch...)
					s.stats.recordSubscription()
				}
			case "quotes":
				if action == "unsubscribe" {
					err = s.streamClient.UnsubscribeFromQuotes(batch...)
				} else {
					err = s.streamClient.SubscribeToQuotes(s.onQuote, batch...)
					s.stats.recordSubscription()
				}
			default:
				err = fmt.Errorf("unknown field %q", field)
			}
			if err != nil {
				return fmt.Errorf("%s batch: %w", action, err)
			}
		}

		log.Printf("stream: %s request sent for %d symbols (batch %d/%d fields=%v)", action, len(batch), batchNum, totalBatches, fields)
		if end < len(symbols) {
			time.Sleep(subscribeBatchWait)
		}
	}

	return nil
}

func (s *Stream) statsLoop(ctx context.Context) {
	ticker := time.NewTicker(statsLogInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			bars, updated, daily, trades, subs, dropped, lastBar := s.stats.snapshot()

			lastBarAgo := "never"
			if !lastBar.IsZero() {
				lastBarAgo = time.Since(lastBar).Round(time.Second).String()
			}

			log.Printf("stream: stats — bars=%d updated=%d daily=%d trades=%d subs=%d dropped=%d lastBar=%s",
				bars, updated, daily, trades, subs, dropped, lastBarAgo)
		}
	}
}
