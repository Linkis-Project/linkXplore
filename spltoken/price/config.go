package price

import (
	"os"

	"github.com/gagliardetto/solana-go"
)

// Default mainnet mints (used if env is missing/invalid).
const (
	mainnetUSDC = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	mainnetUSDT = "Es9vMFrzaCERmJfrFz4rQZf5nC5QgZFUY6BebquG4wNYB"
)

// Reads stablecoin mints, preferring environment (no hardcoding required by the user).
// If a value is missing or invalid, it falls back to the known mainnet mint.
func mustStableMintsFromEnv() (usdc solana.PublicKey, usdt solana.PublicKey) {
	usdcEnv := os.Getenv("SOLANA_USDC_CONTRACT_ADDRESS")
	usdtEnv := os.Getenv("SOLANA_USDT_CONTRACT_ADDRESS")

	// USDC: prefer env, else default
	if pk, err := solana.PublicKeyFromBase58(usdcEnv); err == nil {
		usdc = pk
	} else {
		usdc = solana.MustPublicKeyFromBase58(mainnetUSDC)
	}

	// USDT: prefer env, else default
	if pk, err := solana.PublicKeyFromBase58(usdtEnv); err == nil {
		usdt = pk
	} else {
		usdt = solana.MustPublicKeyFromBase58(mainnetUSDT)
	}

	return
}
