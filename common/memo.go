package common

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// ValidateMemoEncoding checks that a memo can be committed to a block as text:
// it must be valid UTF-8 and must not contain U+0000. PostgreSQL cannot store a
// NUL in a text or jsonb column, and JSON encoders replace invalid UTF-8 with
// U+FFFD, so either one stalls or silently alters the indexers and explorers
// that read THORChain's transactions and events. It is the only rule for a memo
// THORChain treats as free text, such as that of a send between two accounts.
func ValidateMemoEncoding(memo string) error {
	return validateMemoRunes(memo, false)
}

// ValidateMemoCharacters checks a memo that THORChain parses and executes: a
// deposit memo, or a memo registered for memoless use. On top of
// ValidateMemoEncoding it refuses every control character: Unicode category Cc
// (U+0000 to U+001F and U+007F to U+009F, so tab, line feed and carriage return
// too), the line and paragraph separators, and the bidirectional controls. The
// memo grammar has no use for any of them. A line break lets one memo pass for
// two in logs and explorers, and a bidirectional control can show the text
// after it in another order than it is executed. Spaces, the free text after
// the "|" data separator and other Unicode characters are unaffected.
func ValidateMemoCharacters(memo string) error {
	return validateMemoRunes(memo, true)
}

func validateMemoRunes(memo string, refuseControl bool) error {
	for i, r := range memo {
		// ranging over a string yields RuneError for each byte that is not part
		// of a valid encoding, which includes overlong forms such as C0 80; an
		// encoded U+FFFD itself is valid and three bytes long
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(memo[i:]); size == 1 {
				return fmt.Errorf("memo is not valid UTF-8 at byte %d", i)
			}
		}
		if r == 0 || (refuseControl && (unicode.IsControl(r) || isLayoutControl(r))) {
			return fmt.Errorf("memo contains control character %U at byte %d", r, i)
		}
	}
	return nil
}

// isLayoutControl reports whether r is the line separator, the paragraph
// separator or a bidirectional control (Unicode property Bidi_Control). They
// are listed here rather than taken from the unicode package's tables, so the
// rule does not change with the Unicode version of the Go toolchain.
// unicode.IsControl needs no such care: it only covers Latin-1.
func isLayoutControl(r rune) bool {
	switch {
	case r == 0x2028, r == 0x2029: // LINE SEPARATOR, PARAGRAPH SEPARATOR
		return true
	case r == 0x061c, r == 0x200e, r == 0x200f: // ALM, LRM, RLM
		return true
	case r >= 0x202a && r <= 0x202e: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	}
	return false
}
