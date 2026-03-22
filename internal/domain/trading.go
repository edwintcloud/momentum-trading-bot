package domain

import "strings"

const (
	DirectionLong  = "long"
	DirectionShort = "short"

	IntentOpen  = "open"
	IntentClose = "close"

	SideBuy  = "buy"
	SideSell = "sell"
)

func NormalizeDirection(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case DirectionShort:
		return DirectionShort
	default:
		return DirectionLong
	}
}

func NormalizeIntent(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case IntentClose, "partial":
		return IntentClose
	default:
		return IntentOpen
	}
}

func NormalizeSide(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SideSell:
		return SideSell
	default:
		return SideBuy
	}
}

func OpenBrokerSide(positionSide string) string {
	if NormalizeDirection(positionSide) == DirectionShort {
		return SideSell
	}
	return SideBuy
}

func CloseBrokerSide(positionSide string) string {
	if NormalizeDirection(positionSide) == DirectionShort {
		return SideBuy
	}
	return SideSell
}

func IsLong(positionSide string) bool {
	return NormalizeDirection(positionSide) == DirectionLong
}

func IsShort(positionSide string) bool {
	return NormalizeDirection(positionSide) == DirectionShort
}

func IsOpeningIntent(intent string) bool {
	return NormalizeIntent(intent) == IntentOpen
}

func IsClosingIntent(intent string) bool {
	return NormalizeIntent(intent) == IntentClose
}
