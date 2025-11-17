package solanaswapgo

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/gagliardetto/solana-go"
	"github.com/mr-tron/base58"
)

var (
	OKX_SWAP_DISCRIMINATOR                 = [8]byte{248, 198, 158, 145, 225, 117, 135, 200}
	OKX_SWAP2_DISCRIMINATOR                = [8]byte{65, 75, 63, 76, 235, 91, 91, 136}
	OKX_COMMISSION_SPL_SWAP2_DISCRIMINATOR = [8]byte{173, 131, 78, 38, 150, 165, 123, 15}
	OKX_SWAP3_DISCRIMINATOR                = [8]byte{19, 44, 130, 148, 72, 56, 44, 238}
)

// OKXSwapEventData is a router-level aggregate (authoritative net in/out).
// We derive it from OKX program logs: source_token_change & destination_token_change.
type OKXSwapEventData struct {
	InputMint        solana.PublicKey
	InputAmount      uint64
	InputDecimals    uint8
	OutputMint       solana.PublicKey
	OutputAmount     uint64
	OutputDecimals   uint8
	CommissionAmount uint64 // optional, if we can parse it
}

// Try to parse an authoritative aggregate from OKX logs.
// Example line (see Solscan Page 22):
// "Program log: after_source_balance: 0, after_destination_balance: 2385716221310,
//
//	source_token_change: 150000000000, destination_token_change: 2385716221310"
func (p *Parser) parseOKXAggregateFromLogs(instructionIndex int) *OKXSwapEventData {
	if p.txMeta == nil || p.txMeta.LogMessages == nil {
		return nil
	}

	// Extract mints from the *outer* OKX instruction if available.
	// In OKX Aggregation Router V2 (Swap_tob_v3), accounts align as:
	// [0] payer, [1] src token acct, [2] dst token acct,
	// [3] source mint, [4] destination mint, ...
	// We defensively guard indices.
	var srcMint, dstMint solana.PublicKey
	okxInstr := p.txInfo.Message.Instructions[instructionIndex]
	if len(okxInstr.Accounts) >= 5 {
		srcMint = p.allAccountKeys[okxInstr.Accounts[3]]
		dstMint = p.allAccountKeys[okxInstr.Accounts[4]]
	}

	// Fallback protection: if we couldn't read mints (layout change),
	// don't emit an aggregate; we will still fall back to leg collection.
	if srcMint.IsZero() || dstMint.IsZero() {
		return nil
	}

	// Regexes for robust parsing
	aggRe := regexp.MustCompile(`after_source_balance:\s*\d+.*?source_token_change:\s*(\d+),\s*destination_token_change:\s*(\d+)`)
	commissionRe := regexp.MustCompile(`commission_amount:\s*(\d+)`)

	var srcDelta, dstDelta, commission uint64
	for _, line := range p.txMeta.LogMessages {
		// Only consider OKX router context lines to reduce false positives
		// Cheap filter:
		if !strings.Contains(line, "Program log:") {
			continue
		}
		if strings.Contains(line, "OKX DEX: Aggregation Router V2") || strings.Contains(line, "SwapTobV3") ||
			strings.Contains(line, "after_source_balance") || strings.Contains(line, "source_token_change") {
			if m := aggRe.FindStringSubmatch(line); len(m) == 3 {
				if v, err := strconv.ParseUint(m[1], 10, 64); err == nil {
					srcDelta = v
				}
				if v, err := strconv.ParseUint(m[2], 10, 64); err == nil {
					dstDelta = v
				}
			}
			if c := commissionRe.FindStringSubmatch(line); len(c) == 2 {
				if v, err := strconv.ParseUint(c[1], 10, 64); err == nil {
					commission = v
				}
			}
		}
	}

	if srcDelta == 0 && dstDelta == 0 {
		return nil
	}

	// Resolve decimals from earlier extracted maps; SOL explicitly 9.
	inDec := p.splDecimalsMap[srcMint.String()]
	outDec := p.splDecimalsMap[dstMint.String()]
	if srcMint.Equals(NATIVE_SOL_MINT_PROGRAM_ID) && inDec == 0 {
		inDec = 9
	}
	if outDec == 0 {
		// If unknown, default to 0 but keep going (backward-safe).
		outDec = 0
	}

	return &OKXSwapEventData{
		InputMint:        srcMint,
		InputAmount:      srcDelta,
		InputDecimals:    inDec,
		OutputMint:       dstMint,
		OutputAmount:     dstDelta,
		OutputDecimals:   outDec,
		CommissionAmount: commission,
	}
}

func (p *Parser) processOKXSwaps(instructionIndex int) []SwapData {
	p.Log.Infof("starting okx swap parsing for instruction index: %d", instructionIndex)

	parentInstruction := p.txInfo.Message.Instructions[instructionIndex]
	programID := p.allAccountKeys[parentInstruction.ProgramIDIndex]

	if !programID.Equals(OKX_DEX_ROUTER_PROGRAM_ID) {
		p.Log.Warnf("instruction %d skipped: not okx dex router program", instructionIndex)
		return nil
	}

	if len(parentInstruction.Data) < 8 {
		p.Log.Warnf("instruction %d skipped: data too short (%d)", instructionIndex, len(parentInstruction.Data))
		return nil
	}

	decodedBytes, err := base58.Decode(parentInstruction.Data.String())
	if err != nil {
		p.Log.Errorf("failed to decode okx swap instruction %d: %s", instructionIndex, err)
		return nil
	}

	discriminator := decodedBytes[:8]
	p.Log.Infof("decoded okx swap instruction %d with discriminator: %x", instructionIndex, discriminator)

	// Always attempt to get the authoritative aggregate from logs (backward-safe).
	agg := p.parseOKXAggregateFromLogs(instructionIndex)
	if agg != nil {
		p.Log.Infof("OKX aggregate parsed from logs: in=%d out=%d", agg.InputAmount, agg.OutputAmount)
	}

	var legs []SwapData
	switch {
	case bytes.Equal(discriminator, OKX_SWAP_DISCRIMINATOR[:]),
		bytes.Equal(discriminator, OKX_SWAP2_DISCRIMINATOR[:]),
		bytes.Equal(discriminator, OKX_COMMISSION_SPL_SWAP2_DISCRIMINATOR[:]),
		bytes.Equal(discriminator, OKX_SWAP3_DISCRIMINATOR[:]):
		legs = p.processOKXRouterSwaps(instructionIndex)
	default:
		p.Log.Warnf("unknown okx swap discriminator %x for instruction %d", discriminator, instructionIndex)
		legs = p.processOKXRouterSwaps(instructionIndex)
	}

	var swaps []SwapData
	// If we have an aggregate, surface it *first* so ProcessSwapData can prefer it.
	if agg != nil {
		swaps = append(swaps, SwapData{Type: OKX, Data: agg})
	}
	swaps = append(swaps, legs...)
	p.Log.Infof("processed okx swaps: %d total (aggregate=%v, legs=%d)", len(swaps), agg != nil, len(legs))
	return swaps
}

func (p *Parser) processOKXRouterSwaps(instructionIndex int) []SwapData {
	var swaps []SwapData
	seen := make(map[string]bool)
	processedProtocols := make(map[SwapType]bool)

	innerInstructions := p.getInnerInstructions(instructionIndex)
	p.Log.Infof("processing okx router swaps for instruction %d: %d inner instructions", instructionIndex, len(innerInstructions))
	if len(innerInstructions) == 0 {
		p.Log.Warnf("no inner instructions for instruction %d", instructionIndex)
		return swaps
	}

	for _, inner := range innerInstructions {
		progID := p.allAccountKeys[inner.ProgramIDIndex]

		switch {
		case progID.Equals(RAYDIUM_V4_PROGRAM_ID) ||
			progID.Equals(RAYDIUM_CPMM_PROGRAM_ID) ||
			progID.Equals(RAYDIUM_AMM_PROGRAM_ID) ||
			progID.Equals(RAYDIUM_CONCENTRATED_LIQUIDITY_PROGRAM_ID):
			if processedProtocols[RAYDIUM] {
				continue
			}
			if raydSwaps := p.processRaydSwaps(instructionIndex); len(raydSwaps) > 0 {
				for _, swap := range raydSwaps {
					key := getSwapKey(swap)
					if !seen[key] {
						swaps = append(swaps, swap)
						seen[key] = true
					}
				}
				processedProtocols[RAYDIUM] = true
			}

		case progID.Equals(ORCA_PROGRAM_ID):
			if processedProtocols[ORCA] {
				continue
			}
			if orcaSwaps := p.processOrcaSwaps(instructionIndex); len(orcaSwaps) > 0 {
				for _, swap := range orcaSwaps {
					key := getSwapKey(swap)
					if !seen[key] {
						swaps = append(swaps, swap)
						seen[key] = true
					}
				}
				processedProtocols[ORCA] = true
			}

		case progID.Equals(METEORA_PROGRAM_ID) ||
			progID.Equals(METEORA_POOLS_PROGRAM_ID) ||
			progID.Equals(METEORA_DLMM_PROGRAM_ID) ||
			progID.Equals(METEORA_DBC_PROGRAM_ID): // NEW: DBC recognized under OKX router
			if processedProtocols[METEORA] {
				continue
			}
			if meteoraSwaps := p.processMeteoraSwaps(instructionIndex); len(meteoraSwaps) > 0 {
				for _, swap := range meteoraSwaps {
					key := getSwapKey(swap)
					if !seen[key] {
						swaps = append(swaps, swap)
						seen[key] = true
					}
				}
				processedProtocols[METEORA] = true
			}

		case progID.Equals(PUMP_FUN_PROGRAM_ID):
			if processedProtocols[PUMP_FUN] {
				continue
			}
			if pumpfunSwaps := p.processPumpfunSwaps(instructionIndex); len(pumpfunSwaps) > 0 {
				for _, swap := range pumpfunSwaps {
					key := getSwapKey(swap)
					if !seen[key] {
						swaps = append(swaps, swap)
						seen[key] = true
					}
				}
				processedProtocols[PUMP_FUN] = true
			}
		}
	}

	p.Log.Infof("processed okx router legs: %d unique legs", len(swaps))
	return swaps
}

func getSwapKey(swap SwapData) string {
	switch data := swap.Data.(type) {
	case *TransferCheck:
		return fmt.Sprintf("%s-%s-%s", swap.Type, data.Info.TokenAmount.Amount, data.Info.Mint)
	case *TransferData:
		return fmt.Sprintf("%s-%d-%s", swap.Type, data.Info.Amount, data.Mint)
	default:
		return fmt.Sprintf("%s-%v", swap.Type, data)
	}
}
