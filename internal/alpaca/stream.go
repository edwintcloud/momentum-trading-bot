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
)

// StreamConfig holds WebSocket streaming configuration.
type StreamConfig struct {
	APIKey    string
	APISecret string
	Feed      string // "sip" (always, since we require paid subscription)
}

// StreamBar is a real-time bar from the Alpaca WebSocket.
type StreamBar struct {
	Symbol     string    `json:"S"`
	Open       float64   `json:"o"`
	High       float64   `json:"h"`
	Low        float64   `json:"l"`
	Close      float64   `json:"c"`
	Volume     int64     `json:"v"`
	Timestamp  time.Time `json:"t"`
	TradeCount int64     `json:"n"`
	VWAP       float64   `json:"vw"`
}

// Stream manages a real-time WebSocket connection to Alpaca market data.
type Stream struct {
	config      StreamConfig
	conn        *websocket.Conn
	mu          sync.Mutex
	symbols     map[string]bool
	bars        chan StreamBar
	done        chan struct{}
	reconnectCh chan struct{}
}

// NewStream creates a new Alpaca market data stream.
func NewStream(cfg StreamConfig, bufSize int) *Stream {
	if bufSize <= 0 {
		bufSize = 4096
	}
	return &Stream{
		config:      cfg,
		symbols:     make(map[string]bool),
		bars:        make(chan StreamBar, bufSize),
		done:        make(chan struct{}),
		reconnectCh: make(chan struct{}, 1),
	}
}

// Start connects to the WebSocket, authenticates, and begins reading bars.
// Bars are sent to the returned channel. Handles reconnection automatically.
func (s *Stream) Start(ctx context.Context) (<-chan StreamBar, error) {
	if err := s.connect(ctx); err != nil {
		return nil, fmt.Errorf("initial connection: %w", err)
	}

	go s.readLoop(ctx)
	go s.pingLoop(ctx)

	return s.bars, nil
}

// Subscribe adds symbols to the active subscription.
func (s *Stream) Subscribe(ctx context.Context, symbols []string) error {
	s.mu.Lock()
	for _, sym := range symbols {
		s.symbols[sym] = true
	}
	conn := s.conn
	s.mu.Unlock()

	if conn == nil {
		return nil
	}

	// Subscribe in batches of 500
	for i := 0; i < len(symbols); i += 500 {
		end := i + 500
		if end > len(symbols) {
			end = len(symbols)
		}
		batch := symbols[i:end]
		msg := map[string]interface{}{
			"action": "subscribe",
			"bars":   batch,
		}
		s.mu.Lock()
		err := conn.WriteJSON(msg)
		s.mu.Unlock()
		if err != nil {
			return fmt.Errorf("subscribe batch: %w", err)
		}
	}
	log.Printf("stream: subscribed to %d symbols", len(symbols))
	return nil
}

// Unsubscribe removes symbols from the active subscription.
func (s *Stream) Unsubscribe(ctx context.Context, symbols []string) error {
	s.mu.Lock()
	for _, sym := range symbols {
		delete(s.symbols, sym)
	}
	conn := s.conn
	s.mu.Unlock()

	if conn == nil {
		return nil
	}

	msg := map[string]interface{}{
		"action":      "unsubscribe",
		"bars":        symbols,
	}
	s.mu.Lock()
	err := conn.WriteJSON(msg)
	s.mu.Unlock()
	return err
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
	symbols := make([]string, 0, len(s.symbols))
	for sym := range s.symbols {
		symbols = append(symbols, sym)
	}
	s.mu.Unlock()

	if len(symbols) == 0 {
		return nil
	}

	return s.Subscribe(ctx, symbols)
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
		return
	}

	for _, raw := range messages {
		var header struct {
			T string `json:"T"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			continue
		}

		if header.T == "b" {
			var bar StreamBar
			if err := json.Unmarshal(raw, &bar); err != nil {
				continue
			}
			select {
			case s.bars <- bar:
			default:
				// Drop bar if pipeline is backed up
			}
		}
		// Ignore other message types (subscription confirmations, errors, etc.)
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
