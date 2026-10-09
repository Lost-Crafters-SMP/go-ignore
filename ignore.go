// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Standard source ranks. Depth only breaks ties within a rank.
const (
	RankGlobal       = 100
	RankInfoExclude  = 200
	RankPerDirectory = 300
	RankCommand      = 400
)

const defaultSourceBytes int64 = 100 << 20

// ErrInvalidInput identifies invalid API inputs.
var ErrInvalidInput = errors.New("invalid ignore input")

// Source provides bytes and policy metadata, not a filename to open.
type Source struct {
	Label string
	Scope string
	Rank  int
	Data  []byte
}

// Options controls ASCII folding and the per-source size limit.
type Options struct {
	CaseFold       bool
	MaxSourceBytes int64
}

// Action is the endpoint's winning action.
type Action uint8

const (
	// NoMatch indicates no endpoint winner (also used for blocked endpoints).
	NoMatch Action = iota
	// Exclude excludes the endpoint.
	Exclude
	// Include is an explicit negation winner.
	Include
)

// DiagnosticKind classifies compilation diagnostics.
type DiagnosticKind string

const (
	// MalformedPattern is an inert, syntactically invalid rule.
	MalformedPattern DiagnosticKind = "malformed-pattern"
	// OversizeSource means the entire source was skipped.
	OversizeSource DiagnosticKind = "oversize-source"
)

// Diagnostic is a detached compilation observation.
type Diagnostic struct {
	Source  string
	Line    int
	Pattern string
	Kind    DiagnosticKind
	Message string
}

// Rule exposes immutable provenance; its matching representation is private.
type Rule struct {
	source, scope, raw, pattern         string
	line, rank, depth, sequence         int
	negated, dirOnly, anchored, invalid bool
	program                             []instruction
}

// Source returns the source label.
func (r *Rule) Source() string { return r.source }

// Line returns the physical one-based line number.
func (r *Rule) Line() int { return r.line }

// Raw returns the physical line without its terminator or source BOM.
func (r *Rule) Raw() string { return r.raw }

// Pattern returns trimmed text without negation and directory-only flags.
func (r *Rule) Pattern() string { return r.pattern }

// Negated reports an include rule.
func (r *Rule) Negated() bool { return r.negated }

// DirOnly reports a directory-only rule.
func (r *Rule) DirOnly() bool { return r.dirOnly }

// Scope returns the root-relative source scope.
func (r *Rule) Scope() string { return r.scope }

// Rank returns the fixed source rank.
func (r *Rule) Rank() int { return r.rank }

// Block describes the first excluded strict ancestor.
type Block struct {
	Directory string
	Rule      *Rule
}

// Decision keeps endpoint provenance distinct from ancestor blocking.
type Decision struct {
	Action Action
	Rule   *Rule
	Block  *Block
}

// Ignored reports either an excluded endpoint or a blocking ancestor.
func (d Decision) Ignored() bool { return d.Block != nil || d.Action == Exclude }

// Matcher owns immutable compiled rules. Its zero value matches nothing.
type Matcher struct {
	rules       []*Rule
	diagnostics []Diagnostic
	fold        bool
}

func validatePath(path string, root bool) error {
	if root && path == "" {
		return nil
	}
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return ErrInvalidInput
	}
	for part := range strings.SplitSeq(path, "/") {
		if part == "" || part == "." || part == ".." {
			return ErrInvalidInput
		}
	}
	return nil
}

// Compile validates all inputs before compiling any source. Malformed rules are
// retained as inert rules with diagnostics; oversized sources are wholly skipped.
func Compile(sources []Source, opts Options) (*Matcher, []Diagnostic, error) {
	capBytes := opts.MaxSourceBytes
	if capBytes < 0 || capBytes > defaultSourceBytes {
		return nil, nil, fmt.Errorf("source cap: %w", ErrInvalidInput)
	}
	if capBytes == 0 {
		capBytes = defaultSourceBytes
	}
	for i, source := range sources {
		if err := validatePath(source.Scope, true); err != nil {
			return nil, nil, fmt.Errorf("source %d scope: %w", i, err)
		}
		if slices.Contains(source.Data, byte(0)) {
			return nil, nil, fmt.Errorf("source %d NUL: %w", i, ErrInvalidInput)
		}
	}
	m := &Matcher{fold: opts.CaseFold}
	for seq, source := range sources {
		if int64(len(source.Data)) > capBytes {
			m.diagnostics = append(m.diagnostics, Diagnostic{Source: source.Label, Kind: OversizeSource, Message: "source exceeds byte cap"})
			continue
		}
		data := strings.TrimPrefix(string(source.Data), "\xef\xbb\xbf")
		line := 0
		for raw := range strings.SplitSeq(data, "\n") {
			line++
			raw = strings.TrimSuffix(raw, "\r")
			if raw == "" || raw[0] == '#' {
				continue
			}
			text := trimSpaces(raw)
			r := &Rule{source: source.Label, scope: source.Scope, raw: raw, line: line, rank: source.Rank, sequence: seq}
			if source.Scope != "" {
				r.depth = strings.Count(source.Scope, "/") + 1
			}
			if strings.HasPrefix(text, "!") {
				r.negated = true
				text = text[1:]
			}
			if strings.HasSuffix(text, "/") {
				r.dirOnly = true
				text = text[:len(text)-1]
			}
			r.pattern = text
			r.anchored = strings.Contains(text, "/")
			text = strings.TrimPrefix(text, "/")
			program, err := compilePattern(text, opts.CaseFold)
			r.program = program
			if err != nil {
				r.invalid = true
				m.diagnostics = append(m.diagnostics, Diagnostic{Source: source.Label, Line: line, Pattern: r.pattern, Kind: MalformedPattern, Message: err.Error()})
			}
			m.rules = append(m.rules, r)
		}
	}
	slices.SortStableFunc(m.rules, func(a, b *Rule) int {
		for _, pair := range [][2]int{{a.rank, b.rank}, {a.depth, b.depth}, {a.sequence, b.sequence}, {a.line, b.line}} {
			if pair[0] < pair[1] {
				return -1
			}
			if pair[0] > pair[1] {
				return 1
			}
		}
		return 0
	})
	slices.SortStableFunc(m.diagnostics, func(a, b Diagnostic) int {
		if order := strings.Compare(a.Source, b.Source); order != 0 {
			return order
		}
		if a.Line < b.Line {
			return -1
		}
		if a.Line > b.Line {
			return 1
		}
		return strings.Compare(string(a.Kind), string(b.Kind))
	})
	return m, m.Diagnostics(), nil
}

func trimSpaces(raw string) string {
	end := len(raw)
	for end > 0 && raw[end-1] == ' ' {
		escapes := 0
		for i := end - 2; i >= 0 && raw[i] == '\\'; i-- {
			escapes++
		}
		if escapes%2 != 0 {
			break
		}
		end--
	}
	return raw[:end]
}

// Diagnostics returns a detached copy sorted by source label, line, and kind,
// with stable input-source sequence breaking ties.
func (m *Matcher) Diagnostics() []Diagnostic {
	if m == nil {
		return nil
	}
	return slices.Clone(m.diagnostics)
}

// Match evaluates a clean slash-relative byte path with no I/O.
func (m *Matcher) Match(path string, isDir bool) (Decision, error) {
	if m == nil {
		return Decision{}, fmt.Errorf("nil matcher: %w", ErrInvalidInput)
	}
	if err := validatePath(path, false); err != nil {
		return Decision{}, fmt.Errorf("path: %w", err)
	}
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if r := m.winner(path[:i], true, false); r != nil && !r.negated {
				return Decision{Block: &Block{Directory: path[:i], Rule: r}}, nil
			}
		}
	}
	r := m.winner(path, isDir, false)
	if isDir && (r == nil || r.negated) {
		r = m.winner(path, true, true)
	}
	if r == nil {
		return Decision{}, nil
	}
	action := Exclude
	if r.negated {
		action = Include
	}
	return Decision{Action: action, Rule: r}, nil
}

func (m *Matcher) winner(path string, isDir, slash bool) *Rule {
	for i := len(m.rules) - 1; i >= 0; i-- {
		r := m.rules[i]
		if r.invalid || (r.dirOnly && !isDir) {
			continue
		}
		rel := path
		if r.scope != "" {
			if len(path) < len(r.scope) || !equalBytes(path[:len(r.scope)], r.scope, m.fold) {
				continue
			}
			if len(path) == len(r.scope) {
				if !slash {
					continue
				}
				rel = ""
			} else {
				if path[len(r.scope)] != '/' {
					continue
				}
				rel = path[len(r.scope)+1:]
			}
		}
		if !r.anchored {
			if slash {
				rel = ""
			} else if at := strings.LastIndexByte(rel, '/'); at >= 0 {
				rel = rel[at+1:]
			}
		}
		if runProgram(r.program, rel, slash && r.anchored && rel != "", m.fold) {
			return r
		}
	}
	return nil
}

func foldByte(b byte, fold bool) byte {
	if fold && b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

func equalBytes(a, b string, fold bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if foldByte(a[i], fold) != foldByte(b[i], fold) {
			return false
		}
	}
	return true
}
