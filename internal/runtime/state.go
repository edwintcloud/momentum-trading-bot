package runtime

import (
	"sync"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

const (
	maxCandidates = 24
	maxLogs       = 200
)

// State tracks runtime controls and operator-visible telemetry.
type State struct {
	mu            sync.RWMutex
	paused        bool
	emergencyStop bool
	dailyLossDay  string
	lastUpdate    time.Time
	candidates    []domain.Candidate
	logs          []domain.LogEntry
	recorder      domain.EventRecorder
	dependencies  map[string]DependencyStatus
	optimizer     OptimizerStatus
}

var tradingDayLocation = mustLoadLocation("America/New_York")

// DependencyStatus tracks readiness for critical runtime dependencies.
type DependencyStatus struct {
	Ready       bool      `json:"ready"`
	Message     string    `json:"message"`
	LastChecked time.Time `json:"lastChecked"`
}

// OptimizerStatus tracks which profile is active and what candidate profile is
// waiting on paper validation or operator review.
type OptimizerStatus struct {
	ActiveProfileName         string    `json:"activeProfileName"`
	ActiveProfileVersion      string    `json:"activeProfileVersion"`
	PendingProfileName        string    `json:"pendingProfileName"`
	PendingProfileVersion     string    `json:"pendingProfileVersion"`
	LastOptimizerRun          time.Time `json:"lastOptimizerRun"`
	LastPaperValidationResult string    `json:"lastPaperValidationResult"`
}

// NewState creates runtime state with the trading system marked as running.
func NewState() *State {
	return &State{lastUpdate: time.Now().UTC(), dependencies: make(map[string]DependencyStatus)}
}

// SetRecorder attaches an optional persistence sink.
func (s *State) SetRecorder(recorder domain.EventRecorder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorder = recorder
}

// Recorder returns the attached persistence sink.
func (s *State) Recorder() domain.EventRecorder {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.recorder
}

// Pause prevents new entries while leaving the market pipeline running.
func (s *State) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
	s.lastUpdate = time.Now().UTC()
}

// Resume clears the paused flag if the system is not emergency stopped.
func (s *State) Resume() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshDailyLossStopLocked(time.Now().UTC())
	if s.emergencyStop {
		return false
	}
	s.paused = false
	s.lastUpdate = time.Now().UTC()
	return s.dailyLossDay == ""
}

// EmergencyStop halts new entries until the process is restarted.
func (s *State) EmergencyStop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
	s.emergencyStop = true
	s.lastUpdate = time.Now().UTC()
}

// TriggerDailyLossStop halts new entries for the rest of the active trading day.
func (s *State) TriggerDailyLossStop(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dailyLossDay = tradingDay(at)
	s.lastUpdate = time.Now().UTC()
}

// CanOpenNewPositions reports whether the system may open fresh positions.
func (s *State) CanOpenNewPositions() bool {
	return s.EntryBlockReasonAt(time.Now().UTC()) == ""
}

// EntryBlockReasonAt returns the current gate reason for new entries at the provided time.
func (s *State) EntryBlockReasonAt(at time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshDailyLossStopLocked(at)
	if s.emergencyStop {
		return "emergency-stop"
	}
	if s.paused {
		return "trading-paused"
	}
	if s.dailyLossDay != "" {
		return "daily-loss-limit-day"
	}
	return ""
}

// IsDailyLossStopped reports whether the daily loss stop is currently active.
func (s *State) IsDailyLossStopped() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dailyLossDay != ""
}

// IsPaused reports the current pause state.
func (s *State) IsPaused() bool {
	return s.EntryBlockReasonAt(time.Now().UTC()) != ""
}

// IsEmergencyStopped reports whether an emergency stop is active.
func (s *State) IsEmergencyStopped() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.emergencyStop
}

// Touch updates the heartbeat timestamp.
func (s *State) Touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUpdate = time.Now().UTC()
}

// LastUpdate returns the latest heartbeat or control change timestamp.
func (s *State) LastUpdate() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastUpdate
}

// SetDependencyStatus updates readiness for a named dependency.
func (s *State) SetDependencyStatus(name string, ready bool, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dependencies[name] = DependencyStatus{
		Ready:       ready,
		Message:     message,
		LastChecked: time.Now().UTC(),
	}
	s.lastUpdate = time.Now().UTC()
}

// DependencyStatuses returns a copy of all dependency readiness states.
func (s *State) DependencyStatuses() map[string]DependencyStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]DependencyStatus, len(s.dependencies))
	for name, status := range s.dependencies {
		out[name] = status
	}
	return out
}

// SetOptimizerStatus replaces the operator-visible optimizer metadata.
func (s *State) SetOptimizerStatus(status OptimizerStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.optimizer = status
	s.lastUpdate = time.Now().UTC()
}

// OptimizerStatus returns the latest optimizer metadata snapshot.
func (s *State) OptimizerStatus() OptimizerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.optimizer
}

// IsReady reports whether all tracked dependencies are healthy.
func (s *State) IsReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.dependencies) == 0 {
		return false
	}
	for _, status := range s.dependencies {
		if !status.Ready {
			return false
		}
	}
	return true
}

// RecordCandidate stores a candidate for the dashboard.
func (s *State) RecordCandidate(candidate domain.Candidate) {
	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := s.candidates[:0]
	for _, existing := range s.candidates {
		if existing.Symbol != candidate.Symbol {
			filtered = append(filtered, existing)
		}
	}
	s.candidates = append([]domain.Candidate{candidate}, filtered...)
	if len(s.candidates) > maxCandidates {
		s.candidates = s.candidates[:maxCandidates]
	}
	s.lastUpdate = time.Now().UTC()
	if s.recorder != nil {
		s.recorder.RecordCandidate(candidate)
	}
}

// Candidates returns the most recent scanner candidates.
func (s *State) Candidates() []domain.Candidate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Candidate, len(s.candidates))
	copy(out, s.candidates)
	return out
}

// RecordLog appends a structured log entry for the dashboard.
func (s *State) RecordLog(level, component, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := domain.LogEntry{
		Timestamp: time.Now().UTC(),
		Level:     level,
		Component: component,
		Message:   message,
	}
	s.logs = append([]domain.LogEntry{entry}, s.logs...)
	if len(s.logs) > maxLogs {
		s.logs = s.logs[:maxLogs]
	}
	s.lastUpdate = entry.Timestamp
	if s.recorder != nil {
		s.recorder.RecordLog(entry)
	}
}

// Logs returns the latest structured logs.
func (s *State) Logs() []domain.LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.LogEntry, len(s.logs))
	copy(out, s.logs)
	return out
}

func (s *State) refreshDailyLossStopLocked(at time.Time) {
	if s.dailyLossDay == "" {
		return
	}
	if tradingDay(at) == s.dailyLossDay {
		return
	}
	s.dailyLossDay = ""
}

func tradingDay(at time.Time) string {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return at.In(tradingDayLocation).Format("2006-01-02")
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}
