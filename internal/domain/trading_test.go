package domain

import "testing"

func TestNormalizeIntent_PartialPreserved(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"partial", IntentPartial},
		{"PARTIAL", IntentPartial},
		{" partial ", IntentPartial},
		{"partial-exit", IntentPartial},
		{"reduce", IntentPartial},
		{"close", IntentClose},
		{"CLOSE", IntentClose},
		{"open", IntentOpen},
		{"buy", IntentOpen},
		{"sell", IntentOpen},
		{"entry", IntentOpen},
	}

	for _, tt := range tests {
		got := NormalizeIntent(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeIntent(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsClosingIntent_IncludesPartial(t *testing.T) {
	if !IsClosingIntent("close") {
		t.Error("IsClosingIntent(\"close\") should be true")
	}
	if !IsClosingIntent("partial") {
		t.Error("IsClosingIntent(\"partial\") should be true")
	}
	if !IsClosingIntent("partial-exit") {
		t.Error("IsClosingIntent(\"partial-exit\") should be true")
	}
	if !IsClosingIntent("reduce") {
		t.Error("IsClosingIntent(\"reduce\") should be true")
	}
	if IsClosingIntent("open") {
		t.Error("IsClosingIntent(\"open\") should be false")
	}
}

func TestIsOpeningIntent_ExcludesPartial(t *testing.T) {
	if !IsOpeningIntent("open") {
		t.Error("IsOpeningIntent(\"open\") should be true")
	}
	if !IsOpeningIntent("buy") {
		t.Error("IsOpeningIntent(\"buy\") should be true")
	}
	if IsOpeningIntent("partial") {
		t.Error("IsOpeningIntent(\"partial\") should be false")
	}
	if IsOpeningIntent("close") {
		t.Error("IsOpeningIntent(\"close\") should be false")
	}
}

func TestIsPartialIntent(t *testing.T) {
	if !IsPartialIntent("partial") {
		t.Error("IsPartialIntent(\"partial\") should be true")
	}
	if !IsPartialIntent("partial-exit") {
		t.Error("IsPartialIntent(\"partial-exit\") should be true")
	}
	if !IsPartialIntent("reduce") {
		t.Error("IsPartialIntent(\"reduce\") should be true")
	}
	if IsPartialIntent("close") {
		t.Error("IsPartialIntent(\"close\") should be false")
	}
	if IsPartialIntent("open") {
		t.Error("IsPartialIntent(\"open\") should be false")
	}
}
