package price

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/joho/godotenv"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

func loadDotEnvNearRepoRoot(t *testing.T) {
	t.Helper()
	candidates := []string{
		"../../.env", "../.env", ".env", "../../../.env",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			_ = godotenv.Load(p)
			return
		}
	}
	wd, _ := os.Getwd()
	dir := wd
	for {
		envPath := filepath.Join(dir, ".env")
		if _, err := os.Stat(envPath); err == nil {
			_ = godotenv.Load(envPath)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

func pickRPCFromEnv() string {
	if v := os.Getenv("SOLANA_RPC_URL_FOR_PRICE"); v != "" {
		return v
	}
	if v := os.Getenv("SOLANA_RPC_URL"); v != "" {
		return v
	}
	if v := os.Getenv("HELIUS_RPC"); v != "" {
		return v
	}
	return ""
}

func TestFilterTxsByMint_Slot371148735(t *testing.T) {
	loadDotEnvNearRepoRoot(t)
	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Fatalf("no RPC found in env (expected SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC)")
	}
	client := rpc.New(rpcURL)

	slot := uint64(371159537)
	mint := solana.MustPublicKeyFromBase58("4NGbC4RRrUjS78ooSN53Up7gSg4dGrj6F6dxpMWHbonk")

	ctx := context.Background()
	filtered, err := FilterTxsByMint(ctx, client, slot, mint)
	if err != nil {
		t.Fatalf("FilterTxsByMint error: %v", err)
	}

	t.Logf("slot=%d → %d tx(s) CHANGED mint %s", slot, len(filtered), mint)

	for i, ft := range filtered {
		sig := "<nil>"
		if ft.Signature != nil {
			sig = ft.Signature.String()
		}
		t.Logf("[%d] sig=%s blockTime=%d totalDelta(base units)=%s accountsTouched=%d matches=%d",
			i, sig, ft.BlockTime, ft.TotalDelta.String(), len(ft.PerAccountDelta), len(ft.Touches))

		for _, m := range ft.Touches {
			keyStr := m.AccountKey.String()
			if m.AccountKey == (solana.PublicKey{}) {
				keyStr = "<unresolved>"
			}
			t.Logf("    - idx=%d key=%s owner=%s pre=%s post=%s delta=%s",
				m.AccountIndex, keyStr, m.Owner.String(), m.PreAmount, m.PostAmount, m.Delta.String())
		}
	}
}
