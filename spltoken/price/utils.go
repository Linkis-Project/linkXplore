package price

import (
	"context"
	"errors"
	"math"
	"sort"

	"github.com/gagliardetto/solana-go/rpc"
)

// ---------- small helpers ----------

func absI64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// cappedAdd returns a+b but clamps within [0, max].
func cappedAdd(a uint64, b int64, max uint64) uint64 {
	if b == 0 {
		return a
	}
	if b > 0 {
		u := a + uint64(b)
		if u < a { // overflow
			return max
		}
		if u > max {
			return max
		}
		return u
	}
	// b < 0
	nb := uint64(-b)
	if nb > a {
		return 0
	}
	return a - nb
}

// ---------- VWAP utilities ----------

// VWAPWithLogFence computes a VWAP after filtering by:
//   - dust threshold on weights (w >= minWeight),
//   - symmetric log fence around the median with parameter r (>1): |ln(p) - ln(median)| <= ln(r).
//
// Returns (vwap, keptCount, weightSum, ok). If ok=false, no points passed the filters.
func VWAPWithLogFence(values []float64, weights []float64, r float64, minWeight float64) (float64, int, float64, bool) {
	n := len(values)
	if n == 0 || n != len(weights) || r <= 1.0 {
		return 0, 0, 0, false
	}

	// 1) dust filter
	type pw struct{ p, w float64 }
	f := make([]pw, 0, n)
	for i := 0; i < n; i++ {
		if !(weights[i] >= minWeight) || math.IsNaN(values[i]) || math.IsInf(values[i], 0) {
			continue
		}
		f = append(f, pw{p: values[i], w: weights[i]})
	}
	if len(f) == 0 {
		return 0, 0, 0, false
	}

	// 2) median of prices (by value, unweighted)
	ps := make([]float64, len(f))
	for i := range f {
		ps[i] = f[i].p
	}
	sort.Float64s(ps)
	var med float64
	m := len(ps)
	if m%2 == 1 {
		med = ps[m/2]
	} else {
		med = 0.5 * (ps[m/2-1] + ps[m/2])
	}
	if med <= 0 || math.IsNaN(med) || math.IsInf(med, 0) {
		return 0, 0, 0, false
	}

	// 3) symmetric log fence
	lnMed := math.Log(med)
	lnR := math.Log(r)
	sumW := 0.0
	sumWP := 0.0
	kept := 0
	for _, x := range f {
		if x.p <= 0 {
			continue
		}
		if math.Abs(math.Log(x.p)-lnMed) <= lnR {
			sumW += x.w
			sumWP += x.w * x.p
			kept++
		}
	}
	if sumW <= 0 {
		return 0, 0, 0, false
	}
	return sumWP / sumW, kept, sumW, true
}

// ---------- time→slot search (optimized/bracketing) ----------

// SlotAtClosest finds the slot whose block-time is closest to targetUnix.
// It returns best, optional tie, and an error.
// The search never uses firstSlot; instead it brackets around an estimated guess
// with an adaptive window that doubles until time(lo) <= target <= time(hi).
func SlotAtClosest(ctx context.Context, client *rpc.Client, targetUnix int64, maxProbes int) (best uint64, tie *uint64, err error) {
	if maxProbes <= 0 {
		maxProbes = 1024
	}
	const minuteSlack = int64(60) // NEW: if we're within 60s, accept immediately

	// 0) Edge: now slot/time
	dbg(ctx, "[slot] locating closest slot to unix=%d (maxProbes=%d)", targetUnix, maxProbes)
	nowSlot, err := client.GetSlot(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return 0, nil, err
	}
	btNowPtr, err := client.GetBlockTime(ctx, nowSlot)
	if err != nil || btNowPtr == nil {
		return 0, nil, errors.New("failed to read block time for latest finalized slot")
	}
	btNow := int64(*btNowPtr)

	if targetUnix <= 0 {
		targetUnix = btNow
	}
	if targetUnix >= btNow {
		// Target in the future (or exactly now): closest is nowSlot.
		return nowSlot, nil, nil
	}

	// 1) Estimate slots/sec and an initial guess near target time.
	guess, sps := estimateSlotGuess(ctx, client, nowSlot, btNow, targetUnix)
	DBGdt := func(slot uint64, t int64) {
		dbg(ctx, "[slot] candidate slot=%d |Δ|=%ds vs target", slot, absI64(t-targetUnix))
	}
	dbg(ctx, "[slot] initial guess=%d using sps≈%.3f", guess, sps)

	// 2) Build an initial window around guess using a % of lookback.
	lookbackSec := float64(btNow - targetUnix)
	if lookbackSec < 0 {
		lookbackSec = 0
	}
	// Base window seconds: max( fewSeconds , 5% * lookbackSec )
	const fewSeconds = 5.0
	winSec := lookbackSec * 0.05
	if winSec < fewSeconds {
		winSec = fewSeconds
	}
	// Convert to slots (respect a minimum slots window).
	minSlotsWindow := uint64(200) // floor to avoid tiny ranges for extreme near-now
	spanSlots := uint64(math.Round(sps * winSec))
	if spanSlots < minSlotsWindow {
		spanSlots = minSlotsWindow
	}
	// Clamp span so we stay inside [0, nowSlot].
	if spanSlots > nowSlot {
		spanSlots = nowSlot
	}

	// Probe budget & getBlockTime helper.
	getBT := func(slot uint64) (int64, bool) {
		if maxProbes <= 0 {
			return 0, false
		}
		maxProbes--
		ptr, err := client.GetBlockTime(ctx, slot)
		if err != nil || ptr == nil {
			return 0, false
		}
		return int64(*ptr), true
	}

	// 3) Try to resolve guess time (may be nil on pruned RPCs).
	tGuess, okGuess := getBT(guess)
	if okGuess {
		DBGdt(guess, tGuess)
		// NEW: if guess already within 60s, accept immediately
		if absI64(tGuess-targetUnix) <= minuteSlack {
			dbg(ctx, "[slot] guess within 60s (Δ=%ds); returning %d", absI64(tGuess-targetUnix), guess)
			return guess, nil, nil
		}
	}
	if !okGuess {
		// Light local nudge to find a nearby slot with time.
		for _, step := range []uint64{50, 200, 1000, 5000} {
			if guess >= step {
				if tg, ok := getBT(guess - step); ok {
					tGuess, okGuess = tg, true
					guess = guess - step
					DBGdt(guess, tGuess)
					if absI64(tGuess-targetUnix) <= minuteSlack {
						dbg(ctx, "[slot] nudged guess within 60s (Δ=%ds); returning %d", absI64(tGuess-targetUnix), guess)
						return guess, nil, nil
					}
					break
				}
			}
			if guess+step <= nowSlot {
				if tg, ok := getBT(guess + step); ok {
					tGuess, okGuess = tg, true
					guess = guess + step
					DBGdt(guess, tGuess)
					if absI64(tGuess-targetUnix) <= minuteSlack {
						dbg(ctx, "[slot] nudged guess within 60s (Δ=%ds); returning %d", absI64(tGuess-targetUnix), guess)
						return guess, nil, nil
					}
					break
				}
			}
		}
	}

	// 4) Adaptive bracketing.
	var lo, hi uint64
	var tLoOK, tHiOK bool
	var tLo, tHi int64

	expand := func(span uint64) (ok bool) {
		lo = cappedAdd(guess, -int64(span), nowSlot)
		hi = cappedAdd(guess, +int64(span), nowSlot)

		// Try to get times at lo/hi; if nil, nudge locally a bit.
		if tt, ok := getBT(lo); ok {
			tLo, tLoOK = tt, true
			DBGdt(lo, tLo)
			// NEW: accept if within 60s
			if absI64(tLo-targetUnix) <= minuteSlack {
				dbg(ctx, "[slot] lo within 60s (Δ=%ds); returning %d", absI64(tLo-targetUnix), lo)
				best = lo
				return true
			}
		} else {
			for _, step := range []uint64{1_000, 5_000, 25_000} {
				if lo+step <= hi {
					if tt, ok := getBT(lo + step); ok {
						lo += step
						tLo, tLoOK = tt, true
						DBGdt(lo, tLo)
						if absI64(tLo-targetUnix) <= minuteSlack {
							dbg(ctx, "[slot] lo+nudge within 60s (Δ=%ds); returning %d", absI64(tLo-targetUnix), lo)
							best = lo
							return true
						}
						break
					}
				}
			}
		}
		if tt, ok := getBT(hi); ok {
			tHi, tHiOK = tt, true
			DBGdt(hi, tHi)
			if absI64(tHi-targetUnix) <= minuteSlack {
				dbg(ctx, "[slot] hi within 60s (Δ=%ds); returning %d", absI64(tHi-targetUnix), hi)
				best = hi
				return true
			}
		} else {
			for _, step := range []uint64{1_000, 5_000, 25_000} {
				if hi > lo+step {
					if tt, ok := getBT(hi - step); ok {
						hi -= step
						tHi, tHiOK = tt, true
						DBGdt(hi, tHi)
						if absI64(tHi-targetUnix) <= minuteSlack {
							dbg(ctx, "[slot] hi-nudge within 60s (Δ=%ds); returning %d", absI64(tHi-targetUnix), hi)
							best = hi
							return true
						}
						break
					}
				}
			}
		}
		if !tLoOK && !tHiOK {
			return false
		}
		return true
	}

	if okGuess {
		if tGuess >= targetUnix {
			lo, hi = 0, guess
			tLoOK, tHiOK = false, true
			tHi = tGuess
		} else {
			lo, hi = guess, nowSlot
			tLoOK, tHiOK = true, false
			tLo = tGuess
		}
	} else {
		_ = expand(spanSlots)
	}

	for tries := 0; tries < 32 && maxProbes > 0; tries++ {
		if !tLoOK || !tHiOK {
			if !expand(spanSlots) {
				break
			}
			// expand() may already set best and early-return conditionally; if so, bail out now.
			if best != 0 && (absI64(tLo-targetUnix) <= minuteSlack || absI64(tHi-targetUnix) <= minuteSlack) {
				return best, nil, nil
			}
		}

		if tLoOK && tHiOK {
			if hi < lo {
				lo, hi = hi, lo
				tLo, tHi = tHi, tLo
			}
			if tLo <= targetUnix && targetUnix <= tHi {
				break
			}
			if targetUnix < tLo {
				newSpan := spanSlots * 2
				if newSpan == 0 {
					newSpan = spanSlots + 1
				}
				spanSlots = newSpan
				guess = lo
				tGuess = tLo
				okGuess = true
				tLoOK, tHiOK = false, false
				continue
			} else if targetUnix > tHi {
				newSpan := spanSlots * 2
				if newSpan == 0 {
					newSpan = spanSlots + 1
				}
				spanSlots = newSpan
				guess = hi
				tGuess = tHi
				okGuess = true
				tLoOK, tHiOK = false, false
				continue
			}
		} else {
			newSpan := spanSlots * 2
			if newSpan == 0 {
				newSpan = spanSlots + 1
			}
			spanSlots = newSpan
			continue
		}
	}

	if !(tLoOK && tHiOK) {
		if !tLoOK {
			if tt, ok := getBT(lo); ok {
				tLo, tLoOK = tt, true
			}
		}
		if !tHiOK {
			if tt, ok := getBT(hi); ok {
				tHi, tHiOK = tt, true
			}
		}
		if tLoOK && !tHiOK {
			return lo, nil, nil
		}
		if tHiOK && !tLoOK {
			return hi, nil, nil
		}
		if okGuess {
			return guess, nil, nil
		}
		return nowSlot, nil, nil
	}

	// 5) Binary search within [lo, hi].
	dbg(ctx, "[slot] bracketed in [%d, %d]; entering binary search", lo, hi)
	for iter := 0; lo+1 < hi && iter < 64 && maxProbes > 0; iter++ {
		mid := lo + (hi-lo)/2
		tMid, okMid := getBT(mid)
		if !okMid {
			if (mid - lo) <= (hi - mid) {
				if mid > lo {
					hi = mid
					tHi, tHiOK = tMid, false
				} else {
					lo = mid
					tLo, tLoOK = tMid, false
				}
			} else {
				if hi > mid {
					lo = mid
					tLo, tLoOK = tMid, false
				} else {
					hi = mid
					tHi, tHiOK = tMid, false
				}
			}
			continue
		}

		// NEW: accept as soon as mid is within 60s
		if d := absI64(tMid - targetUnix); d <= minuteSlack {
			dbg(ctx, "[slot] mid within 60s at %d (Δ=%ds); returning early", mid, d)
			return mid, nil, nil
		}

		if tMid == targetUnix {
			best = mid
			if mid+1 <= hi {
				if tNext, okN := getBT(mid + 1); okN && absI64(tNext-targetUnix) == 0 {
					tie = new(uint64)
					*tie = mid + 1
				}
			}
			return best, tie, nil
		}
		if tMid < targetUnix {
			lo, tLo, tLoOK = mid, tMid, true
		} else {
			hi, tHi, tHiOK = mid, tMid, true
		}
	}

	dLo := absI64(tLo - targetUnix)
	dHi := absI64(tHi - targetUnix)

	// NEW: final pass — if either bound is within 60s, prefer the closer and return
	if dLo <= minuteSlack || dHi <= minuteSlack {
		if dLo <= dHi {
			dbg(ctx, "[slot] done (<=60s): best=%d (Δ=%ds vs target)", lo, dLo)
			return lo, nil, nil
		}
		dbg(ctx, "[slot] done (<=60s): best=%d (Δ=%ds vs target)", hi, dHi)
		return hi, nil, nil
	}

	if dLo < dHi {
		dbg(ctx, "[slot] done: best=%d (Δ=%ds vs target)", lo, dLo)
		return lo, nil, nil
	}
	if dHi < dLo {
		dbg(ctx, "[slot] done: best=%d (Δ=%ds vs target)", hi, dHi)
		return hi, nil, nil
	}
	res := lo
	ti := hi
	dbg(ctx, "[slot] done: tie between %d and %d (equal distance); choose %d and report tie", lo, hi, res)
	return res, &ti, nil
}

// estimateSlotGuess computes a first guess at the target slot and returns (guessSlot, slotsPerSecondUsed).
// It prefers GetRecentPerformanceSamples; fallback is ~2.5 slots/sec.
func estimateSlotGuess(ctx context.Context, client *rpc.Client, nowSlot uint64, btNow int64, targetUnix int64) (guess uint64, sps float64) {
	limit := uint(60)
	samples, err := client.GetRecentPerformanceSamples(ctx, &limit)
	if err == nil && len(samples) > 0 {
		var slots uint64
		var secs uint64
		for _, s := range samples {
			secs += uint64(s.SamplePeriodSecs)
			slots += uint64(s.NumSlots)
		}
		if secs > 0 && slots > 0 {
			sps = float64(slots) / float64(secs) // slots per second
		}
	}
	if sps == 0 {
		sps = 2.5 // fallback heuristic
	}

	dt := float64(btNow - targetUnix)
	if dt < 0 {
		dt = 0
	}
	est := float64(nowSlot) - sps*dt
	if est < 0 {
		est = 0
	}
	guess = uint64(math.Round(est))
	if guess > nowSlot {
		guess = nowSlot
	}
	return guess, sps
}
