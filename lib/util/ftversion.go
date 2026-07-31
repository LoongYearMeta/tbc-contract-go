package util

import (
	"fmt"

	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

// FTVersion identifies the compatible FT contract generation.
type FTVersion uint8

const (
	FTVersion1 FTVersion = 1
	FTVersion2 FTVersion = 2
	FTVersion3 FTVersion = 3
	// FTVersion4 selects the multi-contract swap template. Unlike v3, its
	// covenant selects the authorizing contract input instead of assuming
	// every FT is authorized by current input zero.
	FTVersion4 FTVersion = 4
)

// FTScriptInfo describes the FT generation and whether the script is the
// StableCoin variant.
type FTScriptInfo struct {
	Version FTVersion
	IsCoin  bool
}

// ClassifyFTScriptHex parses and classifies an FT code script.
func ClassifyFTScriptHex(codeHex string) (FTScriptInfo, error) {
	script, err := bscript.NewFromHexString(codeHex)
	if err != nil {
		return FTScriptInfo{}, fmt.Errorf("parse FT code: %w", err)
	}
	return ClassifyFTScript(script)
}

// ClassifyFTScript identifies FT v1-v4 and StableCoin scripts. FT v2 and v3
// are the same byte length, so their fill opcode must be inspected.
func ClassifyFTScript(script *bscript.Script) (FTScriptInfo, error) {
	if script == nil {
		return FTScriptInfo{}, fmt.Errorf("nil FT code script")
	}
	switch len(script.Bytes()) {
	case 1564:
		return FTScriptInfo{Version: FTVersion1}, nil
	case 2012:
		return FTScriptInfo{Version: FTVersion2, IsCoin: true}, nil
	case 1884:
		fill, err := FillCharLengthInFT(script)
		if err != nil {
			return FTScriptInfo{}, err
		}
		if fill == 1 || fill == 2 {
			return FTScriptInfo{Version: FTVersion3}, nil
		}
		return FTScriptInfo{Version: FTVersion2}, nil
	case 1948:
		return FTScriptInfo{Version: FTVersion4}, nil
	default:
		return FTScriptInfo{}, fmt.Errorf("unsupported FT code length %d", len(script.Bytes()))
	}
}

// FillCharLengthInFT mirrors JavaScript fillCharLengthInFT. The fifth script
// chunk from the end encodes the padding length used to distinguish FT v3.
func FillCharLengthInFT(script *bscript.Script) (int, error) {
	if script == nil {
		return 0, fmt.Errorf("nil FT code script")
	}
	chunks := script.Chunks()
	if len(chunks) < 5 {
		return 0, fmt.Errorf("FT code has %d chunks; need at least 5", len(chunks))
	}
	op := int(chunks[len(chunks)-5].OpcodeNum)
	switch op {
	case 95:
		return 1, nil
	case 2:
		return 2, nil
	default:
		return op, nil
	}
}
