package price

import (
	"context"
	"testing"
	"time"
)

// Live integration test against Binance public API.
// Uses a recent time to ensure a candle exists.
// Prints the price so you can see the value during test runs.
func Test_GetSOLPriceAtMillis_Live(t *testing.T) {
	ts := time.Now().UTC().Add(-10 * time.Minute) // recent minute in the past
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	price, err := GetSOLPriceAtTime(ctx, ts)
	if err != nil {
		t.Fatalf("GetSOLPriceAtTime err: %v", err)
	}
	if price <= 0 {
		t.Fatalf("unexpected price: %f", price)
	}
	t.Logf("SOL/USDT close @ %s ≈ %.8f", ts.UTC().Format(time.RFC3339), price)
}

// Quick unit tests for helpers (parsing + minuteFloor).
func Test_TimeHelpers(t *testing.T) {
	// minuteFloor
	ms := time.Date(2025, 1, 2, 12, 34, 56, 789*1e6, time.UTC).UnixMilli()
	got := minuteFloor(ms)
	want := time.Date(2025, 1, 2, 12, 34, 0, 0, time.UTC).UnixMilli()
	if got != want {
		t.Fatalf("minuteFloor wrong: want %d got %d", want, got)
	}

	// parseUserTimeToMs
	if _, err := parseUserTimeToMs("2024-11-07T12:00:00Z"); err != nil {
		t.Fatalf("parse RFC3339 failed: %v", err)
	}
	if _, err := parseUserTimeToMs("1731009600"); err != nil { // seconds
		t.Fatalf("parse seconds failed: %v", err)
	}
	if _, err := parseUserTimeToMs("1731009600000"); err != nil { // millis
		t.Fatalf("parse millis failed: %v", err)
	}
}
