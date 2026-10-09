// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestLiveGitExpansion(t *testing.T) {
	if os.Getenv("IGNORE_LIVE_GIT") != "1" {
		t.Skip("set IGNORE_LIVE_GIT=1")
	}
	requireGit(t)
	cases := []struct {
		pattern, path string
		dir           bool
	}{
		{"[z-a]", "z", false}, {"[a-Z]", "b", false}, {"[A-z]", "_", false},
		{"[!a-c]", "d", false}, {"[^a-c]", "b", false}, {"[]a]", "a", false},
		{"[a-]", "-", false}, {"[\\]]", "]", false}, {"[a\\-c]", "b", false},
		{"[[:alpha:]]", "A", false}, {"[[:upper:]]", "a", false}, {"[[:lower:]]", "A", false},
		{"[[:digit:]]", "5", false}, {"[[:alnum:]]", "z", false}, {"[[:xdigit:]]", "f", false},
		{"[[:punct:]]", "_", false}, {"[[:graph:]]", "!", false}, {"[[:print:]]", "x", false},
		{"[[:cntrl:]]", "x", false}, {"[[:space:]]", "x", false}, {"[[:blank:]]", "x", false},
		{"[[:bad:]]\nlive", "live", false}, {"bad[\nlive", "live", false}, {"bad\\\nlive", "live", false},
		{"foo\\/bar", "foo/bar", false}, {"a/**\\/b", "a/b", false}, {"a/**\\/b", "a/x/b", false},
		{"**/b", "b", false}, {"**/b", "x/y/b", false}, {"a/**", "a/b/c", false},
		{"a**/b", "ab", false}, {"a***/b", "ax/b", false}, {"a/***/b", "a/b", false},
		{"d/*", "d", true}, {"d/**", "d", true}, {"d/\n!d/", "d", true},
		{"*\n!*", "d", true}, {"/", "d", true}, {" ", "d", true},
		{"\\#x", "#x", false}, {"\\!x", "!x", false}, {"UPPER", "upper", false},
		{"??", "é", false}, {"?", "é", false}, {"[[:alpha:]]", "é", false},
		{"\\a", "a", false}, {"abc\\ ", "abc ", false}, {"abc   ", "abc", false},
	}
	eligible, ineligible, failed := 0, 0, 0
	for _, fold := range []bool{false, true} {
		for i, c := range cases {
			passed := t.Run(fmt.Sprintf("case-%02d-fold-%v", i, fold), func(t *testing.T) {
				// Keep POSIX cases in the corpus, and account for Windows dtype/name
				// restrictions rather than pretending an unmaterialized query passed.
				if runtime.GOOS == "windows" && strings.HasSuffix(c.path, " ") {
					ineligible++
					t.Skip("Windows cannot faithfully materialize trailing-space name")
				}
				f := fixture{Files: map[string]string{".gitignore": c.pattern + "\n"}, Query: fixtureQuery{Path: c.path, IsDir: c.dir}}
				o := newOracle(t, f, fold)
				eligible++
				query := c.path
				if c.dir {
					query += "/"
				}
				observed := o.query(t, query)
				m, _, err := Compile(fixtureSources(f), Options{CaseFold: fold})
				if err != nil {
					t.Fatal(err)
				}
				d, err := m.Match(c.path, c.dir)
				if err != nil {
					t.Fatal(err)
				}
				if d.Ignored() != observed.Ignored {
					t.Fatalf("neutral counterexample pattern=%q path=%q fold=%v library=%+v oracle=%+v", c.pattern, c.path, fold, d, observed)
				}
				r := d.Rule
				if d.Block != nil {
					r = d.Block.Rule
				}
				checkRecord(t, r, observed)
			})
			if !passed {
				failed++
			}
		}
	}
	t.Logf("expanded unique queries=%d eligible=%d ineligible=%d missing=%d duplicates=0 failed=%d", len(cases)*2, eligible, ineligible, len(cases)*2-eligible-ineligible, failed)
	if eligible+ineligible != len(cases)*2 {
		t.Fatal("missing expanded records")
	}
}

func TestLiveGitGeneratedSmoke(t *testing.T) {
	if os.Getenv("IGNORE_LIVE_GIT") != "1" {
		t.Skip("set IGNORE_LIVE_GIT=1")
	}
	requireGit(t)
	rng := rand.New(rand.NewPCG(0x20261009, 0x676974))
	seen := map[string]bool{}
	attempted, eligible, duplicates, failed := 0, 0, 0, 0
	for i := range 64 {
		depth := rng.IntN(3)
		basename := fmt.Sprintf("q%d%c", i, 'a'+rng.IntN(3))
		path := strings.Repeat("a/", depth) + basename
		pattern := []string{path, "**/" + basename, strings.ReplaceAll(path, "a/", "a/**/"), "[a-c]*", "**/q?*", "[[:lower:]]*", "bad[", "a/**\\/" + basename}[rng.IntN(8)]
		if rng.IntN(3) == 0 {
			pattern = "!" + pattern
		}
		for _, fold := range []bool{false, true} {
			attempted++
			dir := rng.IntN(2) == 0
			key := fmt.Sprintf("%q:%q:%v:%v", pattern, path, dir, fold)
			if seen[key] {
				duplicates++
				continue
			}
			seen[key] = true
			passed := t.Run(fmt.Sprintf("seed-case-%d-fold-%v", i, fold), func(t *testing.T) {
				c := fixture{Files: map[string]string{".gitignore": pattern + "\n"}, Query: fixtureQuery{Path: path, IsDir: dir}}
				o := newOracle(t, c, fold)
				eligible++
				query := path
				if dir {
					query += "/"
				}
				observed := o.query(t, query)
				m, _, err := Compile(fixtureSources(c), Options{CaseFold: fold})
				if err != nil {
					t.Fatal(err)
				}
				d, err := m.Match(path, dir)
				if err != nil {
					t.Fatal(err)
				}
				if d.Ignored() != observed.Ignored {
					t.Fatalf("neutral input pattern=%q path=%q dir=%v fold=%v library=%+v oracle=%+v", pattern, path, dir, fold, d, observed)
				}
				r := d.Rule
				if d.Block != nil {
					r = d.Block.Rule
				}
				checkRecord(t, r, observed)
			})
			if !passed {
				failed++
			}
		}
	}
	t.Logf("live generated smoke seeds=20261009,676974 attempted=%d unique=%d eligible=%d ineligible=0 duplicates=%d missing=%d failed=%d", attempted, len(seen), eligible, duplicates, len(seen)-eligible, failed)
	if attempted != eligible+duplicates {
		t.Fatal("smoke missing records")
	}
}
