package price

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// This test turns on the debug logger and replays a set of known timestamps,
// so we can see precisely where/why pricing succeeds or fails in logs.
//
// You can still steer it with env vars:
//
//	SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC  → RPC
//	PRICE_DEBUG_MINT  → base58 mint (default: BONK mainnet)
//	PRICE_DEBUG_UNIX  → single RFC3339 time; if set, the replay list is ignored
func Test_DebugPricingPath(t *testing.T) {
	loadDotEnvNearRepoRoot(t)
	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Skip("no RPC found in env (SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC)")
	}
	client := rpc.New(rpcURL)

	mintStr := os.Getenv("PRICE_DEBUG_MINT")
	if mintStr == "" {
		// BONK mainnet (as in your logs)
		mintStr = "DezXAZ8z7PnrnRJjz3wXBoRgixCa6xjnB7YaB1pPB263"
	}
	mint := solana.MustPublicKeyFromBase58(mintStr)

	// If PRICE_DEBUG_UNIX is provided, run a single-shot for that time (keeps old behavior).
	if v := os.Getenv("PRICE_DEBUG_UNIX"); v != "" {
		if tu, err := time.Parse(time.RFC3339, v); err == nil {
			runOneDebugCase(t, client, mint, tu.UTC().Unix())
			return
		}
	}

	// Replayed samples from your report (unix seconds):
	// ok=False cases (no USD-priceable swaps found):
	//   1762330380, 1757274180, 1757341560, 1757389560, 1757447580
	// ok=True cases (examples):
	//   1758695940, 1761826740
	samples := []int64{
		1762330380, // fail in your log
		1758695940, // ok in your log
		1761826740, // ok in your log
		1757274180, // fail
		1757341560, // fail
		1757389560, // fail
		1757447580, // fail
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Enable verbose debug logs from the pricing path.
	ctx = WithPriceDebug(ctx, t.Logf)

	// Walk the samples in order, printing a compact result line + verbose internals.
	okCount, failCount := 0, 0
	for i, ts := range samples {
		t.Logf("[replay %d/%d] t=%d (%s)", i+1, len(samples), ts, time.Unix(ts, 0).UTC().Format(time.RFC3339))
		v, kept, sumW, ok, err := GetTokenUSDPriceAtUnix(ctx, client, mint, ts, 0, 1.5, 1e-6)
		if err != nil {
			t.Logf("[result] t=%d  price=0  ok=%v  kept=%d  sumW=%.6f  err=%v", ts, ok, kept, sumW, err)
			failCount++
			continue
		}
		t.Logf("[result] t=%d  price=%.16f  ok=%v  kept=%d  sumW=%.6f", ts, v, ok, kept, sumW)
		if ok {
			okCount++
		} else {
			failCount++
		}
	}

	t.Logf("[summary] samples=%d ok=%d fail=%d", len(samples), okCount, failCount)
}

func runOneDebugCase(t *testing.T, client *rpc.Client, mint solana.PublicKey, targetUnix int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	ctx = WithPriceDebug(ctx, t.Logf)

	v, kept, sumW, ok, err := GetTokenUSDPriceAtUnix(ctx, client, mint, targetUnix, 0, 1.5, 1e-6)
	if err != nil {
		t.Logf("[debug] final ERROR: %v (ok=%v kept=%d sumW=%.6f)", err, ok, kept, sumW)
		return
	}
	t.Logf("[debug] final PRICE: %.10f USD (kept=%d sumW=%.6f ok=%v)", v, kept, sumW, ok)
}
