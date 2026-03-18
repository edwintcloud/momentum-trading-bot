package backtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
)

func BenchmarkRunSyntheticMonth(b *testing.B) {
	b.ReportAllocs()

	bars := generateSyntheticBars(100, 20, 390)
	runCfg := RunConfig{Bars: bars}
	cfg := config.DefaultTradingConfig()

	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := Run(context.Background(), cfg, runCfg); err != nil {
			b.Fatalf("expected benchmark replay to succeed, got %v", err)
		}
	}
}

func generateSyntheticBars(symbolCount, tradingDays, minutesPerDay int) []InputBar {
	start := time.Date(2026, 3, 2, 13, 30, 0, 0, time.UTC)
	bars := make([]InputBar, 0, symbolCount*tradingDays*minutesPerDay)
	for day := 0; day < tradingDays; day++ {
		dayStart := start.AddDate(0, 0, day)
		for minute := 0; minute < minutesPerDay; minute++ {
			at := dayStart.Add(time.Duration(minute) * time.Minute)
			for symbolIndex := 0; symbolIndex < symbolCount; symbolIndex++ {
				base := 2.0 + float64(symbolIndex%25)*0.35 + float64(day)*0.05
				open := base + float64(minute%7)*0.02
				closePrice := open + 0.03
				bars = append(bars, InputBar{
					Timestamp: at,
					Symbol:    fmt.Sprintf("SYM%03d", symbolIndex),
					Open:      open,
					High:      closePrice + 0.04,
					Low:       open - 0.03,
					Close:     closePrice,
					Volume:    int64(100_000 + symbolIndex*100 + minute*25),
					PrevClose: base - 0.05,
				})
			}
		}
	}
	return bars
}
