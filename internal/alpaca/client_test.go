package alpaca

import "testing"

func TestAvailableQuantityFromError(t *testing.T) {
	err := &APIError{
		StatusCode: 403,
		Status:     "403 Forbidden",
		Code:       40310000,
		Message:    "insufficient qty available for order (requested: 473, available: 170)",
		Available:  "170",
		Symbol:     "EONR",
	}

	available, ok := AvailableQuantityFromError(err)
	if !ok {
		t.Fatal("expected to extract available quantity from Alpaca error")
	}
	if available != 170 {
		t.Fatalf("expected available quantity 170, got %d", available)
	}
	if !IsInsufficientQuantityError(err) {
		t.Fatal("expected insufficient quantity error to be detected")
	}
}

func TestParseShareQuantity(t *testing.T) {
	quantity, err := ParseShareQuantity("170.0000")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if quantity != 170 {
		t.Fatalf("expected quantity 170, got %d", quantity)
	}
}