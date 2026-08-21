package editor

import (
	"encoding/hex"
	"strings"
)

// HexDump renders data in aligned, 16-byte xxd-style rows.
func HexDump(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(hex.Dump(data), "\n"), "\n")
}
