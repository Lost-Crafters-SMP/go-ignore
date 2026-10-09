// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

// This independent test reference translates byte languages to RE2. It shares no
// parser/compiler/class evaluator with production. Each byte is mapped to a rune
// of the same numeric value so RE2's Unicode input cannot change byte widths.
func byteRunes(s string, fold bool) string {
	var out strings.Builder
	for i := range len(s) {
		c := s[i]
		if fold && c >= 65 && c <= 90 {
			c += 32
		}
		out.WriteRune(rune(c))
	}
	return out.String()
}

func referenceRegex(pattern string, fold bool) (*regexp.Regexp, bool) {
	var out strings.Builder
	out.WriteString("(?s)\\A(?:")
	quote := func(b byte) { out.WriteString(regexp.QuoteMeta(byteRunes(string([]byte{b}), fold))) }
	for at := 0; at < len(pattern); {
		switch pattern[at] {
		case '\\':
			at++
			if at == len(pattern) {
				return nil, false
			}
			quote(pattern[at])
			at++
		case '?':
			out.WriteString("[^/]")
			at++
		case '*':
			start := at
			for at < len(pattern) && pattern[at] == '*' {
				at++
			}
			separator := 0
			if at < len(pattern) && pattern[at] == '/' {
				separator = 1
			}
			if at+1 < len(pattern) && pattern[at:at+2] == "\\/" {
				separator = 2
			}
			if at-start > 1 && (start == 0 || pattern[start-1] == '/') && (at == len(pattern) || separator > 0) {
				if separator == 1 {
					out.WriteString("(?:.*/)?")
					at += separator
				} else {
					out.WriteString(".*")
				}
			} else {
				out.WriteString("[^/]*")
			}
		case '[':
			end, members, ok := referenceClass(pattern, at, fold)
			if !ok {
				return nil, false
			}
			at = end
			out.WriteString("[")
			count := 0
			for n, included := range members {
				if included && n != '/' {
					fmt.Fprintf(&out, "\\x{%x}", n)
					count++
				}
			}
			if count == 0 {
				out.WriteString("\\x{100}")
			}
			out.WriteString("]")
		default:
			quote(pattern[at])
			at++
		}
	}
	out.WriteString(")\\z")
	re, err := regexp.Compile(out.String())
	if err != nil {
		return nil, false
	}
	return re, true
}

func referenceClass(s string, at int, fold bool) (int, [256]bool, bool) {
	var members [256]bool
	put := func(b byte) {
		if fold && b >= 'A' && b <= 'Z' {
			b += 32
		}
		members[b] = true
	}
	at++
	invert := false
	if at < len(s) && (s[at] == '!' || s[at] == '^') {
		invert = true
		at++
	}
	begin := at
	read := func(i int) (byte, int, bool) {
		if i >= len(s) {
			return 0, i, false
		}
		if s[i] == '\\' {
			if i+1 >= len(s) {
				return 0, i, false
			}
			return s[i+1], i + 2, true
		}
		return s[i], i + 1, true
	}
	for at < len(s) {
		if s[at] == ']' && at != begin {
			if invert {
				for i := range members {
					members[i] = !members[i]
				}
			}
			return at + 1, members, true
		}
		if strings.HasPrefix(s[at:], "[:") {
			end := strings.Index(s[at:], ":]")
			if end < 2 {
				return 0, members, false
			}
			name := s[at+2 : at+end]
			if !strings.Contains("|alnum|alpha|blank|cntrl|digit|graph|lower|print|punct|space|upper|xdigit|", "|"+name+"|") {
				return 0, members, false
			}
			re, err := regexp.Compile("^[[:" + name + ":]]$")
			if err != nil {
				return 0, members, false
			}
			for i := 0; i < 128; i++ {
				if re.MatchString(string(rune(i))) {
					put(byte(i))
				}
			}
			at += end + 2
			continue
		}
		c, next, ok := read(at)
		if !ok {
			return 0, members, false
		}
		at = next
		put(c)
		if at+1 < len(s) && s[at] == '-' && s[at+1] != ']' {
			last, next, ok := read(at + 1)
			if !ok {
				return 0, members, false
			}
			for i := int(c); i <= int(last); i++ {
				put(byte(i))
			}
			at = next
		} else {
			put(c)
		}
	}
	return 0, members, false
}

var patternAtoms = []string{"a", "b", "A", "é", "\xff", "*", "**", "?", "/", "\\/", "\\*", "\\?", "\\a", "[a-c]", "[!A-B]", "[]a]", "[a-]", "[\\]]", "[z-a]", "[[:digit:]]", "[[:upper:]]", "[b/c]", "[", "[[:bad:]]", "\\"}

func generatedPattern(rng *rand.Rand) string {
	var b strings.Builder
	for n := rng.IntN(6); n > 0; n-- {
		b.WriteString(patternAtoms[rng.IntN(len(patternAtoms))])
	}
	return b.String()
}

func generatedText(rng *rand.Rand) string {
	bytes := []byte{'a', 'b', 'c', 'A', 'B', '0', '1', '/', ']', '*', '?', 0xc3, 0xa9, 0xff, '\\'}
	b := make([]byte, rng.IntN(10))
	for i := range b {
		b[i] = bytes[rng.IntN(len(bytes))]
	}
	return string(b)
}

func TestMillionReferenceComparisons(t *testing.T) {
	const seed1, seed2 = 0x20261009, 0x4d32
	rng := rand.New(rand.NewPCG(seed1, seed2))
	comparisons := 0
	// Reuse independently translated languages across texts, not expected results.
	for range 10000 {
		pattern := generatedPattern(rng)
		fold := rng.IntN(2) == 1
		re, valid := referenceRegex(pattern, fold)
		program, err := compilePattern(pattern, fold)
		if valid != (err == nil) {
			t.Fatalf("validity pattern=%q fold=%v ref=%v production=%v", pattern, fold, valid, err)
		}
		for range 100 {
			text := generatedText(rng)
			want := valid && re.MatchString(byteRunes(text, fold))
			got := err == nil && runProgram(program, text, false, fold)
			if got != want {
				t.Fatalf("neutral counterexample pattern=%q text=%q fold=%v got=%v want=%v", pattern, text, fold, got, want)
			}
			comparisons++
		}
	}
	t.Logf("deterministic comparisons=%d seeds=%x,%x eligible=%d missing=0 failed=0", comparisons, seed1, seed2, comparisons)
}

func FuzzPatternEquivalence(f *testing.F) {
	for _, p := range patternAtoms {
		f.Add(p, "a/b", false)
	}
	f.Add("a/**/b", "a/b", true)
	f.Fuzz(func(t *testing.T, pattern, text string, fold bool) {
		if len(pattern) > 64 || len(text) > 64 {
			return
		}
		re, valid := referenceRegex(pattern, fold)
		program, err := compilePattern(pattern, fold)
		if valid != (err == nil) {
			t.Fatalf("validity %q %v %v", pattern, valid, err)
		}
		want := valid && re.MatchString(byteRunes(text, fold))
		got := err == nil && runProgram(program, text, false, fold)
		if got != want {
			t.Fatalf("pattern=%q text=%q fold=%v got=%v want=%v", pattern, text, fold, got, want)
		}
	})
}

func FuzzParserOwnership(f *testing.F) {
	f.Add([]byte("\xef\xbb\xbfa/**\r\n!a/keep\n"))
	f.Add([]byte("bad[\n\\ "))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			return
		}
		copyData := append([]byte(nil), data...)
		m, diags, err := Compile([]Source{{Label: "fuzz", Data: data}}, Options{MaxSourceBytes: 2048})
		if string(data) != string(copyData) {
			t.Fatal("input mutation")
		}
		if strings.IndexByte(string(data), 0) >= 0 {
			if err == nil || m != nil {
				t.Fatal("NUL accepted")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"a", "a/b", "a/keep", "é", "x\\y"} {
			before, err := m.Match(path, false)
			if err != nil {
				t.Fatal(err)
			}
			clear(data)
			if len(diags) > 0 {
				diags[0].Pattern = "mutated"
			}
			after, err := m.Match(path, false)
			if err != nil || before.Action != after.Action || before.Rule != after.Rule || before.Ignored() != after.Ignored() {
				t.Fatal("ownership/determinism")
			}
		}
	})
}

func TestMetamorphic(t *testing.T) {
	rng := rand.New(rand.NewPCG(123, 456))
	for range 1000 {
		// For valid positive root rules, prefixing the entire tree and scoping the
		// source preserves endpoint/block decisions and relative provenance.
		text := generatedPattern(rng)
		if strings.ContainsAny(text, "\n\r\x00") {
			continue
		}
		root, _, err := Compile([]Source{{Label: "same", Data: []byte(text)}}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		scoped, _, err := Compile([]Source{{Label: "same", Scope: "prefix", Data: []byte(text)}}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range []bool{false, true} {
			a, _ := root.Match("a/b", dir)
			b, _ := scoped.Match("prefix/a/b", dir)
			if a.Action != b.Action || a.Ignored() != b.Ignored() || verboseRule(a.Rule) != verboseRule(b.Rule) || (a.Block == nil) != (b.Block == nil) {
				t.Fatalf("scope transformation %q", text)
			}
			if a.Block != nil && ("prefix/"+a.Block.Directory != b.Block.Directory || verboseRule(a.Block.Rule) != verboseRule(b.Block.Rule)) {
				t.Fatal("block transform")
			}
		}
	}
}
