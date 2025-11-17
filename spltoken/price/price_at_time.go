package price

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// estimateBackoffSlotsForDays computes how many *slots* roughly occur over `days`,
func estimateBackoffSlotsForDays(ctx context.Context, client *rpc.Client, days float64) int {
	if days <= 0 {
		return 0
	}
	const fallbackSPS = 2.5
	limit := uint(60)
	samples, err := client.GetRecentPerformanceSamples(ctx, &limit)
	sps := 0.0
	if err == nil && len(samples) > 0 {
		var totalSlots uint64
		var totalSecs uint64
		for _, s := range samples {
			totalSlots += uint64(s.NumSlots)
			totalSecs += uint64(s.SamplePeriodSecs)
		}
		if totalSecs > 0 && totalSlots > 0 {
			sps = float64(totalSlots) / float64(totalSecs)
		}
	}
	if sps <= 0 {
		sps = fallbackSPS
	}
	seconds := days * 24 * 60 * 60
	slots := int(math.Ceil(sps * seconds))
	if slots < 1 {
		slots = 1
	}
	return slots
}

func GetTokenUSDPriceAtUnix(
	ctx context.Context,
	client *rpc.Client,
	targetMint solana.PublicKey,
	tUnix int64,
	backoffSlots int,
	fenceR float64,
	minWUSD float64,
) (vwapUSD float64, kept int, sumW float64, ok bool, err error) {

	if client == nil {
		return 0, 0, 0, false, errors.New("nil rpc client")
	}
	if tUnix <= 0 {
		return 0, 0, 0, false, errors.New("invalid timestamp")
	}
	if backoffSlots <= 0 {
		backoffSlots = estimateBackoffSlotsForDays(ctx, client, 8.0)
	}
	if fenceR <= 1.0 || math.IsNaN(fenceR) {
		fenceR = 1.5
	}
	if minWUSD <= 0 || math.IsNaN(minWUSD) {
		minWUSD = 1e-6
	}

	best, _, err := SlotAtClosest(ctx, client, tUnix, 4096)
	if err != nil {
		return 0, 0, 0, false, err
	}
	dbg(ctx, "[vwap] target=%s unix=%d → closest slot=%d (backoff cap ~%d)", targetMint.String(), tUnix, best, backoffSlots)

	values := make([]float64, 0, 8)
	weights := make([]float64, 0, 8)

	addPoints := func(ps []PricePoint, slot uint64) {
		dbg(ctx, "[vwap] slot=%d: checking %d point(s)", slot, len(ps))
		for _, p := range ps {
			if p.PriceUSD <= 0 || p.TargetQtyFloat <= 0 {
				dbg(ctx, "[vwap]   drop sig=%s: priceUSD=%.10f qty=%.6f", p.Signature, p.PriceUSD, p.TargetQtyFloat)
				continue
			}
			var w float64
			if p.BaseIsStable {
				w = float64(p.BaseAmountRaw) / math.Pow10(p.BaseDecimals)
				dbg(ctx, "[vwap]   keep sig=%s: STABLE w=%.10f price=%.10f", p.Signature, w, p.PriceUSD)
			} else if p.BaseIsSOL {
				w = p.PriceUSD * p.TargetQtyFloat
				dbg(ctx, "[vwap]   keep sig=%s: SOL w=%.10f price=%.10f", p.Signature, w, p.PriceUSD)
			} else {
				dbg(ctx, "[vwap]   drop sig=%s: base not SOL/USDC/USDT", p.Signature)
				continue
			}
			if w <= 0 || math.IsNaN(w) || math.IsInf(w, 0) {
				dbg(ctx, "[vwap]   drop sig=%s: invalid weight w=%.8f", p.Signature, w)
				continue
			}
			values = append(values, p.PriceUSD)
			weights = append(weights, w)
		}
	}

	// Try the closest slot first.
	if pts, err := GetPricesAtSlot(ctx, client, best, targetMint); err == nil && len(pts) > 0 {
		addPoints(pts, best)
	}

	// If still empty, walk backward until we find any priceable swaps or hit the cap.
	scanned := 0
	curr := best
	for len(values) == 0 && scanned < backoffSlots {
		if curr == 0 {
			break
		}
		curr--

		if scanned%5000 == 0 { // not too chatty
			dbg(ctx, "[vwap] scanning back: curr=%d scanned=%d/%d", curr, scanned, backoffSlots)
		}

		pts, err := GetPricesAtSlot(ctx, client, curr, targetMint)
		if err != nil {
			scanned++
			continue
		}
		if len(pts) > 0 {
			dbg(ctx, "[vwap] first non-empty slot found at %d", curr)
			addPoints(pts, curr)
			break
		}
		scanned++
	}

	if len(values) == 0 {
		dbg(ctx, "[vwap] no USD-priceable swaps found (scanned=%d)", scanned)
		return 0, 0, 0, false, errors.New("no USD-priceable swaps found in the search window")
	}

	v, k, sw, ok := VWAPWithLogFence(values, weights, fenceR, minWUSD)
	dbg(ctx, "[vwap] result: v=%.10f kept=%d sumW=%.6f ok=%v", v, k, sw, ok)
	return v, k, sw, ok, nil
}

// GetTokenUSDPriceAtTime convenience wrapper using time.Time (UTC assumed).
func GetTokenUSDPriceAtTime(
	ctx context.Context,
	client *rpc.Client,
	targetMint solana.PublicKey,
	t time.Time,
) (float64, int, float64, bool, error) {
	return GetTokenUSDPriceAtUnix(ctx, client, targetMint, t.UTC().Unix(), 0, 1.5, 1e-6)
}
