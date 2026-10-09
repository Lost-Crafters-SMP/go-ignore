// SPDX-License-Identifier: Apache-2.0

//go:build unix

package ignore

import "testing"

// Invalid UTF-8 and literal backslash filenames cannot be represented by the
// JSON corpus or faithfully materialized through Win32. Git's NUL-delimited
// protocol must preserve these bytes, not quote or decode them.
func TestLiveGitUnixBytePaths(t *testing.T) {
	requireLiveGit(t)
	for _, c := range []fixture{
		{ID: "invalid-utf8", Files: map[string]string{".gitignore": "byte?.log\n"}, Query: fixtureQuery{Path: "byte\xff.log"}, Expected: oracleRecord{Ignored: true, Source: ".gitignore", Line: 1, Pattern: "byte?.log"}},
		{ID: "literal-backslash", Files: map[string]string{".gitignore": "back\\\\slash\n"}, Query: fixtureQuery{Path: "back\\slash"}, Expected: oracleRecord{Ignored: true, Source: ".gitignore", Line: 1, Pattern: "back\\\\slash"}},
	} {
		t.Run(c.ID, func(t *testing.T) {
			o := newOracle(t, c, false)
			if got := o.query(t, c.Query.Path); got != c.Expected {
				t.Fatalf("Git %+v fixture %+v", got, c.Expected)
			}
			p := testPolicy(t, o.dir, layeredSpecs())
			checkCorpusDecision(t, policyAction(t, p, c.Query.Path, false).Decision, c)
		})
	}
}
