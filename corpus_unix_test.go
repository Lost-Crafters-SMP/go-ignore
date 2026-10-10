// SPDX-License-Identifier: Apache-2.0

//go:build unix

package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

// Invalid UTF-8 and literal backslash filenames cannot be represented by the
// JSON corpus or faithfully materialized through Win32. Git's NUL-delimited
// protocol must preserve these bytes, not quote or decode them. Filesystems
// that require valid UTF-8 names reject the invalid-UTF-8 case with EILSEQ,
// so that case skips there rather than failing the oracle run.
func TestLiveGitUnixBytePaths(t *testing.T) {
	requireLiveGit(t)
	for _, c := range []fixture{
		{ID: "invalid-utf8", Files: map[string]string{".gitignore": "byte?.log\n"}, Query: fixtureQuery{Path: "byte\xff.log"}, Expected: oracleRecord{Ignored: true, Source: ".gitignore", Line: 1, Pattern: "byte?.log"}},
		{ID: "literal-backslash", Files: map[string]string{".gitignore": "back\\\\slash\n"}, Query: fixtureQuery{Path: "back\\slash"}, Expected: oracleRecord{Ignored: true, Source: ".gitignore", Line: 1, Pattern: "back\\\\slash"}},
	} {
		t.Run(c.ID, func(t *testing.T) {
			requireMaterializablePath(t, c.Query.Path)
			o := newOracle(t, c, false)
			if got := o.query(t, c.Query.Path); got != c.Expected {
				t.Fatalf("Git %+v fixture %+v", got, c.Expected)
			}
			p := testPolicy(t, o.dir, layeredSpecs())
			checkCorpusDecision(t, policyAction(t, p, c.Query.Path, false).Decision, c)
		})
	}
}

// requireMaterializablePath probes whether the filesystem can hold the exact
// bytes of path. macOS volumes reject names that are not valid UTF-8 even
// though Git's protocol and the matcher both handle such bytes, so the live
// oracle cannot be hosted there; probing keeps the case running on any
// filesystem that does accept the bytes instead of assuming every Darwin
// volume behaves identically.
func requireMaterializablePath(t *testing.T, path string) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), filepath.FromSlash(path))
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Skipf("filesystem cannot faithfully materialize %q: %v", path, err)
	}
}
