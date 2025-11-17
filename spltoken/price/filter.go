package price

import (
	"context"
	"fmt"
	"math/big"

	"github.com/AlekSi/pointer"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// BalanceTouch captures the exact token-balance rows that matched the target mint.
type BalanceTouch struct {
	AccountIndex uint64
	AccountKey   solana.PublicKey // from message account keys (may be zero if we didn't resolve)
	Owner        solana.PublicKey // pointer-safe copy from RPC token-balance row
	PreAmount    string           // raw base units as decimal string
	PostAmount   string           // raw base units as decimal string
	Delta        *big.Int         // post - pre
}

// FilteredTx contains info needed downstream + proof of match.
type FilteredTx struct {
	Slot            uint64
	BlockTime       int64
	PerAccountDelta map[uint64]*big.Int
	TotalDelta      *big.Int

	Signature *solana.Signature
	Accounts  []solana.PublicKey

	Touches []BalanceTouch // only for the target mint; non-empty and at least one Delta != 0
}

// FilterTxsByMint scans a block at `slot` and returns only transactions
// that *change* balances of `targetMint`. It uses pre/post token-balance
// deltas so it works across inner instructions/routers.
func FilterTxsByMint(
	ctx context.Context,
	client *rpc.Client,
	slot uint64,
	targetMint solana.PublicKey,
) ([]*FilteredTx, error) {

	blk, err := client.GetBlockWithOpts(ctx, slot, &rpc.GetBlockOpts{
		Commitment:                     rpc.CommitmentFinalized,
		TransactionDetails:             rpc.TransactionDetailsFull,
		Rewards:                        pointer.ToBool(false),
		MaxSupportedTransactionVersion: pointer.ToUint64(0),
		// Encoding: solana.EncodingBase64Zstd, // optional
	})
	if err != nil {
		return nil, fmt.Errorf("getBlock(%d): %w", slot, err)
	}
	if blk == nil {
		return nil, nil
	}

	// helper to safely dereference *solana.PublicKey
	pkOrZero := func(p *solana.PublicKey) solana.PublicKey {
		if p == nil {
			return solana.PublicKey{}
		}
		return *p
	}

	var out []*FilteredTx

	for _, txw := range blk.Transactions {
		meta := txw.Meta
		if meta == nil {
			continue
		}

		// Decode once so we can map accountIndex -> pubkey (best-effort).
		var accounts []solana.PublicKey
		var sigPtr *solana.Signature
		if parsedTx, err := txw.GetTransaction(); err == nil && parsedTx != nil {
			accounts = parsedTx.Message.AccountKeys
			if len(parsedTx.Signatures) > 0 {
				s := parsedTx.Signatures[0]
				sigPtr = &s
			}
		}

		indexToKey := func(i uint64) solana.PublicKey {
			if int(i) < len(accounts) {
				return accounts[i]
			}
			return solana.PublicKey{} // zero → not resolved (ALT etc.)
		}

		// Collect pre/post by index for the target mint, plus owners.
		type row struct {
			mint  solana.PublicKey
			owner solana.PublicKey
			amt   string
		}
		preByIdx := map[uint64]row{}
		postByIdx := map[uint64]row{}

		for _, b := range meta.PreTokenBalances {
			if b.Mint.Equals(targetMint) {
				preByIdx[uint64(b.AccountIndex)] = row{
					mint:  b.Mint,
					owner: pkOrZero(b.Owner),
					amt:   b.UiTokenAmount.Amount,
				}
			}
		}
		for _, b := range meta.PostTokenBalances {
			if b.Mint.Equals(targetMint) {
				postByIdx[uint64(b.AccountIndex)] = row{
					mint:  b.Mint,
					owner: pkOrZero(b.Owner),
					amt:   b.UiTokenAmount.Amount,
				}
			}
		}

		// If no appearance of the mint at all, skip.
		if len(preByIdx) == 0 && len(postByIdx) == 0 {
			continue
		}

		// Build deltas and touches; require at least one non-zero delta.
		parse := func(s string) *big.Int {
			n := new(big.Int)
			if _, ok := n.SetString(s, 10); !ok {
				return big.NewInt(0)
			}
			return n
		}

		seen := map[uint64]struct{}{}
		for k := range preByIdx {
			seen[k] = struct{}{}
		}
		for k := range postByIdx {
			seen[k] = struct{}{}
		}

		perAcct := make(map[uint64]*big.Int)
		total := big.NewInt(0)
		touches := make([]BalanceTouch, 0, len(seen))
		anyNonZero := false

		for idx := range seen {
			preAmt := "0"
			postAmt := "0"
			owner := solana.PublicKey{}

			if r, ok := preByIdx[idx]; ok {
				preAmt = r.amt
				owner = r.owner
			}
			if r, ok := postByIdx[idx]; ok {
				postAmt = r.amt
				// prefer owner from post if present
				if r.owner != (solana.PublicKey{}) {
					owner = r.owner
				}
			}

			d := new(big.Int).Sub(parse(postAmt), parse(preAmt))
			perAcct[idx] = d
			total.Add(total, d)

			if d.Sign() != 0 {
				anyNonZero = true
			}

			touches = append(touches, BalanceTouch{
				AccountIndex: idx,
				AccountKey:   indexToKey(idx),
				Owner:        owner,
				PreAmount:    preAmt,
				PostAmount:   postAmt,
				Delta:        new(big.Int).Set(d),
			})
		}

		// Only keep tx if something actually changed for this mint.
		if !anyNonZero {
			continue
		}

		// Pick a time: prefer per-tx time, else fall back to block time.
		var blockTime int64
		if txw.BlockTime != nil {
			blockTime = int64(*txw.BlockTime)
		} else if blk.BlockTime != nil {
			blockTime = int64(*blk.BlockTime)
		}

		out = append(out, &FilteredTx{
			Slot:            slot,
			BlockTime:       blockTime,
			PerAccountDelta: perAcct,
			TotalDelta:      total,
			Signature:       sigPtr,
			Accounts:        accounts,
			Touches:         touches,
		})
	}

	return out, nil
}
