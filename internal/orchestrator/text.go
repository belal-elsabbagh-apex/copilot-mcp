package orchestrator

import (
	"regexp"
	"strings"
)

// The helpers below reproduce the JavaScript string semantics the TS code relied on, because
// normalizeError's output is a dedupe key (GitHub issue search) that must stay byte-identical
// to what the TS server produced: \s and trim() cover Unicode whitespace (nbsp, U+FEFF, …),
// and slice()/length count UTF-16 code units, not bytes or runes.

// jsSpace is the character class of JS `\s`; Go's `\s` is ASCII-only and lacks \v.
const jsSpace = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var jsSpaceRE = regexp.MustCompile(`[` + jsSpace + `]+`)

func isJSSpace(r rune) bool {
	switch {
	case r == '\t', r == '\n', r == '\v', r == '\f', r == '\r', r == ' ',
		r == 0x00a0, r == 0x1680, r >= 0x2000 && r <= 0x200a,
		r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000, r == 0xfeff:
		return true
	}
	return false
}

// jsTrim is String.prototype.trim.
func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// jsLower is String.prototype.toLowerCase for the one place Go's simple mapping differs from JS's
// full mapping in practice: U+0130 (İ) lowercases to "i" + U+0307 in JS.
// ponytail: final-sigma (Σ → ς at word end) is not reproduced; add if Greek payer text ever shows up.
func jsLower(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "\u0130", "i\u0307"))
}

// utf16Len is String.prototype.length.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}
