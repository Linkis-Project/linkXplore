package price

import (
	"context"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

func TestPricesAtSlot_371161375_DBONK(t *testing.T) {
	// Reuse helpers defined in filter_test.go
	loadDotEnvNearRepoRoot(t)

	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Fatalf("no RPC found in env (expected SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC)")
	}
	client := rpc.New(rpcURL)

	slot := uint64(371161375)
	mint := solana.MustPublicKeyFromBase58("83kGGSggYGP2ZEEyvX54SkZR1kFn84RgGCDyptbDbonk")

	ctx := context.Background()
	points, err := GetPricesAtSlot(ctx, client, slot, mint)
	if err != nil {
		t.Fatalf("GetPricesAtSlot error: %v", err)
	}

	if len(points) == 0 {
		t.Logf("slot=%d mint=%s -> 0 price points (no SOL/USDC/USDT pair or swapInfo was nil)", slot, mint)
		return
	}

	t.Logf("slot=%d mint=%s -> %d price point(s)", slot, mint, len(points))
	values := make([]float64, 0, len(points))
	weights := make([]float64, 0, len(points))
	for i, pp := range points {
		t.Logf("[%d] %s", i, PrettyPrice(pp))
		if pp.PriceUSD > 0 && pp.TargetQtyFloat > 0 {
			var w float64
			if pp.BaseIsStable {
				w = float64(pp.BaseAmountRaw) / float64Pow10(pp.BaseDecimals)
			} else if pp.BaseIsSOL {
				w = pp.PriceUSD * pp.TargetQtyFloat
			}
			if w > 0 {
				values = append(values, pp.PriceUSD)
				weights = append(weights, w)
			}
		}
	}

	if len(values) > 0 {
		v, kept, sumW, ok := VWAPWithLogFence(values, weights, 1.5, 1e-6)
		t.Logf("VWAP USD: v=%.10f kept=%d sumW=%.6f ok=%v", v, kept, sumW, ok)
	}
}

func Test_GetTokenUSDPriceAtTime_Live(t *testing.T) {
	loadDotEnvNearRepoRoot(t)
	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Skip("no RPC found in env (SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC)")
	}
	client := rpc.New(rpcURL)

	// Intentionally pick ~30 days ago to exercise the backoff cap (~8 days).
	ts := time.Now().UTC().Add(-200 * time.Minute) // ~30d

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	// Enable debug logs to see slot search + backoff scanning progress.
	ctx = WithPriceDebug(ctx, t.Logf)

	// Use DBONK (example) – replace with any liquid token for real checks.
	mint := solana.MustPublicKeyFromBase58("Dz9mQ9NzkBcCsuGPFJ3r1bS4wgqKMHBPiVuniW8Mbonk")

	// Log planned backoff window (~8d) in slots so we can see the cap in output.
	backoffCap := estimateBackoffSlotsForDays(ctx, client, 8.0)
	t.Logf("[test] backoff cap (≈8 days) ~%d slots", backoffCap)

	v, kept, sumW, ok, err := GetTokenUSDPriceAtTime(ctx, client, mint, ts)
	if err != nil {
		t.Logf("GetTokenUSDPriceAtTime: %v (ok=%v kept=%d sumW=%.6f)", err, ok, kept, sumW)
		return
	}
	t.Logf("Price at %s: %.10f USD (kept=%d sumW=%.6f ok=%v)", ts.Format(time.RFC3339), v, kept, sumW, ok)
}

func float64Pow10(n int) float64 {
	if n <= 0 {
		return 1
	}
	p := 1.0
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}
