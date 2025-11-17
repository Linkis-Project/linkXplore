package solanaswapgo

import (
	"encoding/json"
	"fmt"
	"sort"

	ag_binary "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	"github.com/mr-tron/base58"
)

type JupiterSwapEvent struct {
	Amm          solana.PublicKey
	InputMint    solana.PublicKey
	InputAmount  uint64
	OutputMint   solana.PublicKey
	OutputAmount uint64
}

type JupiterSwapEventData struct {
	JupiterSwapEvent
	InputMintDecimals  uint8
	OutputMintDecimals uint8
}

// Anchor event discriminator for Jupiter RouteV2 event
var JupiterRouteEventDiscriminator = [16]byte{228, 69, 165, 46, 81, 203, 154, 29, 64, 198, 205, 232, 38, 8, 113, 226}

// processJupiterSwaps tries 2 paths:
//  1. Parse the dedicated Jupiter Route event (preferred).
//  2. Fallback: if the event isn't present, scan the *same inner-instruction set*
//     and delegate to AMM decoders (Raydium/Orca/Meteora/Pump.fun) like a router.
func (p *Parser) processJupiterSwaps(instructionIndex int) []SwapData {
	var swaps []SwapData
	foundRouteEvent := false

	// Try to capture the explicit RouteV2 event first.
	for _, innerInstructionSet := range p.txMeta.InnerInstructions {
		if innerInstructionSet.Index != uint16(instructionIndex) {
			continue
		}
		for _, innerInstruction := range innerInstructionSet.Instructions {
			inst := p.convertRPCToSolanaInstruction(innerInstruction)
			if p.isJupiterRouteEventInstruction(inst) {
				eventData, err := p.parseJupiterRouteEventInstruction(inst)
				if err != nil {
					p.Log.Errorf("error processing Jupiter route event: %s", err)
					continue
				}
				if eventData != nil {
					foundRouteEvent = true
					swaps = append(swaps, SwapData{Type: JUPITER, Data: eventData})
				}
			}
		}
	}

	if foundRouteEvent {
		return swaps
	}

	// Fallback router behavior:
	// Jupiter routed swaps often execute as CPIs to the underlying AMMs.
	// Reuse our router logic to extract those legs from the same inner set.
	routerLegs := p.processRouterSwaps(instructionIndex)
	if len(routerLegs) > 0 {
		return routerLegs
	}

	// As a last resort, harvest TokenProgram transfers right under this route.
	for _, innerInstructionSet := range p.txMeta.InnerInstructions {
		if innerInstructionSet.Index != uint16(instructionIndex) {
			continue
		}
		for _, innerInstruction := range innerInstructionSet.Instructions {
			inst := p.convertRPCToSolanaInstruction(innerInstruction)
			switch {
			case p.isTransferCheck(inst):
				if tr := p.processTransferCheck(inst); tr != nil {
					swaps = append(swaps, SwapData{Type: UNKNOWN, Data: tr})
				}
			case p.isTransfer(inst):
				if tr := p.processTransfer(inst); tr != nil {
					swaps = append(swaps, SwapData{Type: UNKNOWN, Data: tr})
				}
			}
		}
	}
	return swaps
}

// containsDCAProgram checks if the transaction contains the Jupiter DCA program.
func (p *Parser) containsDCAProgram() bool {
	for _, accountKey := range p.allAccountKeys {
		if accountKey.Equals(JUPITER_DCA_PROGRAM_ID) {
			return true
		}
	}
	return false
}

func (p *Parser) parseJupiterRouteEventInstruction(instruction solana.CompiledInstruction) (*JupiterSwapEventData, error) {
	decodedBytes, err := base58.Decode(instruction.Data.String())
	if err != nil {
		return nil, fmt.Errorf("error decoding instruction data: %s", err)
	}
	if len(decodedBytes) < 16 {
		return nil, fmt.Errorf("jupiter event data too short: %d", len(decodedBytes))
	}

	decoder := ag_binary.NewBorshDecoder(decodedBytes[16:])
	jupSwapEvent, err := handleJupiterRouteEvent(decoder)
	if err != nil {
		return nil, fmt.Errorf("error decoding jupiter swap event: %s", err)
	}

	inputMintDecimals, exists := p.splDecimalsMap[jupSwapEvent.InputMint.String()]
	if !exists {
		inputMintDecimals = 0
	}
	outputMintDecimals, exists := p.splDecimalsMap[jupSwapEvent.OutputMint.String()]
	if !exists {
		outputMintDecimals = 0
	}

	return &JupiterSwapEventData{
		JupiterSwapEvent:   *jupSwapEvent,
		InputMintDecimals:  inputMintDecimals,
		OutputMintDecimals: outputMintDecimals,
	}, nil
}

func handleJupiterRouteEvent(decoder *ag_binary.Decoder) (*JupiterSwapEvent, error) {
	var event JupiterSwapEvent
	if err := decoder.Decode(&event); err != nil {
		return nil, fmt.Errorf("error unmarshaling JupiterSwapEvent: %s", err)
	}
	return &event, nil
}

func (p *Parser) extractSPLDecimals() error {
	mintToDecimals := make(map[string]uint8)

	for _, accountInfo := range p.txMeta.PostTokenBalances {
		if !accountInfo.Mint.IsZero() {
			mintAddress := accountInfo.Mint.String()
			mintToDecimals[mintAddress] = uint8(accountInfo.UiTokenAmount.Decimals)
		}
	}

	processInstruction := func(instr solana.CompiledInstruction) {
		if !p.allAccountKeys[instr.ProgramIDIndex].Equals(solana.TokenProgramID) {
			return
		}
		if len(instr.Data) == 0 || (instr.Data[0] != 3 && instr.Data[0] != 12) {
			return
		}
		if len(instr.Accounts) < 3 {
			return
		}
		mint := p.allAccountKeys[instr.Accounts[1]].String()
		if _, exists := mintToDecimals[mint]; !exists {
			mintToDecimals[mint] = 0
		}
	}

	for _, instr := range p.txInfo.Message.Instructions {
		processInstruction(instr)
	}
	for _, innerSet := range p.txMeta.InnerInstructions {
		for _, instr := range innerSet.Instructions {
			processInstruction(p.convertRPCToSolanaInstruction(instr))
		}
	}

	// Native SOL (9 decimals)
	if _, exists := mintToDecimals[NATIVE_SOL_MINT_PROGRAM_ID.String()]; !exists {
		mintToDecimals[NATIVE_SOL_MINT_PROGRAM_ID.String()] = 9
	}

	p.splDecimalsMap = mintToDecimals
	return nil
}

// parseJupiterEvents aggregates all legs from Jupiter events into one SwapInfo.
//
// New logic: compute net per-mint flow across legs and select:
//   - TokenInMint  = mint with largest negative net (spent by the route)
//   - TokenOutMint = mint with largest positive net (received by the route)
//
// Amounts are the total *per-direction* sums for the chosen mints.
// This remains backward-compatible for single-hop routes.
func parseJupiterEvents(events []SwapData) (*SwapInfo, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("no events provided")
	}

	type agg struct {
		inSum, outSum uint64
		dec           uint8
	}
	perMint := make(map[string]*agg)

	ensure := func(m string, dec uint8) *agg {
		if a, ok := perMint[m]; ok {
			// Preserve a known non-zero decimals if present
			if a.dec == 0 && dec != 0 {
				a.dec = dec
			}
			return a
		}
		perMint[m] = &agg{dec: dec}
		return perMint[m]
	}

	for _, event := range events {
		if event.Type != JUPITER {
			continue
		}
		var leg JupiterSwapEventData
		raw, err := json.Marshal(event.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal event data: %v", err)
		}
		if err := json.Unmarshal(raw, &leg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Jupiter event data: %v", err)
		}

		inAgg := ensure(leg.InputMint.String(), leg.InputMintDecimals)
		outAgg := ensure(leg.OutputMint.String(), leg.OutputMintDecimals)

		inAgg.inSum += leg.InputAmount
		outAgg.outSum += leg.OutputAmount
	}

	if len(perMint) < 2 {
		return nil, fmt.Errorf("not enough distinct mints in Jupiter events")
	}

	// Compute net = outSum - inSum and pick extremes
	type netRow struct {
		mint string
		dec  uint8
		in   uint64
		out  uint64
		net  int64
	}
	rows := make([]netRow, 0, len(perMint))
	for m, a := range perMint {
		rows = append(rows, netRow{
			mint: m,
			dec:  a.dec,
			in:   a.inSum,
			out:  a.outSum,
			net:  int64(a.outSum) - int64(a.inSum),
		})
	}

	// Largest positive net = final out; most negative = true input
	sort.Slice(rows, func(i, j int) bool { return rows[i].net > rows[j].net })
	outRow := rows[0] // max net
	sort.Slice(rows, func(i, j int) bool { return rows[i].net < rows[j].net })
	inRow := rows[0] // min net

	// Safety: ensure they are different mints
	if inRow.mint == outRow.mint {
		// fallback to a stable heuristic: pick different mints if possible
		names := make([]string, 0, len(perMint))
		for m := range perMint {
			names = append(names, m)
		}
		sort.Strings(names)
		if len(names) >= 2 {
			inRow.mint = names[0]
			outRow.mint = names[len(names)-1]
			inRow.in = perMint[inRow.mint].inSum
			inRow.dec = perMint[inRow.mint].dec
			outRow.out = perMint[outRow.mint].outSum
			outRow.dec = perMint[outRow.mint].dec
		}
	}

	swapInfo := &SwapInfo{
		AMMs:             []string{string(JUPITER)},
		TokenInMint:      solana.MustPublicKeyFromBase58(inRow.mint),
		TokenInAmount:    inRow.in,
		TokenInDecimals:  inRow.dec,
		TokenOutMint:     solana.MustPublicKeyFromBase58(outRow.mint),
		TokenOutAmount:   outRow.out,
		TokenOutDecimals: outRow.dec,
	}
	return swapInfo, nil
}
