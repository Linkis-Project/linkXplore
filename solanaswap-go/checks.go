// checks.go
package solanaswapgo

import (
	"bytes"

	"github.com/gagliardetto/solana-go"
	"github.com/mr-tron/base58"
)

// Treat both Token and Token-2022 as token program (used in several places)
func (p *Parser) isTokenProgram(pk solana.PublicKey) bool {
	return pk.Equals(solana.TokenProgramID) || pk.Equals(solana.Token2022ProgramID)
}

// isTransfer: Token Program "Transfer" (3)
func (p *Parser) isTransfer(instr solana.CompiledInstruction) bool {
	progID := p.allAccountKeys[instr.ProgramIDIndex]
	if !progID.Equals(solana.TokenProgramID) {
		return false
	}
	if len(instr.Accounts) < 3 || len(instr.Data) < 9 {
		return false
	}
	if instr.Data[0] != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if int(instr.Accounts[i]) >= len(p.allAccountKeys) {
			return false
		}
	}
	return true
}

// isTransferCheck: Token or Token-2022 "TransferChecked" (12)
func (p *Parser) isTransferCheck(instr solana.CompiledInstruction) bool {
	progID := p.allAccountKeys[instr.ProgramIDIndex]
	if !p.isTokenProgram(progID) {
		return false
	}
	if len(instr.Accounts) < 4 || len(instr.Data) < 9 {
		return false
	}
	if instr.Data[0] != 12 {
		return false
	}
	for i := 0; i < 4; i++ {
		if int(instr.Accounts[i]) >= len(p.allAccountKeys) {
			return false
		}
	}
	return true
}

// (existing) pump/jup discriminators...

func (p *Parser) isPumpFunTradeEventInstruction(inst solana.CompiledInstruction) bool {
	if !p.allAccountKeys[inst.ProgramIDIndex].Equals(PUMP_FUN_PROGRAM_ID) || len(inst.Data) == 0 {
		return false
	}
	enc := inst.Data.String()
	if len(enc) == 0 {
		return false
	}
	decodedBytes, err := base58.Decode(enc)
	if err != nil || len(decodedBytes) < 16 {
		return false
	}
	return bytes.Equal(decodedBytes[:16], PumpfunTradeEventDiscriminator[:])
}

func (p *Parser) isJupiterRouteEventInstruction(inst solana.CompiledInstruction) bool {
	if !p.allAccountKeys[inst.ProgramIDIndex].Equals(JUPITER_PROGRAM_ID) || len(inst.Data) == 0 {
		return false
	}
	enc := inst.Data.String()
	if len(enc) == 0 {
		return false
	}
	decodedBytes, err := base58.Decode(enc)
	if err != nil || len(decodedBytes) < 16 {
		return false
	}
	return bytes.Equal(decodedBytes[:16], JupiterRouteEventDiscriminator[:])
}

// -------- NEW: tiny wrappers so users can call from checks.go if they prefer -----

// LiquidityAdd/Remove detectors (delegates to liquidity_ops.go)
func (p *Parser) IsAddLiquidity() bool    { return p.IsAddLiquidityTx() }
func (p *Parser) IsRemoveLiquidity() bool { return p.IsRemoveLiquidityTx() }
