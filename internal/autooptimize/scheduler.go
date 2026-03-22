package autooptimize

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
)

// RunFunc is the function that executes an optimization run and returns the report.
// This is injected by the caller (main.go) to reuse the existing data-fetching
// and optimizer infrastructure.
type RunFunc func(ctx context.Context, asOf time.Time, outDir string) (optimizer.Report, error)

// Scheduler runs the optimizer on a weekly schedule and promotes validated
// candidate profiles.
type Scheduler struct {
	ProfilePath  string
	OptimizerDir string
	Schedule     string // "weekly" or "daily"
	Guardrails   Guardrails
	Notifier     Notifier
	RunOptimizer RunFunc
}

// Run starts the scheduling loop. It computes the next scheduled run time,
// sleeps until then, runs the optimizer, validates against guardrails, and
// promotes if all checks pass. Loops forever until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	for {
		nextRun := s.nextRunTime(time.Now())
		log.Printf("auto-optimize: next run scheduled for %s", nextRun.Format("2006-01-02 15:04:05 MST"))

		// Sleep until next run, respecting context cancellation
		sleepDur := time.Until(nextRun)
		if sleepDur > 0 {
			timer := time.NewTimer(sleepDur)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}

		if err := s.runOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("auto-optimize: run failed: %v", err)
			// Continue to next scheduled run
		}
	}
}

// RunOnce executes a single optimization cycle (for testing or one-shot use).
func (s *Scheduler) RunOnce(ctx context.Context) error {
	return s.runOnce(ctx)
}

func (s *Scheduler) runOnce(ctx context.Context) error {
	s.Notifier.NotifyStart()

	now := time.Now().In(markethours.Location())
	asOf := now

	log.Printf("auto-optimize: starting optimization as-of %s", asOf.Format("2006-01-02"))

	report, err := s.RunOptimizer(ctx, asOf, s.OptimizerDir)
	if err != nil {
		s.Notifier.NotifyCompleted(false, 0, 0, 0)
		return fmt.Errorf("optimizer run: %w", err)
	}

	if report.Recommendation == nil {
		log.Printf("auto-optimize: no recommendation produced")
		s.Notifier.NotifyCompleted(false, 0, 0, 0)
		return nil
	}

	rec := report.Recommendation
	sharpe := rec.ValidationResult.ProfitFactor
	winRate := rec.ValidationResult.WinRate
	trades := rec.ValidationResult.Trades

	log.Printf("auto-optimize: candidate sharpe=%.4f winrate=%.4f trades=%d dsr=%.4f",
		sharpe, winRate, trades, report.DSR)
	s.Notifier.NotifyCompleted(true, sharpe, winRate, trades)

	// Load current profile for improvement comparison
	var currentProfile *config.TradingProfile
	if profile, loadErr := config.LoadTradingProfile(s.ProfilePath); loadErr == nil {
		currentProfile = &profile
	}

	// Validate against guardrails
	result := s.Guardrails.Validate(report, currentProfile)
	if !result.Passed {
		log.Printf("auto-optimize: candidate rejected: %s", result.Reason)
		s.Notifier.NotifyRejected(result.Reason)
		s.writeStatusFile(false, result.Reason, sharpe, winRate, trades)
		return nil
	}

	// Promote
	promoter := &Promoter{ProfilePath: s.ProfilePath}
	if err := promoter.Promote(report); err != nil {
		return fmt.Errorf("promote: %w", err)
	}

	log.Printf("auto-optimize: profile promoted to %s", s.ProfilePath)
	s.Notifier.NotifyPromoted(rec.Config.StrategyProfileVersion, sharpe)
	s.writeStatusFile(true, "", sharpe, winRate, trades)
	return nil
}

// writeStatusFile writes a latest-status.json to the optimizer dir with run metadata.
func (s *Scheduler) writeStatusFile(promoted bool, reason string, sharpe, winRate float64, trades int) {
	if err := os.MkdirAll(s.OptimizerDir, 0755); err != nil {
		log.Printf("auto-optimize: failed to create optimizer dir: %v", err)
		return
	}

	statusData := map[string]any{
		"lastRun":  time.Now().In(markethours.Location()),
		"promoted": promoted,
		"reason":   reason,
		"sharpe":   sharpe,
		"winRate":  winRate,
		"trades":   trades,
	}
	data, err := json.MarshalIndent(statusData, "", "  ")
	if err != nil {
		log.Printf("auto-optimize: failed to marshal status: %v", err)
		return
	}

	statusFile := filepath.Join(s.OptimizerDir, "latest-status.json")
	tmpFile := statusFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		log.Printf("auto-optimize: failed to write status tmp: %v", err)
		return
	}
	if err := os.Rename(tmpFile, statusFile); err != nil {
		log.Printf("auto-optimize: failed to rename status file: %v", err)
	}
}

// nextRunTime computes the next Saturday 6 AM ET (for weekly) or next 6 AM ET (for daily).
func (s *Scheduler) nextRunTime(now time.Time) time.Time {
	loc := markethours.Location()
	local := now.In(loc)

	if s.Schedule == "daily" {
		// Next 6 AM ET
		next := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, loc)
		if !next.After(local) {
			next = next.AddDate(0, 0, 1)
		}
		return next
	}

	// Weekly: next Saturday 6 AM ET
	daysUntilSaturday := (time.Saturday - local.Weekday() + 7) % 7
	if daysUntilSaturday == 0 {
		// It's Saturday — check if we're past 6 AM
		saturdayRun := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, loc)
		if local.Before(saturdayRun) {
			return saturdayRun
		}
		// Already past Saturday 6 AM, schedule for next Saturday
		daysUntilSaturday = 7
	}

	next := time.Date(local.Year(), local.Month(), local.Day()+int(daysUntilSaturday), 6, 0, 0, 0, loc)
	return next
}
