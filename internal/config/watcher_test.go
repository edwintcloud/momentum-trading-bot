package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestProfileWatcher_DetectsChange(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.json")

	// Write initial profile
	writeTestProfile(t, profilePath, "v1")

	var callCount int32
	var lastVersion string

	watcher := NewProfileWatcher(profilePath, 50*time.Millisecond, func(cfg TradingConfig) {
		atomic.AddInt32(&callCount, 1)
		lastVersion = cfg.StrategyProfileVersion
	})

	done := make(chan struct{})
	go watcher.Start(done)

	// Let watcher initialize
	time.Sleep(100 * time.Millisecond)

	// Modify the file
	writeTestProfile(t, profilePath, "v2")

	// Wait for watcher to detect
	time.Sleep(200 * time.Millisecond)
	close(done)

	count := atomic.LoadInt32(&callCount)
	if count == 0 {
		t.Fatal("expected callback to be called after file change")
	}
	if lastVersion != "v2" {
		t.Fatalf("expected version v2, got %s", lastVersion)
	}
}

func TestProfileWatcher_NoChangeNoCB(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.json")
	writeTestProfile(t, profilePath, "v1")

	var callCount int32
	watcher := NewProfileWatcher(profilePath, 50*time.Millisecond, func(cfg TradingConfig) {
		atomic.AddInt32(&callCount, 1)
	})

	done := make(chan struct{})
	go watcher.Start(done)

	// Wait a bit without changing the file
	time.Sleep(200 * time.Millisecond)
	close(done)

	count := atomic.LoadInt32(&callCount)
	if count != 0 {
		t.Fatalf("expected no callback calls, got %d", count)
	}
}

func writeTestProfile(t *testing.T, path, version string) {
	t.Helper()
	profile := TradingProfile{
		Name:    StrategyProfileBaseline,
		Version: version,
		Config: TradingConfig{
			StrategyProfileName:    string(StrategyProfileBaseline),
			StrategyProfileVersion: version,
		},
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}
