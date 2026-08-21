// Package natsort compares strings with embedded unsigned integers by numeric
// value, so key_2 orders before key_10 where plain byte order would not.
package natsort

import "strings"

// Compare orders a and b like strings.Compare, except that maximal runs of
// ASCII digits compare by numeric value. Runs equal in value but different in
// spelling (leading zeros) fall back to byte order so the order stays total.
func Compare(a, b string) int {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		ca, cb := a[ai], b[bi]
		if isDigit(ca) && isDigit(cb) {
			aEnd, bEnd := digitRunEnd(a, ai), digitRunEnd(b, bi)
			if c := compareDigitRuns(a[ai:aEnd], b[bi:bEnd]); c != 0 {
				return c
			}
			ai, bi = aEnd, bEnd
			continue
		}
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
		ai++
		bi++
	}
	switch {
	case ai < len(a):
		return 1
	case bi < len(b):
		return -1
	}
	return strings.Compare(a, b)
}

func compareDigitRuns(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func digitRunEnd(s string, i int) int {
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return i
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
