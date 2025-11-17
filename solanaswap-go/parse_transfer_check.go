package solanaswapgo

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/gagliardetto/solana-go"
)

type TransferCheck struct {
	Info struct {
		Authority   string `json:"authority"`
		Destination string `json:"destination"`
		Mint        string `json:"mint"`
		Source      string `json:"source"`
		TokenAmount struct {
			Amount         string  `json:"amount"`
			Decimals       uint8   `json:"decimals"`
			UIAmount       float64 `json:"uiAmount"`
			UIAmountString string  `json:"uiAmountString"`
		} `json:"tokenAmount"`
	} `json:"info"`
	Type string `json:"type"`
}

func (p *Parser) processMeteoraSwaps(instructionIndex int) []SwapData {
	var swaps []SwapData
	found := false

	// Primary path: harvest TransferChecked/Transfer CPIs (works for DLMM/POOLS/DBC)
	for _, innerInstructionSet := range p.txMeta.InnerInstructions {
		if innerInstructionSet.Index == uint16(instructionIndex) {
			for _, innerInstruction := range innerInstructionSet.Instructions {
				inst := p.convertRPCToSolanaInstruction(innerInstruction)
				switch {
				case p.isTransferCheck(inst):
					if transfer := p.processTransferCheck(inst); transfer != nil {
						swaps = append(swaps, SwapData{Type: METEORA, Data: transfer})
						found = true
					}
				case p.isTransfer(inst):
					if transfer := p.processTransfer(inst); transfer != nil {
						swaps = append(swaps, SwapData{Type: METEORA, Data: transfer})
						found = true
					}
				}
			}
		}
	}
	if found {
		return swaps
	}

	// Fallback: very defensive sweep under the same index (covers edge CPIs)
	if legs := p.collectTokenTransfersUnder(instructionIndex); len(legs) > 0 {
		return legs
	}

	return swaps
}

// Defensive collector used as a last resort under Meteora outer instruction.
func (p *Parser) collectTokenTransfersUnder(index int) []SwapData {
	var legs []SwapData
	for _, innerSet := range p.txMeta.InnerInstructions {
		if innerSet.Index != uint16(index) {
			continue
		}
		for _, rpcInst := range innerSet.Instructions {
			inst := p.convertRPCToSolanaInstruction(rpcInst)
			if p.isTransferCheck(inst) {
				if tr := p.processTransferCheck(inst); tr != nil {
					legs = append(legs, SwapData{Type: METEORA, Data: tr})
				}
			} else if p.isTransfer(inst) {
				if tr := p.processTransfer(inst); tr != nil {
					legs = append(legs, SwapData{Type: METEORA, Data: tr})
				}
			}
		}
	}
	return legs
}

func (p *Parser) processTransferCheck(instr solana.CompiledInstruction) *TransferCheck {
	amount := binary.LittleEndian.Uint64(instr.Data[1:9])

	transferData := &TransferCheck{
		Type: "transferChecked",
	}

	transferData.Info.Source = p.allAccountKeys[instr.Accounts[0]].String()
	transferData.Info.Destination = p.allAccountKeys[instr.Accounts[2]].String()
	transferData.Info.Mint = p.allAccountKeys[instr.Accounts[1]].String()
	transferData.Info.Authority = p.allAccountKeys[instr.Accounts[3]].String()

	transferData.Info.TokenAmount.Amount = fmt.Sprintf("%d", amount)
	transferData.Info.TokenAmount.Decimals = p.splDecimalsMap[transferData.Info.Mint]
	uiAmount := float64(amount) / math.Pow10(int(transferData.Info.TokenAmount.Decimals))
	transferData.Info.TokenAmount.UIAmount = uiAmount
	transferData.Info.TokenAmount.UIAmountString = strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.9f", uiAmount), "0"), ".")

	return transferData
}
