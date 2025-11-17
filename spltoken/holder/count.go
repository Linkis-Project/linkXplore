package holder

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// Env var name (exact string you asked for).
const EnvRPCForCounter = "SOLANA_RPC_URL_FOR_COUNTER"

// SPL program IDs.
var (
	ProgramToken      = solana.MustPublicKeyFromBase58("TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA")
	ProgramToken2022  = solana.MustPublicKeyFromBase58("TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb")
	tokenAcctDataSize = uint64(165) // Token account layout size
)

type Result struct {
	Holders       int
	TotalAccounts int
	ProgramUsed   solana.PublicKey
}

// CountHoldersForMint reproduces the TS "auto" behavior.
// Contract you requested:
//   - This function will keep retrying rate-limits until it returns.
//   - If a non-rate-limit fatal error occurs, it returns that error.
func CountHoldersForMint(ctx context.Context, mintBase58 string) (*Result, error) {
	mint, err := solana.PublicKeyFromBase58(mintBase58)
	if err != nil {
		return nil, fmt.Errorf("invalid mint: %w", err)
	}
	rpcURL := os.Getenv(EnvRPCForCounter)
	if rpcURL == "" {
		return nil, fmt.Errorf("%s is not set", EnvRPCForCounter)
	}
	client := rpc.New(rpcURL)

	// Try Token first.
	if r, err := countForProgram(ctx, client, mint, ProgramToken); err == nil && r.TotalAccounts > 0 {
		r.ProgramUsed = ProgramToken
		return &r, nil
	} else if err != nil && !isMethodNotFound(err) && !isTokenScanUnavailable(err) {
		return nil, fmt.Errorf("token scan error: %w", err)
	}

	// Then Token-2022.
	if r, err := countForProgram(ctx, client, mint, ProgramToken2022); err == nil && r.TotalAccounts > 0 {
		r.ProgramUsed = ProgramToken2022
		return &r, nil
	} else if err != nil && !isMethodNotFound(err) && !isTokenScanUnavailable(err) {
		return nil, fmt.Errorf("token2022 scan error: %w", err)
	}

	// If both scans fail due to disabled index / method missing, return zeroes (same as TS "no token accounts found").
	return &Result{Holders: 0, TotalAccounts: 0}, nil
}

// countForProgram performs filtered getProgramAccounts and parses JSON the same way as the TS script.
func countForProgram(ctx context.Context, client *rpc.Client, mint solana.PublicKey, programID solana.PublicKey) (Result, error) {
	var out rpc.GetProgramAccountsResult
	var err error

	// jittered retry for transient rate limits
	const maxAttempts = 8
	const base = 250 * time.Millisecond

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out, err = client.GetProgramAccountsWithOpts(
			ctx,
			programID,
			&rpc.GetProgramAccountsOpts{
				Filters: []rpc.RPCFilter{
					{DataSize: tokenAcctDataSize},
					{Memcmp: &rpc.RPCFilterMemcmp{
						Offset: 0,
						Bytes:  mint.Bytes(), // account.mint field
					}},
				},
				Encoding:   solana.EncodingJSONParsed, // match TS
				Commitment: rpc.CommitmentConfirmed,
			},
		)
		if err == nil {
			break
		}
		// Only retry for throttling/busyness; bubble up all other errors.
		if !(isRateLimited(err) || isTooManyRequests(err) || isServerBusy(err)) {
			return Result{}, err
		}
		j := time.Duration(rand.Int63n(int64(150 * time.Millisecond)))
		time.Sleep(base*time.Duration(attempt) + j)
	}
	if err != nil {
		return Result{}, err
	}

	type parsedAccount struct {
		Program string `json:"program"`
		Parsed  struct {
			Info struct {
				TokenAmount struct {
					Amount string `json:"amount"`
				} `json:"tokenAmount"`
				Owner string `json:"owner"`
			} `json:"info"`
			Type string `json:"type"`
		} `json:"parsed"`
		Space int `json:"space"`
	}

	owners := make(map[string]struct{})
	total := 0

	for _, ka := range out {
		total++
		raw := ka.Account.Data.GetRawJSON()
		if len(raw) == 0 {
			continue
		}
		var p parsedAccount
		if jsonErr := json.Unmarshal(raw, &p); jsonErr != nil {
			continue
		}
		amt := p.Parsed.Info.TokenAmount.Amount
		own := p.Parsed.Info.Owner
		if own != "" && amt != "" && amt != "0" {
			owners[own] = struct{}{}
		}
	}

	return Result{
		Holders:       len(owners),
		TotalAccounts: total,
		ProgramUsed:   programID,
	}, nil
}

// ---- tiny error helpers (string contains, case-insensitive) ----

func isRateLimited(err error) bool {
	return containsAny(err, "rate limit", "rate-limited", "429", "too many requests")
}
func isTooManyRequests(err error) bool { return containsAny(err, "too many requests") }
func isServerBusy(err error) bool {
	return containsAny(err, "server busy", "try again later", "overloaded")
}
func isMethodNotFound(err error) bool { return containsAny(err, "method not found", "-32601") }

// Many providers phrase this differently; make the detector broad:
func isTokenScanUnavailable(err error) bool {
	return containsAny(
		err,
		"excluded from account secondary indexes",
		"secondary indexes are disabled",
		"account indexes disabled",
		"this rpc method unavailable for key",
		"unsupported filters on this plan",
	)
}

func containsAny(err error, subs ...string) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// lowercase without importing strings
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	ls := string(b)
	for _, sub := range subs {
		sb := []byte(sub)
		for i := range sb {
			if sb[i] >= 'A' && sb[i] <= 'Z' {
				sb[i] += 'a' - 'A'
			}
		}
		ns := string(sb)
		if indexOf(ls, ns) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(haystack, needle string) int {
	nh := len(haystack)
	nn := len(needle)
	if nn == 0 {
		return 0
	}
	for i := 0; i+nn <= nh; i++ {
		if haystack[i:i+nn] == needle {
			return i
		}
	}
	return -1
}
