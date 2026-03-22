package autooptimize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// Notifier sends Telegram notifications for auto-optimizer events.
// If bot token or chat ID are empty, all methods are no-ops.
type Notifier struct {
	botToken string
	chatID   string
	client   *http.Client
}

// NewNotifier creates a Notifier from environment variables.
func NewNotifier() Notifier {
	return Notifier{
		botToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		chatID:   os.Getenv("TELEGRAM_CHAT_ID"),
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (n Notifier) enabled() bool {
	return n.botToken != "" && n.chatID != ""
}

// NotifyStart sends a notification that the optimizer has started.
func (n Notifier) NotifyStart() {
	n.send("Auto-Optimizer: starting weekly optimization run")
}

// NotifyCompleted sends a notification with key metrics from the optimizer run.
func (n Notifier) NotifyCompleted(winner bool, sharpe float64, winRate float64, trades int) {
	if !winner {
		n.send("Auto-Optimizer: completed with no viable candidate")
		return
	}
	msg := fmt.Sprintf(
		"Auto-Optimizer: completed\nSharpe: %.4f\nWin Rate: %.2f%%\nTrades: %d",
		sharpe, winRate*100, trades,
	)
	n.send(msg)
}

// NotifyPromoted sends a notification that a profile was auto-promoted.
func (n Notifier) NotifyPromoted(version string, sharpe float64) {
	msg := fmt.Sprintf(
		"Auto-Optimizer: profile promoted\nVersion: %s\nSharpe: %.4f",
		version, sharpe,
	)
	n.send(msg)
}

// NotifyRejected sends a notification that the candidate was rejected.
func (n Notifier) NotifyRejected(reason string) {
	msg := fmt.Sprintf("Auto-Optimizer: candidate rejected\nReason: %s", reason)
	n.send(msg)
}

func (n Notifier) send(text string) {
	if !n.enabled() {
		return
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.botToken)
	payload, _ := json.Marshal(map[string]string{
		"chat_id": n.chatID,
		"text":    text,
	})
	resp, err := n.client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("auto-optimize: telegram notification failed: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		log.Printf("auto-optimize: telegram returned status %d", resp.StatusCode)
	}
}
