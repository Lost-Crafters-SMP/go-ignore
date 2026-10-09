// SPDX-License-Identifier: Apache-2.0

//go:build windows

package ignore

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyWindowsVolumePaths(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{".gitignore": "*.log\n", "src/.gitignore": "!keep.log\n"})
	p := testPolicy(t, root, layeredSpecs())
	want := policyAction(t, p, "src/keep.log", false)
	if want.Action != Include || want.Rule.Source() != "src/.gitignore" {
		t.Fatalf("slash-relative decision %+v", want)
	}
	mixed := filepath.ToSlash(root) + "\\src/keep.log"
	if got, err := p.MatchPath(mixed, false); err != nil || got != want {
		t.Fatalf("mixed separators %+v error=%v want %+v", got, err, want)
	}
	other := "A:"
	if strings.EqualFold(filepath.VolumeName(root), other) {
		other = "B:"
	}
	// These are lexical checks only: no network share or second drive is opened.
	for _, path := range []string{
		filepath.VolumeName(root) + "src\\keep.log", // drive-relative, not absolute
		"\\src\\keep.log",                  // rooted but missing a volume
		other + "\\src\\keep.log",          // another volume
		"\\\\server\\share\\src\\keep.log", // another UNC volume
	} {
		if _, err := p.MatchPath(path, false); err == nil {
			t.Fatalf("accepted outside/non-absolute native path %q", path)
		}
	}
}
