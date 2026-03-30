package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	sipStreamURL       = "wss://stream.data.alpaca.markets/v2/sip"
	streamPingInterval = 30 * time.Second
	maxReconnectDelay  = 30 * time.Second
	statsLogInterval   = 60 * time.Second
	subscribeBatchSize = 500
	subscribeBatchWait = 100 * time.Millisecond
)

// StreamConfig holds WebSocket streaming configuration.
type StreamConfig struct {
	APIKey    string
	APISecret string
	Feed      string // "sip" (always, since we require paid subscription)
}

// StreamBar is a real-time bar from the Alpaca WebSocket.
type StreamBar struct {
	Type       string    `json:"T"` // Message type: "b" (bar), "u" (updated bar), "d" (daily bar)
	Symbol     string    `json:"S"`
	Open       float64   `json:"o"`
	High       float64   `json:"h"`
	Low        float64   `json:"l"`
	Close      float64   `json:"c"`
	Volume     uint64    `json:"v"`
	Timestamp  time.Time `json:"t"`
	TradeCount int64     `json:"n"`
	VWAP       float64   `json:"vw"`
}

// StreamTrade is a real-time trade tick from the Alpaca WebSocket.
type StreamTrade struct {
	Type      string    `json:"T"` // Message type: "t"
	Symbol    string    `json:"S"`
	Price     float64   `json:"p"`
	Size      int64     `json:"s"`
	Timestamp time.Time `json:"t"`
}

// streamStats tracks debug counters for stream health monitoring.
type streamStats struct {
	mu             sync.Mutex
	barsReceived   int64
	updatedBars    int64
	dailyBarsRecv  int64
	tradesReceived int64
	errorsReceived int64
	subscriptions  int64
	droppedBars    int64
	lastBarAt      time.Time
	lastErrorMsg   string
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

func (s *streamStats) recordError(msg string) {
	s.mu.Lock()
	s.errorsReceived++
	s.lastErrorMsg = msg
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

func (s *streamStats) snapshot() (bars, updated, daily, trades, errors, subs, dropped int64, lastBar time.Time, lastErr string) {
	s.mu.Lock()
	bars = s.barsReceived
	updated = s.updatedBars
	daily = s.dailyBarsRecv
	trades = s.tradesReceived
	errors = s.errorsReceived
	subs = s.subscriptions
	dropped = s.droppedBars
	lastBar = s.lastBarAt
	lastErr = s.lastErrorMsg
	s.mu.Unlock()
	return
}

// Stream manages a real-time WebSocket connection to Alpaca market data.
type Stream struct {
	config       StreamConfig
	conn         *websocket.Conn
	mu           sync.Mutex
	barSymbols   map[string]bool
	tradeSymbols map[string]bool
	bars         chan StreamBar
	trades       chan StreamTrade
	dailyBars    chan StreamBar
	done         chan struct{}
	reconnectCh  chan struct{}
	stats        streamStats
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
		bars:         make(chan StreamBar, bufSize),
		trades:       make(chan StreamTrade, 2048),
		dailyBars:    make(chan StreamBar, 1024),
		done:         make(chan struct{}),
		reconnectCh:  make(chan struct{}, 1),
	}
}

// DailyBars returns the channel for receiving daily bar updates.
func (s *Stream) DailyBars() <-chan StreamBar {
	return s.dailyBars
}

// Trades returns the channel for receiving real-time trade ticks.
func (s *Stream) Trades() <-chan StreamTrade {
	return s.trades
}

// Start connects to the WebSocket, authenticates, and begins reading market data.
// Bars are sent to the returned channel. Handles reconnection automatically.
func (s *Stream) Start(ctx context.Context) (<-chan StreamBar, error) {
	if err := s.connect(ctx); err != nil {
		return nil, fmt.Errorf("initial connection: %w", err)
	}

	go s.readLoop(ctx)
	go s.pingLoop(ctx)
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

// Close cleanly shuts down the stream.
func (s *Stream) Close() error {
	select {
	case <-s.done:
		return nil
	default:
		close(s.done)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

func (s *Stream) connect(ctx context.Context) error {
	log.Printf("stream: connecting to %s", sipStreamURL)

	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, sipStreamURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	// Read welcome message
	_, _, err = conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("read welcome: %w", err)
	}

	// Authenticate
	authMsg := map[string]string{
		"action": "auth",
		"key":    s.config.APIKey,
		"secret": s.config.APISecret,
	}
	if err := conn.WriteJSON(authMsg); err != nil {
		conn.Close()
		return fmt.Errorf("write auth: %w", err)
	}

	// Read auth response
	_, msgBytes, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("read auth response: %w", err)
	}

	var authResp []struct {
		T   string `json:"T"`
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(msgBytes, &authResp); err != nil {
		conn.Close()
		return fmt.Errorf("parse auth response: %w", err)
	}
	if len(authResp) == 0 || authResp[0].T != "success" || authResp[0].Msg != "authenticated" {
		conn.Close()
		return fmt.Errorf("auth failed: %s", string(msgBytes))
	}

	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	log.Println("stream: connected and authenticated")
	return nil
}

func (s *Stream) resubscribe(ctx context.Context) error {
	s.mu.Lock()
	barSymbols := make([]string, 0, len(s.barSymbols))
	for sym := range s.barSymbols {
		barSymbols = append(barSymbols, sym)
	}
	tradeSymbols := make([]string, 0, len(s.tradeSymbols))
	for sym := range s.tradeSymbols {
		tradeSymbols = append(tradeSymbols, sym)
	}
	s.mu.Unlock()

	if err := s.writeSymbolBatches("subscribe", barSymbols, "bars", "dailyBars"); err != nil {
		return err
	}
	return s.writeSymbolBatches("subscribe", tradeSymbols, "trades")
}

func (s *Stream) readLoop(ctx context.Context) {
	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			return
		default:
		}

		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()

		if conn == nil {
			s.reconnect(ctx)
			continue
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("stream: read error: %v", err)
			s.reconnect(ctx)
			continue
		}

		s.processMessage(msg)
	}
}

func (s *Stream) processMessage(msg []byte) {
	var messages []json.RawMessage
	if err := json.Unmarshal(msg, &messages); err != nil {
		log.Printf("stream: failed to parse message: %v (raw: %s)", err, truncate(msg, 200))
		return
	}

	for _, raw := range messages {
		// Use a map to extract the "T" key with exact case sensitivity.
		// Go's json.Unmarshal struct matching is case-insensitive, which
		// causes "t" (timestamp) to overwrite "T" (message type) since
		// Alpaca messages contain both keys.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			continue
		}

		var msgType string
		if tField, ok := fields["T"]; ok {
			json.Unmarshal(tField, &msgType)
		}

		// Extract error fields if present
		var errCode int
		var errMsg string
		if codeField, ok := fields["code"]; ok {
			json.Unmarshal(codeField, &errCode)
		}
		if msgField, ok := fields["msg"]; ok {
			json.Unmarshal(msgField, &errMsg)
		}

		switch msgType {
		case "b": // Minute bar
			var bar StreamBar
			if err := json.Unmarshal(raw, &bar); err != nil {
				log.Printf("stream: failed to parse bar: %v", err)
				continue
			}

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

		case "u": // Updated bar (late trade correction) — same schema as minute bar
			var bar StreamBar
			if err := json.Unmarshal(raw, &bar); err != nil {
				log.Printf("stream: failed to parse updated bar: %v", err)
				continue
			}
			select {
			case s.bars <- bar: // Route to same channel — normalizer handles update
			default:
				s.stats.recordDrop()
			}
			s.stats.recordUpdatedBar()

		case "d": // Daily bar
			var bar StreamBar
			if err := json.Unmarshal(raw, &bar); err != nil {
				log.Printf("stream: failed to parse daily bar: %v", err)
				continue
			}
			select {
			case s.dailyBars <- bar:
			default:
				// Drop if dailyBars consumer is backed up
			}
			s.stats.recordDailyBar()

		case "t": // Trade (if subscribed)
			var trade StreamTrade
			if err := json.Unmarshal(raw, &trade); err != nil {
				log.Printf("stream: failed to parse trade: %v", err)
				continue
			}
			select {
			case s.trades <- trade:
			default:
			}
			s.stats.recordTrade()

		case "subscription": // Subscription confirmation
			var sub struct {
				T           string   `json:"T"`
				Trades      []string `json:"trades"`
				Quotes      []string `json:"quotes"`
				Bars        []string `json:"bars"`
				UpdatedBars []string `json:"updatedBars"`
				DailyBars   []string `json:"dailyBars"`
				Statuses    []string `json:"statuses"`
			}
			if err := json.Unmarshal(raw, &sub); err == nil {
				log.Printf("stream: subscription confirmed — bars=%d updatedBars=%d dailyBars=%d trades=%d",
					len(sub.Bars), len(sub.UpdatedBars), len(sub.DailyBars), len(sub.Trades))
			}
			s.stats.recordSubscription()

		case "error": // Error from Alpaca
			log.Printf("stream: ERROR from Alpaca — code=%d msg=%s", errCode, errMsg)
			s.stats.recordError(errMsg)

		case "success": // Auth or other success
			log.Printf("stream: success — %s", errMsg)

		default:
			log.Printf("stream: unknown message type %q: %s", msgType, truncate(raw, 200))
		}
	}
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
		msg := map[string]interface{}{
			"action": action,
		}
		for _, field := range fields {
			msg[field] = batch
		}

		s.mu.Lock()
		conn := s.conn
		if conn == nil {
			s.mu.Unlock()
			return nil
		}
		err := conn.WriteJSON(msg)
		s.mu.Unlock()
		if err != nil {
			return fmt.Errorf("%s batch: %w", action, err)
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
		case <-s.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			bars, updated, daily, trades, errors, subs, dropped, lastBar, lastErr := s.stats.snapshot()

			lastBarAgo := "never"
			if !lastBar.IsZero() {
				lastBarAgo = time.Since(lastBar).Round(time.Second).String()
			}

			log.Printf("stream: stats — bars=%d updated=%d daily=%d trades=%d errors=%d subs=%d dropped=%d lastBar=%s",
				bars, updated, daily, trades, errors, subs, dropped, lastBarAgo)
			if lastErr != "" {
				log.Printf("stream: last error — %s", lastErr)
			}
		}
	}
}

func (s *Stream) reconnect(ctx context.Context) {
	s.mu.Lock()
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
	s.mu.Unlock()

	delay := time.Second
	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			return
		default:
		}

		log.Printf("stream: reconnecting in %v", delay)
		select {
		case <-time.After(delay):
		case <-s.done:
			return
		case <-ctx.Done():
			return
		}

		if err := s.connect(ctx); err != nil {
			log.Printf("stream: reconnect failed: %v", err)
			delay *= 2
			if delay > maxReconnectDelay {
				delay = maxReconnectDelay
			}
			continue
		}

		if err := s.resubscribe(ctx); err != nil {
			log.Printf("stream: resubscribe failed: %v", err)
			continue
		}

		log.Println("stream: reconnected successfully")
		return
	}
}

func (s *Stream) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(streamPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			conn := s.conn
			s.mu.Unlock()
			if conn != nil {
				s.mu.Lock()
				err := conn.WriteMessage(websocket.PingMessage, nil)
				s.mu.Unlock()
				if err != nil {
					log.Printf("stream: ping error: %v", err)
				}
			}
		}
	}
}

// truncate shortens a byte slice for log display.
func truncate(data []byte, maxLen int) string {
	if len(data) <= maxLen {
		return string(data)
	}
	return string(data[:maxLen]) + "..."
}
