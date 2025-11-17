package price

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

/*
Fetch SOL/USDT “close” price for the 1-minute candle that contains timestamp T.

Binance REST:
  GET /api/v3/klines?symbol=SOLUSDT&interval=1m&startTime=...&endTime=...&limit=1
Times are milliseconds since epoch (UTC).

Env override:
  BINANCE_BASE (default: https://api.binance.com)
*/

const (
	binanceDefaultBase = "https://api.binance.com"
	binanceSymbol      = "SOLUSDT"
	binanceInterval    = "1m"
)

// minuteFloor rounds ms down to the start of its 1-minute window.
func minuteFloor(ms int64) int64 {
	const oneMinMs = int64(60 * 1000)
	return (ms / oneMinMs) * oneMinMs
}

// parseUserTimeToMs converts commonly used time inputs to ms since epoch.
// Accepts UNIX seconds, UNIX ms, RFC3339/RFC3339Nano, and a couple simple layouts.
func parseUserTimeToMs(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty time")
	}

	// Try integer seconds/millis.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		// Heuristic: >= 1e12 => already milliseconds
		if n >= 1_000_000_000_000 {
			return n, nil
		}
		return n * 1000, nil
	}

	// RFC3339 first.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UnixMilli(), nil
	}

	// A few extra formats.
	layouts := []string{
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UnixMilli(), nil
		}
	}

	return 0, fmt.Errorf("cannot parse time: %q", s)
}

// small HTTP helper with sane timeouts and tiny retry.
type httpClient struct{ c *http.Client }

func newHTTP() *httpClient {
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		IdleConnTimeout:     60 * time.Second,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
	}
	return &httpClient{
		c: &http.Client{
			Timeout:   10 * time.Second,
			Transport: tr,
		},
	}
}

func (h *httpClient) getJSON(ctx context.Context, rawURL string, dst interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	var lastErr error
	for i := 0; i < 3; i++ {
		resp, err := h.c.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(300*(i+1)) * time.Millisecond)
			continue
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				lastErr = json.NewDecoder(resp.Body).Decode(dst)
				return
			}
			var errObj map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&errObj)
			lastErr = fmt.Errorf("http %d: %v", resp.StatusCode, errObj)
		}()
		if lastErr == nil {
			return nil
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			time.Sleep(time.Duration(500*(i+1)) * time.Millisecond)
		}
	}
	return lastErr
}

// GetSOLPriceAtMillis returns the SOL/USDT close price for the minute that contains ms.
func GetSOLPriceAtMillis(ctx context.Context, ms int64) (float64, error) {
	base := os.Getenv("BINANCE_BASE")
	if base == "" {
		base = binanceDefaultBase
	}

	start := minuteFloor(ms)
	end := start + 60_000 - 1

	u, _ := url.Parse(base)
	u.Path = "/api/v3/klines"
	q := u.Query()
	q.Set("symbol", binanceSymbol)
	q.Set("interval", binanceInterval)
	q.Set("startTime", strconv.FormatInt(start, 10))
	q.Set("endTime", strconv.FormatInt(end, 10))
	q.Set("limit", "1")
	u.RawQuery = q.Encode()

	var data [][]any // Binance returns array-of-arrays
	if err := newHTTP().getJSON(ctx, u.String(), &data); err != nil {
		return 0, err
	}
	if len(data) == 0 || len(data[0]) < 5 {
		return 0, fmt.Errorf("no kline for window [%d,%d]", start, end)
	}

	// index 4 is "close"
	switch v := data[0][4].(type) {
	case string:
		return strconv.ParseFloat(v, 64)
	case float64:
		return v, nil
	default:
		return 0, errors.New("unexpected close type from Binance")
	}
}

// GetSOLPriceAtTime convenience wrapper for a time.Time.
func GetSOLPriceAtTime(ctx context.Context, t time.Time) (float64, error) {
	return GetSOLPriceAtMillis(ctx, t.UTC().UnixMilli())
}

// GetSOLPriceAtInput parses a time string (unix sec/ms or RFC3339) then fetches price.
func GetSOLPriceAtInput(ctx context.Context, input string) (float64, error) {
	ms, err := parseUserTimeToMs(input)
	if err != nil {
		return 0, err
	}
	return GetSOLPriceAtMillis(ctx, ms)
}
