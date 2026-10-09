// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func mustCompile(t *testing.T, text string, opts Options) *Matcher {
	t.Helper()
	m, _, err := Compile([]Source{{Label: "test", Rank: RankPerDirectory, Data: []byte(text)}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAPI(t *testing.T) {
	for _, path := range []string{"", "/a", "a/", "a//b", ".", "a/..", "a/./b", "a\x00b"} {
		t.Run(fmt.Sprintf("path-%q", path), func(t *testing.T) {
			_, err := new(Matcher).Match(path, false)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatal(err)
			}
		})
	}
	for _, scope := range []string{"/a", "a/", "..", "a//b", "a\x00b"} {
		if m, _, err := Compile([]Source{{Scope: scope}}, Options{}); m != nil || !errors.Is(err, ErrInvalidInput) {
			t.Fatal(scope, err)
		}
	}
	for _, capBytes := range []int64{-1, defaultSourceBytes + 1} {
		if m, _, err := Compile(nil, Options{MaxSourceBytes: capBytes}); m != nil || !errors.Is(err, ErrInvalidInput) {
			t.Fatal(err)
		}
	}
	if m, _, err := Compile([]Source{{Data: []byte("a")}, {Data: []byte{0}}}, Options{}); m != nil || !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	var nilMatcher *Matcher
	if _, err := nilMatcher.Match("a", false); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if d, err := new(Matcher).Match("a\\b", false); err != nil || d.Action != NoMatch {
		t.Fatal(d, err)
	}
}

func TestParsingOwnership(t *testing.T) {
	data := []byte("\xef\xbb\xbf#comment\r\n\r\n #name\r\n\\#name\n\\!name\nfoo\\ \nbar   \nbad[\nlive\ntrailing\\\n[[:unknown:]]\n")
	m, diags, err := Compile([]Source{{Label: "source", Scope: "a", Rank: 13, Data: data}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	clear(data)
	if len(diags) != 3 {
		t.Fatalf("diagnostics %+v", diags)
	}
	diags[0].Message = "mutated"
	if m.Diagnostics()[0].Message == "mutated" {
		t.Fatal("alias")
	}
	for _, c := range []struct {
		path         string
		line         int
		raw, pattern string
	}{{"a/ #name", 3, " #name", " #name"}, {"a/#name", 4, "\\#name", "\\#name"}, {"a/!name", 5, "\\!name", "\\!name"}, {"a/foo ", 6, "foo\\ ", "foo\\ "}, {"a/bar", 7, "bar   ", "bar"}, {"a/live", 9, "live", "live"}} {
		d, err := m.Match(c.path, false)
		if err != nil || d.Action != Exclude || d.Rule.Line() != c.line || d.Rule.Raw() != c.raw || d.Rule.Pattern() != c.pattern || d.Rule.Scope() != "a" || d.Rule.Rank() != 13 {
			t.Fatalf("%+v got %+v %v", c, d, err)
		}
	}
	for _, capBytes := range []int64{1, 2, 3} {
		m, diags, err := Compile([]Source{{Label: "cap", Data: []byte("ab")}, {Label: "other", Data: []byte("z")}}, Options{MaxSourceBytes: capBytes})
		if err != nil {
			t.Fatal(err)
		}
		d, _ := m.Match("ab", false)
		if capBytes == 1 {
			if d.Action != NoMatch || len(diags) != 1 || diags[0].Kind != OversizeSource {
				t.Fatal(d, diags)
			}
		} else if d.Action != Exclude || len(diags) != 0 {
			t.Fatal(d, diags)
		}
	}
}

func TestPatternRegressions(t *testing.T) {
	cases := []struct {
		pattern, path   string
		fold, dir, want bool
	}{
		{"**/x", "x", false, false, true}, {"a/**/b", "a/c/d/b", false, false, true}, {"a/**/b", "a/b", false, false, true},
		{"a/**\\/b", "a/b", false, false, false}, {"foo**/bar", "foobar", false, false, false},
		{"a[b/c]d", "acd", false, false, true}, {"a[b/c]d", "a/d", false, false, false},
		{"[]a]", "a", false, false, true}, {"[]a]", "]", false, false, true}, {"[!a-c]", "d", false, false, true},
		{"[z-a]", "z", false, false, true}, {"[a-]", "-", false, false, true}, {"[\\]]", "]", false, false, true},
		{"\\?", "?", false, false, true}, {"\\*", "*", false, false, true}, {"x\\\\", "x\\", false, false, true},
		{"?", "é", false, false, false}, {"??", "é", false, false, true}, {"[[:alpha:]]", "é", false, false, false},
		{"UPPER", "upper", true, false, true}, {"[A-Z]", "z", true, false, true}, {"[!A-Z]", "z", true, false, false},
		{"[[:upper:]]", "a", true, false, true}, {"[[:lower:]]", "A", true, false, true},
		{"bad[", "bad[", false, false, false}, {"bad\\", "bad\\", false, false, false}, {"a\\/bad[", "a/bad[", false, false, false},
		{"   ", "a", false, true, true}, {"\t", "\t", false, false, true}, {"a/**", "a", false, false, false},
		{"a/*", "a", false, true, true}, {"a/\n!a/", "a", false, true, false},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%q-%q", c.pattern, c.path), func(t *testing.T) {
			m := mustCompile(t, c.pattern, Options{CaseFold: c.fold})
			d, err := m.Match(c.path, c.dir)
			if err != nil || d.Ignored() != c.want {
				t.Fatalf("got %+v err=%v want=%v", d, err, c.want)
			}
		})
	}
}

func TestPolicyFixtures(t *testing.T) {
	type policyRule struct {
		ID                     string
		Rank, Depth, Seq, Line int
		Action                 string
	}
	type candidate struct {
		Path    string
		Matches []string
	}
	var policy struct {
		Schema int
		Origin string
		Rules  []policyRule
		Cases  []struct {
			ID         string
			Candidates []candidate
			Expected   struct{ Action, Rule, Block string }
		}
	}
	readJSON(t, "docs/spec/policy-cases.json", &policy)
	if len(policy.Cases) != 9 {
		t.Fatal("policy accounting")
	}
	for _, c := range policy.Cases {
		t.Run(c.ID, func(t *testing.T) {
			// Materialize exact matching candidate sets as literal patterns, preserving
			// each declared source sequence and physical line. Non-candidates are inert.
			sources := make([]Source, 6)
			for seq := range sources {
				lines := []string{"[", "["}
				var selected *policyRule
				for i := range policy.Rules {
					r := &policy.Rules[i]
					if r.Seq != seq {
						continue
					}
					selected = r
					var paths []string
					for _, at := range c.Candidates {
						for _, id := range at.Matches {
							if id == r.ID {
								paths = append(paths, at.Path)
							}
						}
					}
					if len(paths) > 1 {
						t.Fatal("fixture requires independently representable candidate")
					}
					if len(paths) == 1 {
						text := paths[0]
						if r.Depth == 2 {
							text = strings.TrimPrefix(text, "a/b/")
						}
						text = "/" + text
						if r.Action == "Include" {
							text = "!" + text
						}
						lines[r.Line-1] = text
					}
				}
				if selected != nil {
					scope := ""
					if selected.Depth == 2 {
						scope = "a/b"
					}
					sources[seq] = Source{Label: fmt.Sprint(seq), Scope: scope, Rank: selected.Rank, Data: []byte(strings.Join(lines, "\n"))}
				}
			}
			m, _, err := Compile(sources, Options{})
			if err != nil {
				t.Fatal(err)
			}
			endpoint := c.Candidates[len(c.Candidates)-1].Path
			d, err := m.Match(endpoint, false)
			if err != nil {
				t.Fatal(err)
			}
			wantAction := map[string]Action{"NoMatch": NoMatch, "Include": Include, "Exclude": Exclude}[c.Expected.Action]
			if d.Action != wantAction {
				t.Fatalf("action %+v", d)
			}
			r := d.Rule
			if c.Expected.Block != "" {
				if d.Block == nil || d.Block.Directory != c.Expected.Block || d.Rule != nil {
					t.Fatalf("block %+v", d)
				}
				r = d.Block.Rule
			} else if d.Block != nil {
				t.Fatal("unexpected block")
			}
			if c.Expected.Rule == "" {
				if r != nil {
					t.Fatal("unexpected winner")
				}
				return
			}
			for _, expected := range policy.Rules {
				if expected.ID == c.Expected.Rule {
					if r == nil || r.Source() != fmt.Sprint(expected.Seq) || r.Line() != expected.Line || r.Rank() != expected.Rank {
						t.Fatalf("rule %+v expected %+v", r, expected)
					}
				}
			}
		})
	}
}

func TestConcurrentAndDetached(t *testing.T) {
	m := mustCompile(t, "a/\n!a/\na/sub/\n*.log\n!keep.log\n", Options{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 1000 {
				d, err := m.Match("a/sub/file", false)
				if err != nil || d.Block == nil || d.Block.Directory != "a/sub" {
					t.Error(d, err)
					return
				}
				d.Block.Directory = "mutated"
				d.Block.Rule = nil
				m.Diagnostics()
			}
		})
	}
	wg.Wait()
	a, _ := m.Match("x.log", false)
	b, _ := m.Match("x.log", false)
	if a.Rule != b.Rule {
		t.Fatal("unstable identity")
	}
}

func TestDiagnosticsOrderingAndScopeFold(t *testing.T) {
	m, diags, err := Compile([]Source{{Label: "z", Data: []byte("[\n")}, {Label: "a", Data: []byte("\\\n[")}, {Label: "a", Data: []byte("[[:bad:]]")}}, Options{})
	if err != nil || len(diags) != 4 {
		t.Fatal(diags, err)
	}
	if diags[0].Source != "a" || diags[0].Line != 1 || diags[0].Pattern != "\\" || diags[1].Pattern != "[[:bad:]]" || diags[2].Line != 2 || diags[3].Source != "z" {
		t.Fatalf("diagnostic order %+v", diags)
	}
	got := m.Diagnostics()
	got[0].Source = "changed"
	if m.Diagnostics()[0].Source != "a" {
		t.Fatal("diagnostic alias")
	}
	m, _, err = Compile([]Source{{Label: "folded", Scope: "TREE", Data: []byte("/FILE")}}, Options{CaseFold: true})
	if err != nil {
		t.Fatal(err)
	}
	d, err := m.Match("tree/file", false)
	if err != nil || d.Action != Exclude || d.Rule.Scope() != "TREE" {
		t.Fatal(d, err)
	}
	for _, path := range []string{"treehouse/file", "sibling/file", "TREE/sub/FILE"} {
		d, err := m.Match(path, false)
		if err != nil || d.Action != NoMatch {
			t.Fatal(path, d, err)
		}
	}
}

func ExampleCompile() {
	m, _, err := Compile([]Source{{Label: ".gitignore", Rank: RankPerDirectory, Data: []byte("*.log\n!keep.log\n")}}, Options{})
	if err != nil {
		fmt.Println(err)
		return
	}
	d, err := m.Match("keep.log", false)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(d.Ignored(), d.Rule.Source(), d.Rule.Line())
	// Output: false .gitignore 2
}
