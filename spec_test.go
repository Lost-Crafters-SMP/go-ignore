// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fixtureQuery struct {
	Path           string
	IsDir, NoSlash bool
}
type oracleRecord struct {
	Ignored bool
	Source  string
	Line    int
	Pattern string
}

func (r *oracleRecord) UnmarshalJSON(data []byte) error {
	var wire struct {
		Ignored *bool
		Source  *string
		Line    *int
		Pattern *string
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&wire); err != nil {
		return fmt.Errorf("expected record: %w", err)
	}
	if wire.Ignored == nil || wire.Source == nil || wire.Line == nil || wire.Pattern == nil {
		return fmt.Errorf("missing expected record fields")
	}
	*r = oracleRecord{Ignored: *wire.Ignored, Source: *wire.Source, Line: *wire.Line, Pattern: *wire.Pattern}
	return nil
}

type fixtureBlock struct {
	Directory, Source string
	Line              int
	Pattern           string
}
type fixture struct {
	ID       string            `json:"id"`
	Files    map[string]string `json:"files"`
	Query    fixtureQuery      `json:"query"`
	Expected oracleRecord      `json:"expected"`
	Block    *fixtureBlock     `json:"block"`
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		t.Fatal(err)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("extra JSON: %v", err)
	}
}

func fixtures(t *testing.T) []fixture {
	t.Helper()
	var result []fixture
	seen := map[string]bool{}
	for _, name := range []string{"corpus.json", "directory-cases.json"} {
		var data struct {
			Schema int
			Origin string
			Cases  []fixture
		}
		readJSON(t, filepath.Join("docs", "spec", name), &data)
		if data.Schema != 1 {
			t.Fatal("unknown schema")
		}
		for _, c := range data.Cases {
			if c.ID == "" || c.Query.Path == "" || c.Files == nil {
				t.Fatal("missing fixture identity/input")
			}
			if seen[c.ID] {
				t.Fatalf("duplicate %s", c.ID)
			}
			seen[c.ID] = true
			result = append(result, c)
		}
	}
	if len(result) != 56 {
		t.Fatalf("missing cases: %d", len(result))
	}
	return result
}

func fixtureSources(c fixture) []Source {
	keys := make([]string, 0, len(c.Files))
	for k := range c.Files {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var sources []Source
	for _, k := range keys {
		scope := ""
		rank := RankPerDirectory
		if k == ".git/info/exclude" {
			rank = RankInfoExclude
		} else if strings.HasSuffix(k, "/.gitignore") {
			scope = strings.TrimSuffix(k, "/.gitignore")
		}
		sources = append(sources, Source{Label: k, Scope: scope, Rank: rank, Data: []byte(c.Files[k])})
	}
	return sources
}

func verboseRule(r *Rule) string {
	if r == nil {
		return ""
	}
	p := r.Pattern()
	if r.Negated() {
		p = "!" + p
	}
	if r.DirOnly() {
		p += "/"
	}
	return p
}

func checkRecord(t *testing.T, r *Rule, expected oracleRecord) {
	t.Helper()
	if r == nil {
		if expected.Source != "" || expected.Line != 0 || expected.Pattern != "" {
			t.Fatalf("missing rule: %+v", expected)
		}
		return
	}
	if r.Source() != expected.Source || r.Line() != expected.Line || verboseRule(r) != expected.Pattern {
		t.Fatalf("provenance got %s:%d:%q want %+v", r.Source(), r.Line(), verboseRule(r), expected)
	}
}

func TestSpecManifest(t *testing.T) {
	var manifest struct {
		Version                    int
		Files                      []struct{ Path, SHA256 string }
		CaseCount, PolicyCaseCount int
	}
	readJSON(t, "docs/spec/manifest.json", &manifest)
	if manifest.Version != 1 || len(manifest.Files) != 4 || manifest.CaseCount != 56 || manifest.PolicyCaseCount != 9 {
		t.Fatal("manifest accounting")
	}
	for _, f := range manifest.Files {
		data, err := os.ReadFile(filepath.Join("docs", "spec", f.Path))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != f.SHA256 {
			t.Fatalf("hash %s", f.Path)
		}
	}
}

func TestSpecFixtures(t *testing.T) {
	for _, c := range fixtures(t) {
		t.Run(c.ID, func(t *testing.T) {
			m, _, err := Compile(fixtureSources(c), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if c.Query.NoSlash {
				r := m.winner(c.Query.Path, true, false, nil)
				checkRecord(t, r, c.Expected)
				return
			}
			for range 3 {
				d, err := m.Match(c.Query.Path, c.Query.IsDir)
				if err != nil {
					t.Fatal(err)
				}
				if d.Ignored() != c.Expected.Ignored {
					t.Fatalf("decision %+v expected %+v", d, c.Expected)
				}
				if c.Block != nil {
					if d.Action != NoMatch || d.Rule != nil || d.Block == nil || d.Block.Directory != c.Block.Directory {
						t.Fatalf("block %+v", d)
					}
					checkRecord(t, d.Block.Rule, c.Expected)
				} else {
					if d.Block != nil {
						t.Fatalf("unexpected block %+v", d.Block)
					}
					wantAction := NoMatch
					if c.Expected.Line > 0 {
						wantAction = Exclude
						if strings.HasPrefix(c.Expected.Pattern, "!") {
							wantAction = Include
						}
					}
					if d.Action != wantAction {
						t.Fatalf("action got %v want %v", d.Action, wantAction)
					}
					checkRecord(t, d.Rule, c.Expected)
				}
			}
		})
	}
}

// oracle executes only the external binary in freshly created scratch repositories.
// No Git source or historic runner is used. Every command has a killable deadline.
type oracle struct {
	dir  string
	env  []string
	fold bool
}

func newOracle(t *testing.T, c fixture, fold bool) *oracle {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "repo")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(root, "empty")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{}
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "GIT_") || upper == "HOME" || upper == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+empty, "HOME="+home, "XDG_CONFIG_HOME="+home, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	o := &oracle{dir: dir, env: env, fold: fold}
	o.command(t, "", false, "init", "--quiet", "--template=")
	o.command(t, "", false, "config", "core.ignoreCase", strconv.FormatBool(fold))
	o.command(t, "", false, "config", "core.excludesFile", empty)
	for name, data := range c.Files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, filepath.FromSlash(c.Query.Path))
	if c.Query.IsDir {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() != c.Query.IsDir {
		t.Fatalf("ineligible materialization %q: %v", p, err)
	}
	return o
}

func (o *oracle) command(t *testing.T, input string, allowOne bool, args ...string) ([]byte, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = o.dir
	cmd.Env = o.env
	cmd.Stdin = strings.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
		if !allowOne || code != 1 {
			t.Fatalf("git %v: %v stderr=%s", args, err, stderr.String())
		}
	}
	if ctx.Err() != nil || stderr.Len() != 0 {
		t.Fatalf("oracle failure %v %s", ctx.Err(), stderr.String())
	}
	return data, code
}

func (o *oracle) query(t *testing.T, path string) oracleRecord {
	t.Helper()
	verdict, code := o.command(t, path+"\x00", true, "check-ignore", "-z", "--stdin")
	wire, _ := o.command(t, path+"\x00", true, "check-ignore", "-v", "-z", "--stdin", "--non-matching")
	r, err := decodeOracleRecord(path, verdict, code, wire)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func decodeOracleRecord(path string, verdict []byte, code int, wire []byte) (oracleRecord, error) {
	if code != 0 && code != 1 {
		return oracleRecord{}, fmt.Errorf("invalid verdict status %d", code)
	}
	if code == 0 {
		if string(verdict) != path+"\x00" {
			return oracleRecord{}, fmt.Errorf("unexpected/duplicate verdict %q", verdict)
		}
	} else if len(verdict) != 0 {
		return oracleRecord{}, fmt.Errorf("negative verdict %q", verdict)
	}
	fields := bytes.Split(wire, []byte{0})
	if len(fields) != 5 || len(fields[4]) != 0 || string(fields[3]) != path {
		return oracleRecord{}, fmt.Errorf("missing/duplicate/truncated oracle record %q", wire)
	}
	r := oracleRecord{Ignored: code == 0, Source: string(fields[0]), Pattern: string(fields[2])}
	if len(fields[1]) != 0 {
		n, err := strconv.Atoi(string(fields[1]))
		if err != nil || n <= 0 {
			return oracleRecord{}, fmt.Errorf("bad line %q", fields[1])
		}
		r.Line = n
	}
	if (r.Source == "") != (r.Line == 0) || r.Source == "" && (r.Pattern != "" || r.Ignored) || r.Source != "" && r.Ignored == strings.HasPrefix(r.Pattern, "!") {
		return oracleRecord{}, fmt.Errorf("inconsistent record %+v", r)
	}
	return r, nil
}

func TestOracleWireFailClosed(t *testing.T) {
	for _, c := range []struct {
		wire, verdict string
		code          int
		valid         bool
	}{
		{".gitignore\x001\x00\x00p\x00", "p\x00", 0, true},
		{".gitignore\x002\x00!p\x00p\x00", "", 1, true},
		{"\x00\x00\x00p\x00", "", 1, true},
		{"", "", 1, false}, {"\x00\x00\x00p", "", 1, false},
		{"\x00\x00\x00p\x00\x00\x00\x00p\x00", "", 1, false},
		{".gitignore\x00oops\x00p\x00p\x00", "p\x00", 0, false},
		{".gitignore\x001\x00p\x00other\x00", "p\x00", 0, false},
		{".gitignore\x001\x00!p\x00p\x00", "p\x00", 0, false},
		{"\x00\x00\x00p\x00", "p\x00", 0, false},
		{".gitignore\x001\x00p\x00p\x00", "p\x00p\x00", 0, false},
		{"\x00\x00\x00p\x00", "", 2, false},
	} {
		_, err := decodeOracleRecord("p", []byte(c.verdict), c.code, []byte(c.wire))
		if (err == nil) != c.valid {
			t.Fatalf("wire=%q err=%v", c.wire, err)
		}
	}
	for _, raw := range []string{`{"Ignored":false}`, `{"Ignored":false,"Source":"","Line":0,"Pattern":"","Unexpected":1}`} {
		var r oracleRecord
		if err := json.Unmarshal([]byte(raw), &r); err == nil {
			t.Fatal("accepted incomplete/unknown expected record")
		}
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	version, err := exec.Command("git", "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	var major, minor int
	if _, err := fmt.Sscanf(string(version), "git version %d.%d", &major, &minor); err != nil || major < 2 || major == 2 && minor < 52 {
		t.Fatalf("Git >=2.52 required: %s", version)
	}
	t.Logf("%s platform=%s/%s Go=%s", strings.TrimSpace(string(version)), runtime.GOOS, runtime.GOARCH, runtime.Version())
}

func TestLiveGitSpec(t *testing.T) {
	if os.Getenv("IGNORE_LIVE_GIT") != "1" {
		t.Skip("set IGNORE_LIVE_GIT=1 for fresh-process external oracle")
	}
	requireGit(t)
	t.Log("spec fixtures casefold=false")
	endpoints, probes := 0, 0
	for _, c := range fixtures(t) {
		t.Run(c.ID, func(t *testing.T) {
			o := newOracle(t, c, false)
			query := c.Query.Path
			if c.Query.IsDir && !c.Query.NoSlash {
				query += "/"
			}
			got := o.query(t, query)
			if got != c.Expected {
				t.Fatalf("oracle %+v expected %+v", got, c.Expected)
			}
			endpoints++
			m, _, err := Compile(fixtureSources(c), Options{})
			if err != nil {
				t.Fatal(err)
			}
			var first *fixtureBlock
			for i := range len(c.Query.Path) {
				if c.Query.Path[i] != '/' {
					continue
				}
				ancestor := c.Query.Path[:i]
				observed := o.query(t, ancestor)
				probes++
				r := m.winner(ancestor, true, false, nil)
				checkRecord(t, r, observed)
				if first == nil && observed.Ignored {
					first = &fixtureBlock{Directory: ancestor, Source: observed.Source, Line: observed.Line, Pattern: observed.Pattern}
					break
				}
			}
			if (first == nil) != (c.Block == nil) || first != nil && *first != *c.Block {
				t.Fatalf("ancestor projection got %+v want %+v", first, c.Block)
			}
		})
	}
	if endpoints != 56 || probes != 29 {
		t.Fatalf("accounting endpoints=%d probes=%d", endpoints, probes)
	}
	t.Logf("endpoints=56 ancestor-probes=29 eligible=85 ineligible=0 missing=0 duplicate=0 failed=0")
}
