// liquidity_ops.go
package solanaswapgo

import (
	"bytes"
	"crypto/sha256"

	"github.com/gagliardetto/solana-go"
	"github.com/mr-tron/base58"
)

// LiquidityOp represents add/remove-liquidity classification.
type LiquidityOp int

const (
	LiquidityNone LiquidityOp = iota
	LiquidityAdd
	LiquidityRemove
)

// ------ AMM program allowlist (same spirit as filters.ts) ------
func (p *Parser) isAMMProgram(pk solana.PublicKey) bool {
	switch {
	// Pump.fun AMM
	case pk.Equals(PUMPFUN_AMM_PROGRAM_ID):
		return true
	// Meteora family (DLMM / Pools / DBC / DAMM v2)
	case pk.Equals(METEORA_PROGRAM_ID),
		pk.Equals(METEORA_POOLS_PROGRAM_ID),
		pk.Equals(METEORA_DLMM_PROGRAM_ID),
		pk.Equals(METEORA_DBC_PROGRAM_ID),
		pk.Equals(METEORA_DAMM_V2_PROGRAM_ID):
		return true
	// Orca whirlpools
	case pk.Equals(ORCA_PROGRAM_ID):
		return true
	// Raydium (v4/AMM/CPMM/CLMM/Launchpad)
	case pk.Equals(RAYDIUM_V4_PROGRAM_ID),
		pk.Equals(RAYDIUM_AMM_PROGRAM_ID),
		pk.Equals(RAYDIUM_CPMM_PROGRAM_ID),
		pk.Equals(RAYDIUM_CONCENTRATED_LIQUIDITY_PROGRAM_ID),
		pk.Equals(RAYDIUM_LAUNCHLAB_PROGRAM_ID):
		return true
	default:
		return false
	}
}

// ------ Token opcodes (SPL + Token-2022) ------
var (
	// 7=MintTo, 14=MintToChecked
	tokenMintOps = map[byte]struct{}{7: {}, 14: {}}
	// 8=Burn, 15=BurnChecked
	tokenBurnOps = map[byte]struct{}{8: {}, 15: {}}
)

func (p *Parser) tokenOpcodeIfAny(inst solana.CompiledInstruction) (byte, bool) {
	progID := p.allAccountKeys[inst.ProgramIDIndex]
	if !p.isTokenProgram(progID) {
		return 0, false
	}
	data := inst.Data
	if len(data) == 0 {
		return 0, false
	}
	return data[0], true
}

func (p *Parser) hasAnyTokenOpcode(opSet map[byte]struct{}) bool {
	// outer
	for _, ix := range p.txInfo.Message.Instructions {
		if op, ok := p.tokenOpcodeIfAny(ix); ok {
			if _, hit := opSet[op]; hit {
				return true
			}
		}
	}
	// inner
	for _, inner := range p.txMeta.InnerInstructions {
		for _, ri := range inner.Instructions {
			ix := p.convertRPCToSolanaInstruction(ri)
			if op, ok := p.tokenOpcodeIfAny(ix); ok {
				if _, hit := opSet[op]; hit {
					return true
				}
			}
		}
	}
	return false
}

// ------ Anchor discriminator helpers ------
func anchorDiscriminator8(name string) [8]byte {
	// first 8 bytes of sha256("global:"+name)
	sum := sha256.Sum256([]byte("global:" + name))
	var out [8]byte
	copy(out[:], sum[:8])
	return out
}

var addAnchors = func() map[[8]byte]struct{} {
	names := []string{
		"add_liquidity_by_strategy2",
		"add_liquidity_by_strategy",
		"add_liquidity_with_slippage",
		"add_liquidity",
		"increase_liquidity",
		"increase_liquidity_v2",
	}
	m := make(map[[8]byte]struct{}, len(names))
	for _, n := range names {
		m[anchorDiscriminator8(n)] = struct{}{}
	}
	return m
}()

// Expanded to catch Meteora DAMM v2 / pools variants commonly seen in the wild.
var removeAnchors = func() map[[8]byte]struct{} {
	names := []string{
		"remove_liquidity",
		"remove_liquidity_by_strategy",
		"remove_liquidity_by_strategy2",
		"decrease_liquidity",
		"decrease_liquidity_v2",
		"close_position",
		"withdraw",
		"withdraw_liquidity",
		"withdraw_one",
		"withdraw_one_token",
		"claim_and_withdraw",
	}
	m := make(map[[8]byte]struct{}, len(names))
	for _, n := range names {
		m[anchorDiscriminator8(n)] = struct{}{}
	}
	return m
}()

func (p *Parser) instDataPrefix8(inst solana.CompiledInstruction) (prefix [8]byte, ok bool) {
	enc := inst.Data.String()
	if len(enc) == 0 {
		return prefix, false
	}
	raw, err := base58.Decode(enc)
	if err != nil || len(raw) < 8 {
		return prefix, false
	}
	copy(prefix[:], raw[:8])
	return prefix, true
}

// ------ Scan helpers (outer + inner) ------
func (p *Parser) anyAMMProgramPresent() bool {
	// outer
	for _, ix := range p.txInfo.Message.Instructions {
		if p.isAMMProgram(p.allAccountKeys[ix.ProgramIDIndex]) {
			return true
		}
	}
	// inner
	for _, inner := range p.txMeta.InnerInstructions {
		for _, ri := range inner.Instructions {
			ix := p.convertRPCToSolanaInstruction(ri)
			if p.isAMMProgram(p.allAccountKeys[ix.ProgramIDIndex]) {
				return true
			}
		}
	}
	return false
}

func (p *Parser) hasAnchorPrefix(prefixes map[[8]byte]struct{}, ammOnly bool) bool {
	// outer
	for _, ix := range p.txInfo.Message.Instructions {
		if ammOnly && !p.isAMMProgram(p.allAccountKeys[ix.ProgramIDIndex]) {
			continue
		}
		if pre, ok := p.instDataPrefix8(ix); ok {
			if _, hit := prefixes[pre]; hit {
				return true
			}
		}
	}
	// inner
	for _, inner := range p.txMeta.InnerInstructions {
		for _, ri := range inner.Instructions {
			ix := p.convertRPCToSolanaInstruction(ri)
			if ammOnly && !p.isAMMProgram(p.allAccountKeys[ix.ProgramIDIndex]) {
				continue
			}
			if pre, ok := p.instDataPrefix8(ix); ok {
				if _, hit := prefixes[pre]; hit {
					return true
				}
			}
		}
	}
	return false
}

// Meteora-specific: broader weak remove-signal fallback (DLMM + DAMM v2 + DBC + Pools + main)
func (p *Parser) hasMeteoraRemoveContext() bool {
	// outer
	for _, ix := range p.txInfo.Message.Instructions {
		pid := p.allAccountKeys[ix.ProgramIDIndex]
		if pid.Equals(METEORA_DLMM_PROGRAM_ID) ||
			pid.Equals(METEORA_DAMM_V2_PROGRAM_ID) ||
			pid.Equals(METEORA_DBC_PROGRAM_ID) ||
			pid.Equals(METEORA_POOLS_PROGRAM_ID) ||
			pid.Equals(METEORA_PROGRAM_ID) {
			return true
		}
	}
	// inner
	for _, inner := range p.txMeta.InnerInstructions {
		for _, ri := range inner.Instructions {
			ix := p.convertRPCToSolanaInstruction(ri)
			pid := p.allAccountKeys[ix.ProgramIDIndex]
			if pid.Equals(METEORA_DLMM_PROGRAM_ID) ||
				pid.Equals(METEORA_DAMM_V2_PROGRAM_ID) ||
				pid.Equals(METEORA_DBC_PROGRAM_ID) ||
				pid.Equals(METEORA_POOLS_PROGRAM_ID) ||
				pid.Equals(METEORA_PROGRAM_ID) {
				return true
			}
		}
	}
	return false
}

// ------ Public detection ------
func (p *Parser) DetectLiquidityOp() LiquidityOp {
	// 1) Must see an AMM program
	if !p.anyAMMProgramPresent() {
		return LiquidityNone
	}

	// 2) Hard rules: burn → remove; mint → add
	if p.hasAnyTokenOpcode(tokenBurnOps) {
		return LiquidityRemove
	}
	if p.hasAnyTokenOpcode(tokenMintOps) {
		return LiquidityAdd
	}

	// 3) Anchor discriminators on AMM instructions
	if p.hasAnchorPrefix(addAnchors, true) {
		return LiquidityAdd
	}
	if p.hasAnchorPrefix(removeAnchors, true) {
		return LiquidityRemove
	}

	// 4) Fallback: any Meteora family signal → treat as remove (parity with JS fallback)
	if p.hasMeteoraRemoveContext() {
		return LiquidityRemove
	}

	return LiquidityNone
}

// Convenience predicates.
func (p *Parser) IsAddLiquidityTx() bool    { return p.DetectLiquidityOp() == LiquidityAdd }
func (p *Parser) IsRemoveLiquidityTx() bool { return p.DetectLiquidityOp() == LiquidityRemove }

// For completeness (parity with TS): quick byte-prefix compare utility.
func bytesEq8(a [8]byte, b [8]byte) bool { return bytes.Equal(a[:], b[:]) }
