package config

import (
	"log"
	"os"
	"time"
)

// ProfileWatcher polls a trading profile file and fires a callback when the
// file is modified. Uses os.Stat polling rather than fsnotify to avoid an
// external dependency.
type ProfileWatcher struct {
	path        string
	interval    time.Duration
	lastModTime time.Time
	callback    func(TradingConfig)
}

// NewProfileWatcher creates a watcher that polls the given profile path at
// the specified interval and calls callback when the file's mod time changes.
func NewProfileWatcher(path string, interval time.Duration, callback func(TradingConfig)) *ProfileWatcher {
	return &ProfileWatcher{
		path:     path,
		interval: interval,
		callback: callback,
	}
}

// Start begins polling the profile file. It blocks until ctx is done.
func (w *ProfileWatcher) Start(done <-chan struct{}) {
	// Record initial mod time
	if info, err := os.Stat(w.path); err == nil {
		w.lastModTime = info.ModTime()
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			w.poll()
		}
	}
}

func (w *ProfileWatcher) poll() {
	info, err := os.Stat(w.path)
	if err != nil {
		return
	}

	modTime := info.ModTime()
	if modTime.Equal(w.lastModTime) {
		return
	}
	w.lastModTime = modTime

	profile, err := LoadTradingProfile(w.path)
	if err != nil {
		log.Printf("config: hot-reload failed to parse profile: %v", err)
		return
	}

	log.Printf("config: hot-reloaded trading profile version %s", profile.Version)
	w.callback(profile.Config)
}
