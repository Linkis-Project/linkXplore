package price

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// Outliers requested for focused debugging (taken from your summary).
// Each entry is the UNIX time `t` for the indicated idx.
func Test_DebugPricingPath_Outliers(t *testing.T) {
	loadDotEnvNearRepoRoot(t)
	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Skip("no RPC found in env (SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC)")
	}
	client := rpc.New(rpcURL)

	mintStr := os.Getenv("PRICE_DEBUG_MINT")
	if mintStr == "" {
		mintStr = "DezXAZ8z7PnrnRJjz3wXBoRgixCa6xjnB7YaB1pPB263" // BONK mainnet
	}
	mint := solana.MustPublicKeyFromBase58(mintStr)

	ctx, cancel := context.WithTimeout(context.Background(), 30000*time.Second)
	defer cancel()
	ctx = WithPriceDebug(ctx, t.Logf)

	// NOTE: Header said "Outliers: 14", but 12 idx were provided.
	// If you want two more, ping me with their idx and I’ll add them.
	samples := []int64{
		1759062600, // idx=40  obs=13197.145339009843   vs 1.8599e-05 (×7.10e8)
		1759317540, // idx=47  obs=1.3364489336696532e-03 vs 2.0077e-05 (×65.57)
		1758431640, // idx=26  obs=4.6342748156391966e-05 vs 2.3133e-05 (×2.00)
		1761185340, // idx=79  obs=2.789477112339135e-05  vs 1.3938e-05 (×2.00)
		1758415680, // idx=24  obs=4.634868436719059e-05  vs 2.3168e-05 (×2.00)
		1757692800, // idx=14  obs=0.0                    vs 2.5147e-05 (100% low; ok=False in your run)
		1761185940, // idx=80  obs=2.797494436780589e-05  vs 1.5302e-05 (×1.83)
		1758923940, // idx=36  obs=4.2298866829669917e-07 vs 1.9353e-05 (≈−97.8%)
		1761222060, // idx=81  obs=7.041700891453499e-06  vs 1.5520e-05 (−54.6%)
		1761058320, // idx=77  obs=7.515342027738449e-06  vs 1.5033e-05 (−50.0%)
		1758006420, // idx=18  obs=2.6203158323767497e-05 vs 2.3092e-05 (+13.5%, IQR fence)
		1757673600, // idx=13  obs=2.2558483609277007e-05 vs 2.4759e-05 (−8.9%, IQR fence)
	}

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
