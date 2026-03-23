package alpaca

import (
	"encoding/json"
	"testing"
	"time"
)

func TestProcessMessage_MinuteBar(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)

	msg := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T":  "b",
			"S":  "AAPL",
			"o":  150.0,
			"h":  151.0,
			"l":  149.5,
			"c":  150.5,
			"v":  10000,
			"t":  "2026-03-23T13:31:00Z",
			"n":  50,
			"vw": 150.25,
		},
	})

	s.processMessage(msg)

	// Check bar was routed to bars channel
	select {
	case bar := <-s.bars:
		if bar.Symbol != "AAPL" {
			t.Errorf("bar symbol = %q, want AAPL", bar.Symbol)
		}
		if bar.Close != 150.5 {
			t.Errorf("bar close = %f, want 150.5", bar.Close)
		}
		if bar.Volume != 10000 {
			t.Errorf("bar volume = %d, want 10000", bar.Volume)
		}
	default:
		t.Fatal("expected bar on bars channel, got none")
	}

	// Check stats
	bars, _, _, _, _, _, _, _, _ := s.stats.snapshot()
	if bars != 1 {
		t.Errorf("stats.barsReceived = %d, want 1", bars)
	}
}

func TestProcessMessage_UpdatedBar(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)

	msg := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T":  "u",
			"S":  "TSLA",
			"o":  200.0,
			"h":  205.0,
			"l":  199.0,
			"c":  203.0,
			"v":  50000,
			"t":  "2026-03-23T13:31:00Z",
			"n":  100,
			"vw": 202.0,
		},
	})

	s.processMessage(msg)

	// Updated bars should route to the same bars channel
	select {
	case bar := <-s.bars:
		if bar.Symbol != "TSLA" {
			t.Errorf("updated bar symbol = %q, want TSLA", bar.Symbol)
		}
		if bar.Close != 203.0 {
			t.Errorf("updated bar close = %f, want 203.0", bar.Close)
		}
	default:
		t.Fatal("expected updated bar on bars channel, got none")
	}

	_, updated, _, _, _, _, _, _, _ := s.stats.snapshot()
	if updated != 1 {
		t.Errorf("stats.updatedBars = %d, want 1", updated)
	}
}

func TestProcessMessage_DailyBar(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)

	msg := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T":  "d",
			"S":  "SPY",
			"o":  500.0,
			"h":  505.0,
			"l":  498.0,
			"c":  503.0,
			"v":  80000000,
			"t":  "2026-03-23T13:31:00Z",
			"n":  500000,
			"vw": 502.0,
		},
	})

	s.processMessage(msg)

	// Daily bar should route to dailyBars channel
	select {
	case bar := <-s.dailyBars:
		if bar.Symbol != "SPY" {
			t.Errorf("daily bar symbol = %q, want SPY", bar.Symbol)
		}
		if bar.High != 505.0 {
			t.Errorf("daily bar high = %f, want 505.0", bar.High)
		}
		if bar.Volume != 80000000 {
			t.Errorf("daily bar volume = %d, want 80000000", bar.Volume)
		}
	default:
		t.Fatal("expected daily bar on dailyBars channel, got none")
	}

	_, _, daily, _, _, _, _, _, _ := s.stats.snapshot()
	if daily != 1 {
		t.Errorf("stats.dailyBarsRecv = %d, want 1", daily)
	}
}

func TestProcessMessage_Trade(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)

	msg := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T": "t",
			"S": "AAPL",
			"p": 150.25,
			"s": 100,
			"t": "2026-03-23T13:31:00Z",
		},
	})

	s.processMessage(msg)

	_, _, _, trades, _, _, _, _, _ := s.stats.snapshot()
	if trades != 1 {
		t.Errorf("stats.tradesReceived = %d, want 1", trades)
	}
}

func TestProcessMessage_SubscriptionConfirmation(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)

	msg := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T":           "subscription",
			"trades":      []string{},
			"quotes":      []string{},
			"bars":        []string{"AAPL", "TSLA"},
			"updatedBars": []string{"AAPL", "TSLA"},
			"dailyBars":   []string{"AAPL", "TSLA"},
			"statuses":    []string{},
		},
	})

	s.processMessage(msg)

	_, _, _, _, _, subs, _, _, _ := s.stats.snapshot()
	if subs != 1 {
		t.Errorf("stats.subscriptions = %d, want 1", subs)
	}
}

func TestProcessMessage_Error(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)

	msg := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T":    "error",
			"code": 405,
			"msg":  "symbol limit exceeded",
		},
	})

	s.processMessage(msg)

	_, _, _, _, errors, _, _, _, lastErr := s.stats.snapshot()
	if errors != 1 {
		t.Errorf("stats.errorsReceived = %d, want 1", errors)
	}
	if lastErr != "symbol limit exceeded" {
		t.Errorf("stats.lastErrorMsg = %q, want %q", lastErr, "symbol limit exceeded")
	}
}

func TestProcessMessage_Success(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)

	msg := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T":   "success",
			"msg": "authenticated",
		},
	})

	// Should not panic or produce errors
	s.processMessage(msg)
}

func TestProcessMessage_UnknownType(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)

	msg := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T":    "unknown_type",
			"data": "something",
		},
	})

	// Should not panic
	s.processMessage(msg)
}

func TestProcessMessage_MultipleMessages(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)

	msg := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T": "b", "S": "AAPL", "o": 150.0, "h": 151.0, "l": 149.5, "c": 150.5,
			"v": 10000, "t": "2026-03-23T13:31:00Z", "n": 50, "vw": 150.25,
		},
		map[string]interface{}{
			"T": "b", "S": "TSLA", "o": 200.0, "h": 201.0, "l": 199.0, "c": 200.5,
			"v": 20000, "t": "2026-03-23T13:31:00Z", "n": 100, "vw": 200.25,
		},
		map[string]interface{}{
			"T": "d", "S": "SPY", "o": 500.0, "h": 505.0, "l": 498.0, "c": 503.0,
			"v": 80000000, "t": "2026-03-23T13:31:00Z", "n": 500000, "vw": 502.0,
		},
	})

	s.processMessage(msg)

	bars, _, daily, _, _, _, _, _, _ := s.stats.snapshot()
	if bars != 2 {
		t.Errorf("stats.barsReceived = %d, want 2", bars)
	}
	if daily != 1 {
		t.Errorf("stats.dailyBarsRecv = %d, want 1", daily)
	}
}

func TestProcessMessage_InvalidJSON(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)

	// Should not panic on invalid JSON
	s.processMessage([]byte(`not json at all`))
	s.processMessage([]byte(`{}`))
	s.processMessage([]byte(`[{"invalid}]`))
}

func TestProcessMessage_BarChannelFull(t *testing.T) {
	s := NewStream(StreamConfig{}, 1) // Buffer of 1

	// Fill the channel
	msg1 := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T": "b", "S": "A", "o": 1.0, "h": 1.0, "l": 1.0, "c": 1.0,
			"v": 1, "t": "2026-03-23T13:31:00Z", "n": 1, "vw": 1.0,
		},
	})
	s.processMessage(msg1)

	// This should drop
	msg2 := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T": "b", "S": "B", "o": 2.0, "h": 2.0, "l": 2.0, "c": 2.0,
			"v": 2, "t": "2026-03-23T13:31:00Z", "n": 1, "vw": 2.0,
		},
	})
	s.processMessage(msg2)

	_, _, _, _, _, _, dropped, _, _ := s.stats.snapshot()
	if dropped != 1 {
		t.Errorf("stats.droppedBars = %d, want 1", dropped)
	}
}

func TestProcessMessage_FirstBarLogged(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)

	msg := mustJSON(t, []interface{}{
		map[string]interface{}{
			"T": "b", "S": "AAPL", "o": 150.0, "h": 151.0, "l": 149.5, "c": 150.5,
			"v": 10000, "t": "2026-03-23T13:31:00Z", "n": 50, "vw": 150.25,
		},
	})

	// First call — isFirst should be true (tested via stats counter)
	s.processMessage(msg)

	bars1, _, _, _, _, _, _, _, _ := s.stats.snapshot()
	if bars1 != 1 {
		t.Errorf("after first bar: barsReceived = %d, want 1", bars1)
	}

	// Drain the channel
	<-s.bars

	// Second call — isFirst should be false
	s.processMessage(msg)

	bars2, _, _, _, _, _, _, _, _ := s.stats.snapshot()
	if bars2 != 2 {
		t.Errorf("after second bar: barsReceived = %d, want 2", bars2)
	}
}

func TestStreamStats_Snapshot(t *testing.T) {
	s := &streamStats{}
	now := time.Now()

	s.recordBar(now)
	s.recordBar(now)
	s.recordUpdatedBar()
	s.recordDailyBar()
	s.recordTrade()
	s.recordTrade()
	s.recordTrade()
	s.recordError("test error")
	s.recordSubscription()
	s.recordDrop()
	s.recordDrop()

	bars, updated, daily, trades, errors, subs, dropped, lastBar, lastErr := s.snapshot()

	if bars != 2 {
		t.Errorf("bars = %d, want 2", bars)
	}
	if updated != 1 {
		t.Errorf("updated = %d, want 1", updated)
	}
	if daily != 1 {
		t.Errorf("daily = %d, want 1", daily)
	}
	if trades != 3 {
		t.Errorf("trades = %d, want 3", trades)
	}
	if errors != 1 {
		t.Errorf("errors = %d, want 1", errors)
	}
	if subs != 1 {
		t.Errorf("subs = %d, want 1", subs)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	if lastBar != now {
		t.Errorf("lastBar = %v, want %v", lastBar, now)
	}
	if lastErr != "test error" {
		t.Errorf("lastErr = %q, want %q", lastErr, "test error")
	}
}

func TestNewStream_DefaultBufferSize(t *testing.T) {
	s := NewStream(StreamConfig{}, 0)
	if cap(s.bars) != 4096 {
		t.Errorf("bars channel capacity = %d, want 4096", cap(s.bars))
	}
	if cap(s.dailyBars) != 1024 {
		t.Errorf("dailyBars channel capacity = %d, want 1024", cap(s.dailyBars))
	}
}

func TestNewStream_CustomBufferSize(t *testing.T) {
	s := NewStream(StreamConfig{}, 2048)
	if cap(s.bars) != 2048 {
		t.Errorf("bars channel capacity = %d, want 2048", cap(s.bars))
	}
}

func TestDailyBarsChannel(t *testing.T) {
	s := NewStream(StreamConfig{}, 100)
	ch := s.DailyBars()
	if ch == nil {
		t.Fatal("DailyBars() returned nil")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is a ..."},
		{"", 10, ""},
	}

	for _, tt := range tests {
		got := truncate([]byte(tt.input), tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return data
}
