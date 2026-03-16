package strategy

import (
	"testing"

	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

func TestDefaultEntryModelCapsExtremeFeatures(t *testing.T) {
	model := DefaultEntryModel()

	capped := domain.Candidate{
		GapPercent:           12,
		RelativeVolume:       20,
		PriceVsOpenPct:       35,
		DistanceFromHighPct:  0.6,
		OneMinuteReturnPct:   6,
		ThreeMinuteReturnPct: 10,
		VolumeRate:           4,
		MinutesSinceOpen:     45,
	}
	extreme := capped
	extreme.RelativeVolume = 4_000
	extreme.PriceVsOpenPct = 125
	extreme.OneMinuteReturnPct = 40
	extreme.ThreeMinuteReturnPct = 60
	extreme.VolumeRate = 12

	if got, want := model.Predict(extreme), model.Predict(capped); got != want {
		t.Fatalf("expected extreme features to be capped in prediction, got %.2f want %.2f", got, want)
	}
}
