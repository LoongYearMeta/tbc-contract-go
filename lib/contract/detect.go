package contract

import "github.com/LoongYearMeta/tbc-lib-go/bscript"

// DetectGeneration authenticates the complete new-generation compiler template.
// Legacy contracts keep their existing family-specific parsers.
func DetectGeneration(code *bscript.Script) string {
	if code == nil {
		return ""
	}
	if ValidateTBC20StandardCode(code, 0) == nil {
		return "TBC20Standard"
	}
	if c, e := ParseModernTokenCode(code); e == nil {
		switch c.Kind {
		case ModernStablecoin:
			return "TBC20Stablecoin"
		case ModernLP:
			return "TBC20LP"
		default:
			return "TBC20TimelockedLP"
		}
	}
	if _, _, e := ParseTBC721StandardCode(code); e == nil {
		return "TBC721Standard"
	}
	if _, e := ParseTBCAMMCode(code); e == nil {
		return "TBCAMM"
	}
	return ""
}
