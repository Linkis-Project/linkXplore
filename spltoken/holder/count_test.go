// spltoken/holder/count_test.go
package holder

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// tryLoadDotEnvOnce tries to load .env if the needed env var is missing.
// It looks in a few likely relative paths so it works when tests run from the
// package dir or repo root.
var envAttempted bool

func tryLoadDotEnvOnce() {
	if envAttempted {
		return
	}
	envAttempted = true
	if os.Getenv(EnvRPCForCounter) != "" {
		return
	}
	candidates := []string{
		".env",
		"../.env",
		"../../.env",
		"../../../.env",
	}
	for _, path := range candidates {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			kv := strings.SplitN(line, "=", 2)
			if len(kv) != 2 {
				continue
			}
			key := strings.TrimSpace(kv[0])
			val := strings.TrimSpace(kv[1])
			// strip optional surrounding quotes
			val = strings.TrimPrefix(val, "\"")
			val = strings.TrimSuffix(val, "\"")
			val = strings.TrimPrefix(val, "'")
			val = strings.TrimSuffix(val, "'")
			// don't overwrite if already set
			if os.Getenv(key) == "" {
				_ = os.Setenv(key, val)
			}
		}
		_ = f.Close()
		// if we got what we need, stop
		if os.Getenv(EnvRPCForCounter) != "" {
			break
		}
	}
}

// requireCounterRPC skips integration tests unless the env is set
// (after attempting to load .env).
func requireCounterRPC(t *testing.T) string {
	t.Helper()
	tryLoadDotEnvOnce()
	url := os.Getenv(EnvRPCForCounter)
	if url == "" {
		t.Skipf("%s not set; skipping integration tests", EnvRPCForCounter)
	}
	return url
}

// We accept “close enough”. The test SKIPs on infra limits (rate-limit, timeout,
// missing secondary indexes, etc.)
func TestCountHoldersForMint_Auto(t *testing.T) {
	requireCounterRPC(t)

	// TRUMP mint (many holders)
	const mint = "4qWWLpN3k8CQFjhmxYWxurULDbApEVjFzbC9WVfLRhXm"

	ctx, cancel := context.WithTimeout(context.Background(), 2000*time.Second)
	defer cancel()

	res, err := CountHoldersForMint(ctx, mint)
	if err != nil {
		switch {
		case isRateLimited(err),
			isTooManyRequests(err),
			isServerBusy(err),
			isMethodNotFound(err),
			isTokenScanUnavailable(err):
			t.Skipf("skipping due to infra limitation: %v", err)
		default:
			if ctx.Err() == context.DeadlineExceeded {
				t.Skipf("skipping: context deadline exceeded")
			}
			t.Fatalf("CountHoldersForMint error: %v", err)
		}
		return
	}

	if res.TotalAccounts < res.Holders {
		t.Fatalf("invariant: totalAccounts(%d) < holders(%d)", res.TotalAccounts, res.Holders)
	}
	t.Logf("Holders≈%d, TotalAccounts=%d, ProgramUsed=%s", res.Holders, res.TotalAccounts, res.ProgramUsed.String())
}

func TestCountHoldersForMint_InvalidMintString(t *testing.T) {
	_, err := CountHoldersForMint(context.Background(), "this-is-not-a-mint")
	if err == nil {
		t.Fatalf("expected error on invalid mint string")
	}
}
