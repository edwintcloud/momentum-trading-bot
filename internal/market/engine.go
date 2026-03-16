package market

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
	"github.com/edwincloud/momentum-trading-bot/internal/volumeprofile"
)

var nyLocation = mustLoadLocation("America/New_York")

type symbolState struct {
	previousClose float64
	open          float64
	highOfDay     float64
	totalVolume   int64
	preMarketVol  int64
	prevDayVolume int64
	recentVolumes []int64
	barVolumes    map[int64]int64
	catalyst      string
	catalystURL   string
	hydrated      bool
	hydrating     bool
	nextHydration time.Time
}

type hydrationRequest struct {
	symbol    string
	timestamp time.Time
}

const hydrationBatchSize = 100

// Engine streams live Alpaca market data and normalizes it into trading ticks.
type Engine struct {
	mu                sync.Mutex
	client            *alpaca.Client
	config            config.TradingConfig
	portfolio         *portfolio.Manager
	runtime           *runtime.State
	state             map[string]*symbolState
	seenBars          map[string]bool
	seenTypes         map[string]bool
	hydrationQueue    chan hydrationRequest
	hydrationOverflow map[string]hydrationRequest
	hydrationSignal   chan struct{}
	backpressureAt    time.Time
}

// NewEngine creates a live market-data engine.
func NewEngine(client *alpaca.Client, cfg config.TradingConfig, portfolioManager *portfolio.Manager, runtimeState *runtime.State) *Engine {
	return &Engine{
		client:            client,
		config:            cfg,
		portfolio:         portfolioManager,
		runtime:           runtimeState,
		state:             make(map[string]*symbolState),
		seenBars:          make(map[string]bool),
		seenTypes:         make(map[string]bool),
		hydrationQueue:    make(chan hydrationRequest, maxInt(cfg.HydrationQueueSize, 32)),
		hydrationOverflow: make(map[string]hydrationRequest),
		hydrationSignal:   make(chan struct{}, 1),
	}
}

// Start consumes Alpaca market data until the context is canceled.
func (e *Engine) Start(ctx context.Context, out chan<- domain.Tick) error {
	e.runtime.SetDependencyStatus("market_data_stream", false, "waiting for alpaca stream")
	e.runtime.RecordLog("info", "market", "connecting to alpaca market data stream")
	e.runtime.RecordLog("info", "market", fmt.Sprintf("hydration rate limit set to %d requests/min", e.config.HydrationRequestsPerMin))
	go e.runHydrationWorker(ctx)
	return e.client.StreamMarketData(ctx, func(message alpaca.StreamMessage) error {
		e.runtime.Touch()
		if message.Type != "stream-error" {
			e.runtime.SetDependencyStatus("market_data_stream", true, "alpaca stream connected")
		}
		e.logFirstEventType(message)
		if message.Type == "success" || message.Type == "subscription" {
			text := strings.TrimSpace(message.Message)
			if text == "" {
				text = message.Type
			}
			e.runtime.RecordLog("info", "market", "stream "+message.Type+": "+text)
			return nil
		}
		if message.Type == "stream-error" {
			e.runtime.SetDependencyStatus("market_data_stream", false, "alpaca stream reconnecting")
			e.runtime.RecordLog("warn", "market", "stream error: "+strings.TrimSpace(message.Message))
			return nil
		}
		if message.Type == "s" {
			e.runtime.RecordLog("warn", "market", message.Symbol+" status "+message.StatusCode+" "+strings.TrimSpace(message.ReasonCode+" "+message.ReasonMessage))
			return nil
		}
		if message.Type != "b" && message.Type != "u" && message.Type != "d" {
			e.runtime.RecordLog("warn", "market", fmt.Sprintf("unhandled stream event type=%s payload=%s", message.Type, message.RawPayload))
			return nil
		}
		tick := e.handleBar(ctx, message)
		if tick.Symbol == "" {
			return nil
		}
		e.portfolio.MarkPrice(tick.Symbol, tick.Price)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- tick:
			return nil
		}
	})
}

func (e *Engine) handleBar(ctx context.Context, message alpaca.StreamMessage) domain.Tick {
	e.getState(message.Symbol)
	e.scheduleHydration(message.Symbol, message.Close, message.Timestamp)

	e.logFirstBar(message)

	e.mu.Lock()
	defer e.mu.Unlock()
	state := e.state[message.Symbol]
	if state == nil {
		return domain.Tick{}
	}

	minuteKey := message.Timestamp.UTC().Unix() / 60
	previousMinuteVolume := state.barVolumes[minuteKey]
	deltaVolume := message.Volume - previousMinuteVolume
	if deltaVolume < 0 {
		deltaVolume = message.Volume
	}
	state.barVolumes[minuteKey] = message.Volume
	state.totalVolume += deltaVolume
	if len(state.barVolumes) > 60 {
		cutoff := minuteKey - 60
		for k := range state.barVolumes {
			if k < cutoff {
				delete(state.barVolumes, k)
			}
		}
	}
	state.recentVolumes = append(state.recentVolumes, deltaVolume)
	if len(state.recentVolumes) > 5 {
		state.recentVolumes = state.recentVolumes[len(state.recentVolumes)-5:]
	}

	if state.open == 0 {
		state.open = message.Open
	}
	if message.High > state.highOfDay {
		state.highOfDay = message.High
	}
	if state.highOfDay == 0 {
		state.highOfDay = message.High
	}
	if isPremarket(message.Timestamp) {
		state.preMarketVol += deltaVolume
	}

	gapPercent := 0.0
	if state.previousClose > 0 {
		gapPercent = ((message.Close - state.previousClose) / state.previousClose) * 100
	}
	relativeVolume := calculateRelativeVolume(state, message.Timestamp)
	volumeSpike := isVolumeSpike(state, deltaVolume, relativeVolume)

	return domain.Tick{
		Symbol:          message.Symbol,
		Price:           round2(message.Close),
		BarOpen:         round2(message.Open),
		BarHigh:         round2(message.High),
		BarLow:          round2(message.Low),
		Open:            round2(state.open),
		HighOfDay:       round2(state.highOfDay),
		Volume:          state.totalVolume,
		RelativeVolume:  round2(relativeVolume),
		GapPercent:      round2(gapPercent),
		PreMarketVolume: state.preMarketVol,
		VolumeSpike:     volumeSpike,
		Catalyst:        state.catalyst,
		CatalystURL:     state.catalystURL,
		Timestamp:       message.Timestamp.UTC(),
	}
}

func (e *Engine) scheduleHydration(symbol string, price float64, timestamp time.Time) {
	if price < e.config.MinPrice {
		return
	}
	now := time.Now().UTC()
	e.mu.Lock()
	state := e.state[symbol]
	if state == nil || state.hydrated || state.hydrating || (!state.nextHydration.IsZero() && now.Before(state.nextHydration)) {
		e.mu.Unlock()
		return
	}
	state.hydrating = true
	e.mu.Unlock()

	request := hydrationRequest{symbol: symbol, timestamp: timestamp}
	e.enqueueHydrationRequest(request, now)
}

func (e *Engine) runHydrationWorker(ctx context.Context) {
	for {
		request, ok := e.nextHydrationRequest(ctx)
		if !ok {
			return
		}
		batch := e.collectHydrationBatch(request)
		e.hydrateBatch(ctx, batch)
	}
}

func (e *Engine) enqueueHydrationRequest(request hydrationRequest, now time.Time) {
	select {
	case e.hydrationQueue <- request:
		return
	default:
	}

	e.mu.Lock()
	existing, exists := e.hydrationOverflow[request.symbol]
	if !exists || request.timestamp.After(existing.timestamp) {
		e.hydrationOverflow[request.symbol] = request
	}
	overflowSize := len(e.hydrationOverflow)
	shouldLog := e.backpressureAt.IsZero() || now.Sub(e.backpressureAt) >= 15*time.Second
	if shouldLog {
		e.backpressureAt = now
	}
	e.mu.Unlock()

	select {
	case e.hydrationSignal <- struct{}{}:
	default:
	}

	if shouldLog {
		e.runtime.RecordLog("warn", "market", fmt.Sprintf("hydration backlog saturated, queue=%d overflow=%d", cap(e.hydrationQueue), overflowSize))
	}
}

func (e *Engine) nextHydrationRequest(ctx context.Context) (hydrationRequest, bool) {
	for {
		select {
		case <-ctx.Done():
			return hydrationRequest{}, false
		case request := <-e.hydrationQueue:
			return request, true
		case <-e.hydrationSignal:
			request, ok := e.takeOverflowRequest()
			if ok {
				return request, true
			}
		}
	}
}

func (e *Engine) collectHydrationBatch(first hydrationRequest) []hydrationRequest {
	batch := []hydrationRequest{first}
	seen := map[string]int{first.symbol: 0}
	for len(batch) < hydrationBatchSize {
		select {
		case request := <-e.hydrationQueue:
			e.mergeHydrationRequest(&batch, seen, request)
		default:
			for _, request := range e.takeOverflowRequests(hydrationBatchSize - len(batch)) {
				e.mergeHydrationRequest(&batch, seen, request)
			}
			return batch
		}
	}
	return batch
}

func (e *Engine) mergeHydrationRequest(batch *[]hydrationRequest, seen map[string]int, request hydrationRequest) {
	if index, exists := seen[request.symbol]; exists {
		if request.timestamp.After((*batch)[index].timestamp) {
			(*batch)[index].timestamp = request.timestamp
		}
		return
	}
	seen[request.symbol] = len(*batch)
	*batch = append(*batch, request)
}

func (e *Engine) takeOverflowRequest() (hydrationRequest, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for symbol, request := range e.hydrationOverflow {
		delete(e.hydrationOverflow, symbol)
		if len(e.hydrationOverflow) == 0 {
			e.backpressureAt = time.Time{}
		}
		return request, true
	}
	return hydrationRequest{}, false
}

func (e *Engine) takeOverflowRequests(limit int) []hydrationRequest {
	if limit <= 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	requests := make([]hydrationRequest, 0, limit)
	for symbol, request := range e.hydrationOverflow {
		requests = append(requests, request)
		delete(e.hydrationOverflow, symbol)
		if len(requests) == limit {
			break
		}
	}
	if len(e.hydrationOverflow) == 0 {
		e.backpressureAt = time.Time{}
	}
	return requests
}

func (e *Engine) hydrateBatch(ctx context.Context, batch []hydrationRequest) {
	if len(batch) == 0 {
		return
	}
	hydrateCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	symbols := make([]string, 0, len(batch))
	requestBySymbol := make(map[string]hydrationRequest, len(batch))
	for _, request := range batch {
		symbols = append(symbols, request.symbol)
		requestBySymbol[request.symbol] = request
	}

	if err := e.waitForHydrationSlot(hydrateCtx); err != nil {
		e.deferHydrationBatch(symbols, time.Duration(e.config.HydrationRetrySec)*time.Second)
		return
	}
	snapshots, err := e.client.GetSnapshots(hydrateCtx, symbols)
	if err != nil {
		e.deferHydrationBatch(symbols, e.hydrationRetryDelay(err))
		return
	}

	volumes := map[string]int64{}
	byDay := make(map[string][]string)
	dayTimestamp := make(map[string]time.Time)
	for _, request := range batch {
		dayKey := request.timestamp.In(nyLocation).Format("2006-01-02")
		byDay[dayKey] = append(byDay[dayKey], request.symbol)
		dayTimestamp[dayKey] = request.timestamp
	}
	for dayKey, daySymbols := range byDay {
		if err := e.waitForHydrationSlot(hydrateCtx); err != nil {
			break
		}
		dayVolumes, err := e.client.GetPremarketVolumes(hydrateCtx, daySymbols, dayTimestamp[dayKey])
		if err != nil {
			continue
		}
		for symbol, volume := range dayVolumes {
			volumes[symbol] = volume
		}
	}

	now := time.Now().UTC()
	for _, symbol := range symbols {
		request := requestBySymbol[symbol]
		snapshot, ok := snapshots[symbol]
		if !ok {
			e.finishHydration(symbol, func(state *symbolState) {
				state.hydrating = false
				state.nextHydration = now.Add(60 * time.Second)
			})
			continue
		}
		premarketVolume := volumes[symbol]
		e.finishHydration(symbol, func(state *symbolState) {
			if snapshot.PrevDailyBar != nil {
				state.previousClose = snapshot.PrevDailyBar.Close
				state.prevDayVolume = snapshot.PrevDailyBar.Volume
			}
			if snapshot.DailyBar != nil {
				state.open = snapshot.DailyBar.Open
				state.highOfDay = math.Max(state.highOfDay, snapshot.DailyBar.High)
				if snapshot.DailyBar.Volume > state.totalVolume {
					state.totalVolume = snapshot.DailyBar.Volume
				}
			}
			if premarketVolume > state.preMarketVol {
				state.preMarketVol = premarketVolume
			}
			state.hydrated = true
			state.hydrating = false
			state.nextHydration = time.Time{}
		})
		_ = request
	}

	// Fetch news catalysts for batch
	if err := e.waitForHydrationSlot(hydrateCtx); err == nil {
		catalysts, newsErr := e.client.GetNews(hydrateCtx, symbols, len(symbols)*2)
		if newsErr == nil {
			for symbol, catalyst := range catalysts {
				e.finishHydration(symbol, func(state *symbolState) {
					if state.catalyst == "" {
						state.catalyst = catalyst.Headline
						state.catalystURL = catalyst.URL
					}
				})
			}
		}
	}
}

func (e *Engine) deferHydrationBatch(symbols []string, delay time.Duration) {
	next := time.Now().UTC().Add(delay)
	for _, symbol := range symbols {
		e.finishHydration(symbol, func(state *symbolState) {
			state.hydrating = false
			state.nextHydration = next
		})
	}
}

func (e *Engine) waitForHydrationSlot(ctx context.Context) error {
	interval := time.Minute / time.Duration(maxInt(e.config.HydrationRequestsPerMin, 1))
	if interval < 250*time.Millisecond {
		interval = 250 * time.Millisecond
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(interval):
		return nil
	}
}

func (e *Engine) hydrationRetryDelay(err error) time.Duration {
	if strings.Contains(strings.ToLower(err.Error()), "429") || strings.Contains(strings.ToLower(err.Error()), "too many requests") {
		return time.Duration(e.config.HydrationRetrySec) * time.Second
	}
	return 60 * time.Second
}

func (e *Engine) getState(symbol string) *symbolState {
	e.mu.Lock()
	defer e.mu.Unlock()
	state, exists := e.state[symbol]
	if !exists {
		state = &symbolState{
			barVolumes: make(map[int64]int64),
		}
		e.state[symbol] = state
	}
	return state
}

func (e *Engine) finishHydration(symbol string, mutate func(*symbolState)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	state := e.state[symbol]
	if state == nil {
		return
	}
	mutate(state)
}

func (e *Engine) logFirstBar(message alpaca.StreamMessage) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.seenBars[message.Symbol] {
		return
	}
	if len(e.seenBars) >= 12 {
		return
	}
	e.seenBars[message.Symbol] = true
	e.runtime.RecordLog("info", "market", fmt.Sprintf("first %s event for %s close=%.2f volume=%d", message.Type, message.Symbol, message.Close, message.Volume))
}

func (e *Engine) logFirstEventType(message alpaca.StreamMessage) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.seenTypes[message.Type] {
		return
	}
	if len(e.seenTypes) >= 16 {
		return
	}
	e.seenTypes[message.Type] = true
	parts := []string{fmt.Sprintf("first stream event type=%s", message.Type)}
	if message.Symbol != "" {
		parts = append(parts, "symbol="+message.Symbol)
	}
	if text := strings.TrimSpace(message.Message); text != "" {
		parts = append(parts, "message="+text)
	}
	if raw := strings.TrimSpace(message.RawPayload); raw != "" {
		parts = append(parts, "payload="+raw)
	}
	e.runtime.RecordLog("info", "market", strings.Join(parts, " "))
}

func calculateRelativeVolume(state *symbolState, timestamp time.Time) float64 {
	if state.prevDayVolume <= 0 {
		return 1.0
	}
	expected := float64(state.prevDayVolume) * volumeprofile.ExpectedCumulativeShare(timestamp)
	if expected < 1 {
		return 1.0
	}
	return float64(state.totalVolume) / expected
}

func isVolumeSpike(state *symbolState, deltaVolume int64, relativeVolume float64) bool {
	if relativeVolume >= 5 {
		return true
	}
	if len(state.recentVolumes) < 3 {
		return false
	}
	var total int64
	for _, volume := range state.recentVolumes[:len(state.recentVolumes)-1] {
		total += volume
	}
	average := float64(total) / float64(len(state.recentVolumes)-1)
	return average > 0 && float64(deltaVolume) >= average*1.8
}

func isPremarket(timestamp time.Time) bool {
	est := timestamp.In(nyLocation)
	minutes := est.Hour()*60 + est.Minute()
	return minutes >= 4*60 && minutes < 9*60+30
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
