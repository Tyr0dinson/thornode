package common

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestValidateMemo(t *testing.T) {
	long := strings.Repeat("=:ETH.ETH:0x1111111111111111111111111111111111111111::t:1|", 4)

	cases := []struct {
		name       string
		memo       string
		encodingOK bool
		charsOK    bool
		errAt      string // byte offset expected in the error, when not OK
	}{
		{"empty", "", true, true, ""},
		{"spaces", "   ", true, true, ""},
		{"swap", "=:ETH.ETH:0x1111111111111111111111111111111111111111:1e6/1/0:t:10", true, true, ""},
		{"reference", "REFERENCE:BTC.BTC:=:ETH.ETH:0x1111111111111111111111111111111111111111", true, true, ""},
		{"data tail", "=:ETH.ETH:0x1111111111111111111111111111111111111111|hello world", true, true, ""},
		{"unicode text", "\u00E9t\u00E9 \u2192 \U0001F680 \u4E2D\u6587", true, true, ""},
		{"encoded U+FFFD", "a\uFFFDb", true, true, ""},
		{"format characters", "a\u200Bb\u200Cc\u200Dd\uFEFF\u2060", true, true, ""},
		{"next to the layout controls", "\u061B\u061D\u2027\u202F\u2065\u206A", true, true, ""},
		{"long", long, true, true, ""},

		{"NUL alone", "\x00", false, false, "byte 0"},
		{"NUL in address", "=:BTC.BTC:bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4\x00::t:1", false, false, "byte 52"},
		{"NUL in data tail", "=:ETH.ETH:0x1111111111111111111111111111111111111111|\x00", false, false, "byte 53"},
		{"NUL after unicode", "\u00E9\x00", false, false, "byte 2"},
		{"invalid byte", "a\xffb", false, false, "byte 1"},
		{"truncated sequence", "a\xe2\x82", false, false, "byte 1"},
		{"overlong NUL", "a\xc0\x80", false, false, "byte 1"},
		{"surrogate half", "a\xed\xa0\x80", false, false, "byte 1"},

		{"line feed", "=:ETH.ETH:0x1111111111111111111111111111111111111111\nOUT:1", true, false, "byte 52"},
		{"carriage return line feed", "a\r\nb", true, false, "byte 1"},
		{"tab", "a\tb", true, false, "byte 1"},
		{"line feed only", "\n", true, false, "byte 0"},
		{"escape", "a\x1b[2Jb", true, false, "byte 1"},
		{"delete", "a\x7f", true, false, "byte 1"},
		{"next line (C1)", "a\u0085", true, false, "byte 1"},
		{"control sequence introducer (C1)", "a\u009b", true, false, "byte 1"},
		{"line separator", "=:ETH.ETH:0x1111111111111111111111111111111111111111\u2028OUT:1", true, false, "byte 52"},
		{"paragraph separator", "a\u2029b", true, false, "byte 1"},
		{"right-to-left override", "a\u202Eb", true, false, "byte 1"},
		{"left-to-right embedding", "a\u202Ab", true, false, "byte 1"},
		{"right-to-left isolate", "a\u2067b", true, false, "byte 1"},
		{"pop directional isolate", "a\u2069b", true, false, "byte 1"},
		{"right-to-left mark", "a\u200Fb", true, false, "byte 1"},
		{"arabic letter mark", "\u00E9\u061C", true, false, "byte 2"},
	}

	for _, tc := range cases {
		check := func(rule string, err error, wantOK bool) {
			switch {
			case wantOK && err != nil:
				t.Errorf("%s: %s(%q) = %v, want nil", tc.name, rule, tc.memo, err)
			case !wantOK && err == nil:
				t.Errorf("%s: %s(%q) = nil, want an error", tc.name, rule, tc.memo)
			case !wantOK && !strings.Contains(err.Error(), tc.errAt):
				t.Errorf("%s: %s(%q) = %q, want it to name %s", tc.name, rule, tc.memo, err, tc.errAt)
			}
		}
		check("ValidateMemoEncoding", ValidateMemoEncoding(tc.memo), tc.encodingOK)
		check("ValidateMemoCharacters", ValidateMemoCharacters(tc.memo), tc.charsOK)
	}
}

// Every control character is refused by ValidateMemoCharacters, and of those
// only NUL by ValidateMemoEncoding. Every other ASCII character is allowed.
func TestValidateMemoASCII(t *testing.T) {
	for b := 0; b < 0x80; b++ {
		memo := "a" + string(rune(b)) + "b"
		control := b < 0x20 || b == 0x7f
		if err := ValidateMemoCharacters(memo); (err != nil) != control {
			t.Errorf("ValidateMemoCharacters(%q) = %v, control = %v", memo, err, control)
		}
		if err := ValidateMemoEncoding(memo); (err != nil) != (b == 0) {
			t.Errorf("ValidateMemoEncoding(%q) = %v", memo, err)
		}
	}
	for r := rune(0x80); r < 0xa0; r++ {
		if ValidateMemoCharacters("a"+string(r)) == nil {
			t.Errorf("ValidateMemoCharacters accepts C1 control %U", r)
		}
		if err := ValidateMemoEncoding("a" + string(r)); err != nil {
			t.Errorf("ValidateMemoEncoding(%U) = %v", r, err)
		}
	}
}

// isControlOracle is the character rule written with the unicode package's own
// tables, as an oracle for the explicit list in isLayoutControl.
func isControlOracle(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp, unicode.Bidi_Control)
}

// Every code point, checked alone and after a letter, agrees with the oracle.
// This fails if a Go release adds a character to Bidi_Control, Zl or Zp, which
// is the cue to decide whether isLayoutControl, a consensus rule, follows.
func TestValidateMemoEveryRune(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue // surrogates cannot be encoded in UTF-8
		}
		for _, memo := range []string{string(r), "a" + string(r)} {
			if err := ValidateMemoCharacters(memo); (err == nil) == isControlOracle(r) {
				t.Errorf("ValidateMemoCharacters(%q) = %v, oracle says control = %v", memo, err, isControlOracle(r))
			}
			if err := ValidateMemoEncoding(memo); (err == nil) != (r != 0) {
				t.Errorf("ValidateMemoEncoding(%q) = %v", memo, err)
			}
		}
	}
}

func FuzzValidateMemo(f *testing.F) {
	for _, seed := range []string{"", "=:ETH.ETH:0x1111111111111111111111111111111111111111", "a\x00", "a\n", "\xff", "\u00e9", "\xc0\x80", "a\u2028", "a\u202e"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, memo string) {
		wantEncoding := utf8.ValidString(memo) && !strings.ContainsRune(memo, 0)
		wantChars := wantEncoding && strings.IndexFunc(memo, isControlOracle) < 0
		if err := ValidateMemoEncoding(memo); (err == nil) != wantEncoding {
			t.Fatalf("ValidateMemoEncoding(%q) = %v, want ok = %v", memo, err, wantEncoding)
		}
		if err := ValidateMemoCharacters(memo); (err == nil) != wantChars {
			t.Fatalf("ValidateMemoCharacters(%q) = %v, want ok = %v", memo, err, wantChars)
		}
	})
}
