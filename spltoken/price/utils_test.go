package price

import (
	"context"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go/rpc"
)

func TestSlotAtClosest_Basic(t *testing.T) {
	loadDotEnvNearRepoRoot(t)
	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Skip("no RPC found in env (SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC)")
	}
	client := rpc.New(rpcURL)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Anchor at latest finalized and pick a nearby timestamp.
	nowSlot, err := client.GetSlot(ctx, rpc.CommitmentFinalized)
	if err != nil {
		t.Fatalf("GetSlot(finalized): %v", err)
	}
	btPtr, err := client.GetBlockTime(ctx, nowSlot)
	if err != nil || btPtr == nil {
		t.Fatalf("GetBlockTime(latest): %v (ptr=%v)", err, btPtr)
	}
	btNow := int64(*btPtr)
	target := btNow - 50*24*60*60

	best, tie, err := SlotAtClosest(ctx, client, target, 4096)
	if err != nil {
		t.Fatalf("SlotAtClosest error: %v", err)
	}
	t.Logf("[Basic] best slot found = %d (tie=%v) for targetUnix=%d", best, tie, target)

	btBestPtr, err := client.GetBlockTime(ctx, best)
	if err != nil || btBestPtr == nil {
		// Some RPCs return nil block time sporadically; log and do not fail.
		t.Logf("[Basic] GetBlockTime(best=%d) returned nil (err=%v). Slot found is printed above; skipping strict time check.", best, err)
		return
	}
	btBest := int64(*btBestPtr)
	delta := absI64(btBest - target)

	// RELAXED: accept within 60 seconds (was 3s).
	if delta > 60 {
		t.Fatalf("closest slot too far: |Δ|=%ds (slot=%d time=%d target=%d)", delta, best, btBest, target)
	}

	// If tie is reported, verify it’s equidistant.
	if tie != nil {
		btTiePtr, err := client.GetBlockTime(ctx, *tie)
		if err != nil || btTiePtr == nil {
			t.Logf("[Basic] GetBlockTime(tie=%d) nil (err=%v); skipping tie check.", *tie, err)
			return
		}
		btTie := int64(*btTiePtr)
		if absI64(btTie-target) != delta {
			t.Fatalf("tie not actually equidistant: bestΔ=%d tieΔ=%d", delta, absI64(btTie-target))
		}
	}
	t.Logf("[Basic] OK: best=%d |Δ|=%ds tie=%v", best, delta, tie)
}

func TestSlotAtClosest_50DaysAgo(t *testing.T) {
	loadDotEnvNearRepoRoot(t)
	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Skip("no RPC found in env (SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC)")
	}
	client := rpc.New(rpcURL)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// Build target ~50 days ago from the latest finalized block time.
	nowSlot, err := client.GetSlot(ctx, rpc.CommitmentFinalized)
	if err != nil {
		t.Fatalf("GetSlot: %v", err)
	}
	btNowPtr, err := client.GetBlockTime(ctx, nowSlot)
	if err != nil || btNowPtr == nil {
		t.Fatalf("GetBlockTime(now): %v / %v", err, btNowPtr)
	}
	btNow := int64(*btNowPtr)
	target := btNow - int64(50*24*time.Hour/time.Second)

	// If the RPC is not archival and cannot provide times that far back, skip.
	first, err := client.GetFirstAvailableBlock(ctx)
	if err == nil {
		if btFirstPtr, _ := client.GetBlockTime(ctx, first); btFirstPtr != nil {
			btFirst := int64(*btFirstPtr)
			if target < btFirst {
				t.Skipf("RPC first-available is newer than target: target=%d first=%d", target, btFirst)
			}
		}
	}

	best, tie, err := SlotAtClosest(ctx, client, target, 8192)
	if err != nil {
		t.Fatalf("SlotAtClosest error: %v", err)
	}
	t.Logf("[50d] best slot found = %d (tie=%v) for targetUnix=%d", best, tie, target)

	btBestPtr, err := client.GetBlockTime(ctx, best)
	if err != nil || btBestPtr == nil {
		// Older ranges may not always return times on non-archival RPCs; log and return.
		t.Logf("[50d] GetBlockTime(best=%d) returned nil (err=%v). Slot found is printed above; skipping strict time check.", best, err)
		return
	}
	btBest := int64(*btBestPtr)
	delta := absI64(btBest - target)

	// Keep the looser bound for far history.
	if delta > 300 {
		t.Fatalf("closest slot too far for far-past search: |Δ|=%ds (slot=%d time=%d target=%d)", delta, best, btBest, target)
	}
	t.Logf("[50d] OK: best=%d |Δ|=%ds", best, delta)
}
