// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"path/filepath"
	"slices"
	"testing"
)

// Custom ranks, multiple source filenames and reserved paths are library
// features, not Git compatibility claims. Pin their combined public behaviour
// with explicit expectations rather than another matcher as the expected model.
func TestPolicyDiscoveryDiagnosticsAndPruning(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":            "blocked/\n*.tmp\n!local.log\n",
		".first":                "*.log\nbad[\n",
		".last":                 "!keep.log\n",
		"blocked/.first":        "\x00broken\n",
		"blocked/deep/file":     "",
		"reserved/.first":       "\x00broken\n",
		"reserved/deep/file":    "",
		"src/.first":            "!local.log\nbad[\n",
		"src/.last":             "local.log\n",
		"src/local.log":         "",
		"src/keep.log":          "",
		"src/file.tmp":          "",
		"src/sub/keep.log":      "",
		"src/sub/.gitignore":    "!keep.log\n",
		"src/sub/untracked.log": "",
	})
	specs := []SourceSpec{
		{Filename: ".gitignore", Rank: RankPerDirectory, Optional: true, Nested: true},
		{Filename: ".first", Rank: 350, Optional: true, Nested: true},
		{Filename: ".last", Rank: 350, Optional: true, Nested: true},
	}
	p := testPolicy(t, root, specs, "reserved")
	if got := p.Diagnostics(); len(got) != 0 {
		t.Fatalf("eager diagnostics: %+v", got)
	}
	for range 2 {
		got, err := collectWalk(t, p, "")
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, rec := range got {
			paths = append(paths, rec.rel)
			if rec.rel == "reserved" && !rec.d.Reserved {
				t.Fatal("reserved pruning lost its decision")
			}
		}
		want := []string{".first", ".gitignore", ".last", "blocked", "reserved", "src", "src/.first", "src/.last", "src/file.tmp", "src/keep.log", "src/local.log", "src/sub", "src/sub/.gitignore", "src/sub/keep.log", "src/sub/untracked.log"}
		if !slices.Equal(paths, want) {
			t.Fatalf("walk got %v want %v", paths, want)
		}
		// Invalid sources in both pruned trees must not be read. Repeated
		// walks and descendant discovery must not duplicate diagnostics.
		diags := p.Diagnostics()
		if len(diags) != 2 || diags[0].Source != ".first" || diags[1].Source != "src/.first" {
			t.Fatalf("diagnostics %+v", diags)
		}
		for _, d := range diags {
			if d.Line != 2 || d.Kind != MalformedPattern || d.Pattern != "bad[" {
				t.Fatalf("diagnostic %+v", d)
			}
		}
		// Diagnostics returns detached storage even after lazy discovery.
		diags[0].Source = "changed"
	}
	for _, q := range []struct {
		path, source string
		line         int
		action       Action
	}{
		{"src/local.log", "src/.last", 1, Exclude}, // equal rank/depth: spec order
		{"src/keep.log", ".last", 1, Include},
		{"src/sub/keep.log", ".last", 1, Include}, // rank before depth
		{"src/sub/untracked.log", ".first", 1, Exclude},
		{"src/file.tmp", ".gitignore", 2, Exclude},
	} {
		t.Run(q.path, func(t *testing.T) {
			d := policyAction(t, p, q.path, false)
			if d.Action != q.action || d.Block != nil || d.Rule.Source() != q.source || d.Rule.Line() != q.line {
				t.Fatalf("decision %+v rule %+v", d, d.Rule)
			}
			absolute, err := p.MatchPath(filepath.Join(root, filepath.FromSlash(q.path)), false)
			if err != nil || absolute != d {
				t.Fatalf("absolute %+v relative %+v error %v", absolute, d, err)
			}
		})
	}
	if got, err := collectWalk(t, p, filepath.Join(root, "src")); err != nil || len(got) != 9 {
		t.Fatalf("absolute subtree walk length=%d error=%v", len(got), err)
	}
}

// Match uses canonical slash paths; MatchPath deliberately uses the host's
// lexical cleaning and separators. Discovery and provenance must still agree.
func TestPolicyNativePathNormalization(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":     "*.tmp\n",
		"src/.gitignore": "!/keep.tmp\n",
		"src/keep.tmp":   "",
	})
	p := testPolicy(t, root, layeredSpecs())
	want := policyAction(t, p, "src/keep.tmp", false)
	if want.Action != Include || want.Rule.Source() != "src/.gitignore" {
		t.Fatalf("relative %+v", want)
	}
	sep := string(filepath.Separator)
	for _, path := range []string{
		root + sep + "src" + sep + "." + sep + "keep.tmp",
		root + sep + "src" + sep + "unused" + sep + ".." + sep + "keep.tmp",
		filepath.ToSlash(filepath.Join(root, "src", "keep.tmp")),
	} {
		d, err := p.MatchPath(path, false)
		if err != nil || d != want {
			t.Fatalf("native %q: %+v error=%v want %+v", path, d, err, want)
		}
	}
}
