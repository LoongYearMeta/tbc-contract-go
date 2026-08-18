package util

import (
	"bytes"
	"fmt"

	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

// FTVersion identifies the compatible legacy FT/StableCoin generation.
type FTVersion uint8

const (
	FTVersion1 FTVersion = 1
	FTVersion2 FTVersion = 2
	FTVersion3 FTVersion = 3
	FTVersion4 FTVersion = 4

	FTV1CodeLength          = 1564
	FTV1PartialOffset       = 1536
	FTV2CodeLength          = 1884
	FTV2PartialOffset       = 1856
	LegacyCoinCodeLength    = 2012
	LegacyCoinPartialOffset = 1984
	FTV4CodeLength          = 2076
	FTV4PartialOffset       = 2048

	LegacyFTV4FillLength = 28
	V4CoinFillLength     = 10
)

var ftCodeMarker = []byte("2Code")

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

// ClassifyFTScript implements the published tbc-contract 1.6.6 rules for FT
// v1-v4, StableCoin v2-v4, and the pre-release 2,012-byte FT v4 template.
func ClassifyFTScript(script *bscript.Script) (FTScriptInfo, error) {
	if script == nil {
		return FTScriptInfo{}, fmt.Errorf("nil FT code script")
	}
	chunks := script.Chunks()
	if len(chunks) == 0 || !bytes.Equal(chunks[len(chunks)-1].Buf, ftCodeMarker) {
		return FTScriptInfo{}, fmt.Errorf("FT code marker 2Code is missing")
	}

	switch len(script.Bytes()) {
	case FTV1CodeLength:
		return FTScriptInfo{Version: FTVersion1}, nil
	case FTV2CodeLength:
		fill, err := FillCharLengthInFT(script)
		if err != nil {
			return FTScriptInfo{}, err
		}
		if fill == 1 || fill == 2 {
			return FTScriptInfo{Version: FTVersion3}, nil
		}
		return FTScriptInfo{Version: FTVersion2}, nil
	case LegacyCoinCodeLength:
		fillChunk, err := ftFillChunk(script)
		if err != nil {
			return FTScriptInfo{}, err
		}
		fill := fillCharLength(fillChunk)
		allFF := isAllFF(fillChunk.Buf)
		switch {
		case allFF && (fill == 2 || fill == 11 || fill == 54):
			version := FTVersion2
			if fill == 2 {
				version = FTVersion3
			}
			return FTScriptInfo{Version: version, IsCoin: true}, nil
		case allFF && fill == LegacyFTV4FillLength:
			return FTScriptInfo{Version: FTVersion4}, nil
		default:
			return FTScriptInfo{}, fmt.Errorf("unsupported 2012-byte FT fill length %d", fill)
		}
	case FTV4CodeLength:
		fillChunk, err := ftFillChunk(script)
		if err != nil {
			return FTScriptInfo{}, err
		}
		return FTScriptInfo{
			Version: FTVersion4,
			IsCoin:  isAllFF(fillChunk.Buf) && len(fillChunk.Buf) == V4CoinFillLength,
		}, nil
	default:
		return FTScriptInfo{}, fmt.Errorf("unsupported FT code length %d", len(script.Bytes()))
	}
}

// IsCoinCodeScript reports whether script is a recognized StableCoin code.
func IsCoinCodeScript(script *bscript.Script) bool {
	info, err := ClassifyFTScript(script)
	return err == nil && info.IsCoin
}

// FTPartialOffsetByLength returns the 64-byte-aligned partial-SHA boundary.
func FTPartialOffsetByLength(codeLength int) (int, bool) {
	switch codeLength {
	case FTV1CodeLength:
		return FTV1PartialOffset, true
	case FTV2CodeLength:
		return FTV2PartialOffset, true
	case LegacyCoinCodeLength:
		return LegacyCoinPartialOffset, true
	case FTV4CodeLength:
		return FTV4PartialOffset, true
	default:
		return 0, false
	}
}

// FTPartialOffset returns the partial-SHA boundary for a supported code script.
func FTPartialOffset(script *bscript.Script) (int, error) {
	if script == nil {
		return 0, fmt.Errorf("nil FT code script")
	}
	offset, ok := FTPartialOffsetByLength(len(script.Bytes()))
	if !ok {
		return 0, fmt.Errorf("unsupported FT code length %d", len(script.Bytes()))
	}
	return offset, nil
}

// FillCharLengthInFT mirrors JavaScript fillCharLengthInFT. The fifth script
// chunk from the end encodes the template padding length.
func FillCharLengthInFT(script *bscript.Script) (int, error) {
	chunk, err := ftFillChunk(script)
	if err != nil {
		return 0, err
	}
	return fillCharLength(chunk), nil
}

func ftFillChunk(script *bscript.Script) (bscript.Chunk, error) {
	if script == nil {
		return bscript.Chunk{}, fmt.Errorf("nil FT code script")
	}
	chunks := script.Chunks()
	if len(chunks) < 5 {
		return bscript.Chunk{}, fmt.Errorf("FT code has %d chunks; need at least 5", len(chunks))
	}
	return chunks[len(chunks)-5], nil
}

func fillCharLength(chunk bscript.Chunk) int {
	if chunk.OpcodeNum == 95 {
		return 1
	}
	if chunk.Buf != nil {
		return len(chunk.Buf)
	}
	return int(chunk.OpcodeNum)
}

func isAllFF(buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	for _, value := range buf {
		if value != 0xff {
			return false
		}
	}
	return true
}
