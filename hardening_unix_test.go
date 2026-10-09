// SPDX-License-Identifier: Apache-2.0

//go:build unix

package ignore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Platform-specific tests require POSIX filename freedom or Unix permission
// semantics, covering platform differences without weakening portable tests.

// TestPolicyUnixFilenames exercises characters that cannot appear in Windows
// filenames, as rule text and as materialized paths.
func TestPolicyUnixFilenames(t *testing.T) {
	root := t.TempDir()
	rules := "*:colon\n??q\n[<>]|pipe\n\"quoted\"\nstar*name\n"
	writeTree(t, root, map[string]string{
		".custom":            rules,
		"a:colon":            "",
		"zzq":                "",
		"x<>y|pipe":          "",
		"\"quoted\"":         "",
		"star*name":          "",
		"sub/":               "",
		"sub/deep/star*name": "",
	})
	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}})
	for _, c := range []struct {
		path string
		hit  bool
	}{
		{"a:colon", true},
		{"zzq", true},
		{"azq", true}, // ? matches any non-slash byte
		{"<|pipe", true},
		{">|pipe", true},
		{"x<>y|pipe", false},
		{"\"quoted\"", true},
		{"starXname", true},
		{"starname", true}, // * matches zero bytes
		{"starname2", false},
		{"sub/deep/starQname", true},
	} {
		d := policyAction(t, p, c.path, false)
		if (d.Action == Exclude) != c.hit {
			t.Fatalf("%q hit=%v decision=%+v", c.path, c.hit, d.Decision)
		}
	}
}

// TestPolicyUnixCaseDistinct pins case-sensitive containment and matching on
// case-sensitive filesystems: identically spelled names differing in case are
// distinct paths, and folding is pattern semantics, not filesystem semantics.
func TestPolicyUnixCaseDistinct(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".custom": "Readme\n",
		"Readme":  "",
		"readme":  "",
		"sub/":    "",
	})
	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}})
	if d := policyAction(t, p, "Readme", false); d.Action != Exclude {
		t.Fatalf("exact case: %+v", d.Decision)
	}
	if d := policyAction(t, p, "readme", false); d.Action != NoMatch {
		t.Fatalf("other case: %+v", d.Decision)
	}
	folded := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}})
	folded.opts.CaseFold = true
	if d := policyAction(t, folded, "readme", false); d.Action != Exclude {
		t.Fatalf("folded: %+v", d.Decision)
	}
	// Absolute containment stays case-sensitive on Unix.
	if _, err := folded.MatchPath(strings.ToUpper(root), false); err == nil {
		t.Fatal("accepted case-varied root")
	}
	// A differently-cased child name is a distinct, contained path; the
	// folded "Readme" rule matches it because folding is pattern semantics.
	if d, err := folded.MatchPath(filepath.Join(root, "README"), false); err != nil || d.Action != Exclude {
		t.Fatalf("case-variant child: %+v %v", d, err)
	}
}

// TestPolicyUnixUnreadableSource verifies deterministic handling of a source
// that exists but cannot be read: an error now, the same error later, and no
// silent rule adoption after permissions are restored.
func TestPolicyUnixUnreadableSource(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"locked/.custom": "*.log\n"})
	if err := os.Chmod(filepath.Join(root, "locked", ".custom"), 0o000); err != nil {
		t.Skipf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "locked", ".custom"), 0o644) })

	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}})
	first, err := p.Match("locked/x.log", false)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unreadable optional source: %+v %v", first, err)
	}
	_, err2 := p.Match("locked/x.log", false)
	if err2 == nil || err2.Error() != err.Error() {
		t.Fatalf("unstable read error: %v then %v", err, err2)
	}
	if err := os.Chmod(filepath.Join(root, "locked", ".custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Match("locked/x.log", false); err == nil || err.Error() != err2.Error() {
		t.Fatalf("restored permissions changed the snapshot: %v", err)
	}
}
