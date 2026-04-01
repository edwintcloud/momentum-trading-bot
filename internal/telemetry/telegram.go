package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
)

// TelegramNotifier sends optional Telegram notifications for broker-confirmed trade events.
type TelegramNotifier struct {
	botToken string
	chatID   string
	client   *http.Client
}

// NewTelegramNotifierFromEnv creates a notifier from environment variables.
func NewTelegramNotifierFromEnv() *TelegramNotifier {
	return &TelegramNotifier{
		botToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		chatID:   os.Getenv("TELEGRAM_CHAT_ID"),
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (n *TelegramNotifier) enabled() bool {
	return n != nil && n.botToken != "" && n.chatID != ""
}

// NotifyTradeOpened sends a message when an opening fill creates or adds to a position.
func (n *TelegramNotifier) NotifyTradeOpened(report domain.ExecutionReport) {
	if !n.enabled() || !domain.IsOpeningIntent(report.Intent) {
		return
	}
	text := fmt.Sprintf(
		"🚀 Trade Opened\n\nSymbol: %s\nSide: %s\nQty: %d\nPrice: $%.2f\nStop: $%.2f",
		report.Symbol,
		domain.NormalizeDirection(report.Side),
		report.Quantity,
		report.Price,
		report.StopPrice,
	)
	n.send(text)
}

// NotifyTradeClosed sends a message when a position is fully closed.
func (n *TelegramNotifier) NotifyTradeClosed(report domain.ExecutionReport) {
	if !n.enabled() {
		return
	}
	text := fmt.Sprintf(
		"🏁 Trade Closed\n\nSymbol: %s\nSide: %s\nExit Price: $%.2f\nReason: %s",
		report.Symbol,
		domain.NormalizeDirection(report.Side),
		report.Price,
		report.Reason,
	)
	n.send(text)
}

// NotifyDailySummary sends an end-of-day summary for the current trading day.
func (n *TelegramNotifier) NotifyDailySummary(status domain.StatusSnapshot, asOf time.Time) {
	if !n.enabled() {
		return
	}

	roiPct := 0.0
	if status.StartingCapital > 0 {
		roiPct = status.DayPnL / status.StartingCapital * 100
	}

	text := fmt.Sprintf(
		"📊 End of Day Summary\n\nDate: %s\nNet Profit: $%.2f\nROI: %.2f%%\nTrades: %d",
		asOf.Format("2006-01-02"),
		status.DayPnL,
		roiPct,
		status.TradesToday,
	)
	n.send(text)
}

func (n *TelegramNotifier) send(text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.botToken)
	payload, _ := json.Marshal(map[string]string{
		"chat_id": n.chatID,
		"text":    text,
	})
	resp, err := n.client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("telemetry: telegram notification failed: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		log.Printf("telemetry: telegram returned status %d", resp.StatusCode)
	}
}
