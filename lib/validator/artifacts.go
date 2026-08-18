package validator

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

type byteRange struct{ start, end int }

type ftArtifact struct {
	id                string
	version           uint8
	coin              bool
	codeBytes         int
	partialOffset     int
	normalizedSHA256  string
	identityRanges    []byteRange
	originalUTXORange *byteRange
	tapeSizeOffsets   []int
}

type decodedFTArtifact struct {
	artifact         *ftArtifact
	originalUTXOWire []byte
	tapeSize         int
}

func ordinaryArtifact(id string, version uint8, codeBytes, partialOffset int, hash string, original byteRange, tapeOffsets []int) ftArtifact {
	rangeCopy := original
	return ftArtifact{id: id, version: version, codeBytes: codeBytes, partialOffset: partialOffset, normalizedSHA256: hash, identityRanges: []byteRange{original}, originalUTXORange: &rangeCopy, tapeSizeOffsets: tapeOffsets}
}

func coinArtifact(id string, version uint8, hash string, identity []byteRange, tapeOffsets []int) ftArtifact {
	return ftArtifact{id: id, version: version, coin: true, codeBytes: 2012, partialOffset: 1984, normalizedSHA256: hash, identityRanges: identity, tapeSizeOffsets: tapeOffsets}
}

var publishedFTArtifacts = []ftArtifact{
	ordinaryArtifact("ft-v1-npm-1.0.0-1.1.28", 1, 1564, 1536, "bfd8126a4cc55725a62b3ae0a7235f6cabe1b15c70f27fe3e7d7fada9bc37c73", byteRange{303, 339}, []int{566, 636, 678, 750, 794, 866, 910, 982, 1026, 1098, 1142, 1214, 1258, 1330, 1374, 1446}),
	ordinaryArtifact("ft-v1-npm-1.1.29-1.4.12", 1, 1564, 1536, "aa4471260dc8dbc3e9ba5a7e98653b87e617634b5c37c6025374b73e55310735", byteRange{313, 349}, []int{576, 646, 688, 760, 804, 876, 920, 992, 1036, 1108, 1152, 1224, 1268, 1340, 1384, 1456}),
	ordinaryArtifact("ft-v2-npm-1.5.0-1.6.0", 2, 1884, 1856, "f4b034ef624fc3de2dd461996712d4389c31e39fc4f74524e663465f1c7bd4cf", byteRange{619, 655}, []int{882, 952, 994, 1066, 1110, 1182, 1226, 1298, 1342, 1414, 1458, 1530, 1574, 1646, 1690, 1762}),
	ordinaryArtifact("ft-v3-npm-1.6.1-1.6.5", 3, 1884, 1856, "fdfa23a85f44ca26cff96e5053fa33e5b47af7dd3bc31ffbc577ab191952fb82", byteRange{647, 683}, []int{919, 989, 1031, 1103, 1147, 1219, 1263, 1335, 1379, 1451, 1495, 1567, 1611, 1683, 1727, 1799}),
	ordinaryArtifact("ft-v4-source-pre-release", 4, 2012, 1984, "2c80025bcbe3e42a3fe171547d0f1a576d7bc53a1d8c8fb2ff5c996e56af9172", byteRange{672, 708}, []int{1019, 1089, 1131, 1203, 1247, 1319, 1363, 1435, 1479, 1551, 1595, 1667, 1711, 1783, 1827, 1899}),
	coinArtifact("stablecoin-v2-npm-1.5.1-1.5.2", 2, "e08e43f220636197380d8763df8c31c8b4bc76c326f66b9385d4e973d16200b8", []byteRange{{258, 278}, {705, 737}}, []int{1041, 1111, 1153, 1225, 1269, 1341, 1385, 1457, 1501, 1573, 1617, 1689, 1733, 1805, 1849, 1921}),
	coinArtifact("stablecoin-v2-npm-1.5.3-1.6.0", 2, "77b60cc9e884901425987112ff3a9547a0dfd155db641aea0f752ad29b6a30a9", []byteRange{{256, 276}, {669, 701}}, []int{998, 1068, 1110, 1182, 1226, 1298, 1342, 1414, 1458, 1530, 1574, 1646, 1690, 1762, 1806, 1878}),
	coinArtifact("stablecoin-v3-npm-1.6.1-1.6.5", 3, "0f226cbd6cf89f89d2c22eca43f8295e4bcafb750e11141bc1cee5aebbb42b3b", []byteRange{{278, 298}, {697, 729}}, []int{1050, 1120, 1162, 1234, 1278, 1350, 1394, 1466, 1510, 1582, 1626, 1698, 1742, 1814, 1858, 1930}),
	ordinaryArtifact("ft-v4-current-sdk", 4, 2076, 2048, "d0a57bdf42b2f919c14febc71aac79ded1f25099cdee8373242261e83150fa74", byteRange{696, 732}, []int{1043, 1117, 1159, 1235, 1279, 1355, 1399, 1475, 1519, 1595, 1639, 1715, 1759, 1835, 1879, 1955}),
	func() ftArtifact {
		artifact := coinArtifact("stablecoin-v4-current-sdk", 4, "7d283417e489492719705e2b495775e53f6667751736abe90e2a5d5af14db5d9", []byteRange{{302, 322}, {721, 753}}, []int{1074, 1148, 1190, 1266, 1310, 1386, 1430, 1506, 1550, 1626, 1670, 1746, 1790, 1866, 1910, 1986})
		artifact.codeBytes, artifact.partialOffset = 2076, 2048
		return artifact
	}(),
}

func decodePublishedFTCode(code []byte) *decodedFTArtifact {
	var matched *decodedFTArtifact
	for index := range publishedFTArtifacts {
		artifact := &publishedFTArtifacts[index]
		if len(code) != artifact.codeBytes || artifact.partialOffset+28 != len(code) || code[artifact.partialOffset] != 0x15 || (code[artifact.partialOffset+21] != 0 && code[artifact.partialOffset+21] != 1) || !bytes.Equal(code[artifact.partialOffset+22:], []byte{5, '2', 'C', 'o', 'd', 'e'}) {
			continue
		}
		if len(artifact.tapeSizeOffsets) == 0 {
			continue
		}
		tapeSize := int(code[artifact.tapeSizeOffsets[0]])
		consistent := true
		for _, offset := range artifact.tapeSizeOffsets {
			if offset >= len(code) || int(code[offset]) != tapeSize {
				consistent = false
				break
			}
		}
		if !consistent {
			continue
		}
		normalized := append([]byte(nil), code...)
		ranges := append([]byteRange(nil), artifact.identityRanges...)
		for _, offset := range artifact.tapeSizeOffsets {
			ranges = append(ranges, byteRange{offset, offset + 1})
		}
		ranges = append(ranges, byteRange{artifact.partialOffset + 1, artifact.partialOffset + 22})
		validRanges := true
		for _, span := range ranges {
			if span.start < 0 || span.end < span.start || span.end > len(normalized) {
				validRanges = false
				break
			}
			for cursor := span.start; cursor < span.end; cursor++ {
				normalized[cursor] = 0
			}
		}
		if !validRanges {
			continue
		}
		hash := sha256.Sum256(normalized)
		if hex.EncodeToString(hash[:]) != artifact.normalizedSHA256 {
			continue
		}
		if matched != nil {
			return nil
		}
		decoded := &decodedFTArtifact{artifact: artifact, tapeSize: tapeSize}
		if artifact.originalUTXORange != nil {
			decoded.originalUTXOWire = append([]byte(nil), code[artifact.originalUTXORange.start:artifact.originalUTXORange.end]...)
		}
		matched = decoded
	}
	return matched
}

func readCanonicalPush(script []byte, start int) (dataStart, length int, ok bool) {
	if start >= len(script) {
		return 0, 0, false
	}
	opcode := script[start]
	switch {
	case opcode >= 1 && opcode <= 0x4b:
		dataStart, length = start+1, int(opcode)
	case opcode == 0x4c && start+1 < len(script):
		dataStart, length = start+2, int(script[start+1])
		if length < 0x4c {
			return 0, 0, false
		}
	case opcode == 0x4d && start+2 < len(script):
		dataStart, length = start+3, int(script[start+1])|int(script[start+2])<<8
		if length <= 0xff {
			return 0, 0, false
		}
	default:
		return 0, 0, false
	}
	return dataStart, length, dataStart+length <= len(script)
}

func isPublishedFTTape(tape []byte) bool {
	if len(tape) < 59 || !bytes.Equal(tape[:3], []byte{0, 0x6a, 0x30}) || !bytes.Equal(tape[len(tape)-6:], []byte{5, 'F', 'T', 'a', 'p', 'e'}) {
		return false
	}
	cursor, pushes := 51, 0
	firstLength, lastStart, lastLength := -1, -1, -1
	for cursor < len(tape) {
		dataStart, length, ok := readCanonicalPush(tape, cursor)
		if !ok {
			return false
		}
		if pushes == 0 {
			firstLength = length
		}
		lastStart, lastLength = cursor, length
		cursor = dataStart + length
		pushes++
	}
	return cursor == len(tape) && pushes >= 2 && firstLength == 1 && lastStart == len(tape)-6 && lastLength == 5
}
