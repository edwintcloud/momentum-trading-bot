package runtime

import (
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
)

const maxLogs = 500

// State manages the runtime state of the trading system.
type State struct {
	mu             sync.RWMutex
	paused         bool
	emergencyStop  bool
	ready          bool
	candidates     []domain.Candidate
	logs           []domain.LogEntry
	marketRegime   domain.MarketRegimeSnapshot
	eventRecorder  domain.EventRecorder
	depStatuses    map[string]bool
	heartbeats     map[string]time.Time
}

// NewState creates a new runtime state.
// The recorder parameter is optional; if omitted, no events are persisted.
func NewState(recorders ...domain.EventRecorder) *State {
	var recorder domain.EventRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &State{
		candidates:    make([]domain.Candidate, 0),
		logs:          make([]domain.LogEntry, 0, maxLogs),
		depStatuses:   make(map[string]bool),
		heartbeats:    make(map[string]time.Time),
		eventRecorder: recorder,
	}
}

// TriggerDailyLossStop records that the daily loss limit was hit.
func (s *State) TriggerDailyLossStop(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
}

// OptimizerStatus tracks the optimizer lifecycle state.
type OptimizerStatus struct {
	ActiveProfileName           string
	ActiveProfileVersion        string
	PendingProfileName          string
	PendingProfileVersion       string
	LastOptimizerRun            time.Time
	LastPaperValidationResult   string
}

// Pause stops new entries from being taken.
func (s *State) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
}

// Resume re-enables trading if not emergency stopped.
func (s *State) Resume() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emergencyStop {
		return false
	}
	s.paused = false
	return true
}

// EmergencyStop permanently halts all trading.
func (s *State) EmergencyStop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emergencyStop = true
	s.paused = true
}

// IsPaused returns the current pause state.
func (s *State) IsPaused() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.paused
}

// IsEmergencyStopped returns whether an emergency stop is active.
func (s *State) IsEmergencyStopped() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.emergencyStop
}

// IsReady returns whether the system has completed initialization.
func (s *State) IsReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}

// SetReady marks the system as initialized.
func (s *State) SetReady(ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = ready
}

// SetDependencyStatus updates the health of a named dependency.
func (s *State) SetDependencyStatus(name string, healthy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.depStatuses[name] = healthy
}

// DependencyStatuses returns a copy of dependency health checks.
func (s *State) DependencyStatuses() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.depStatuses))
	for k, v := range s.depStatuses {
		out[k] = v
	}
	return out
}

// SetCandidates replaces the current scanner candidate list.
func (s *State) SetCandidates(candidates []domain.Candidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.candidates = candidates
}

// Candidates returns the current scanner candidates.
func (s *State) Candidates() []domain.Candidate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Candidate, len(s.candidates))
	copy(out, s.candidates)
	return out
}

// RecordLog adds a structured log entry.
func (s *State) RecordLog(level, component, message string) {
	entry := domain.LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Component: component,
		Message:   message,
	}
	s.mu.Lock()
	s.logs = append(s.logs, entry)
	if len(s.logs) > maxLogs {
		s.logs = s.logs[len(s.logs)-maxLogs:]
	}
	s.mu.Unlock()
	if s.eventRecorder != nil {
		s.eventRecorder.RecordLog(entry)
	}
}

// Logs returns a copy of the recent logs.
func (s *State) Logs() []domain.LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.LogEntry, len(s.logs))
	copy(out, s.logs)
	return out
}

// SetMarketRegime updates the market regime snapshot.
func (s *State) SetMarketRegime(regime domain.MarketRegimeSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marketRegime = regime
}

// MarketRegime returns the current market regime snapshot.
func (s *State) MarketRegime() domain.MarketRegimeSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.marketRegime
}

// Heartbeat records a heartbeat from a named component.
func (s *State) Heartbeat(component string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeats[component] = time.Now()
}

// StaleComponents returns components that haven't sent a heartbeat in the given duration.
func (s *State) StaleComponents(threshold time.Duration) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var stale []string
	now := time.Now()
	for name, lastBeat := range s.heartbeats {
		if now.Sub(lastBeat) > threshold {
			stale = append(stale, name)
		}
	}
	return stale
}
