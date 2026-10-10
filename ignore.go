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
	idx, scopeID                        int32
	// head and tail are the literal runs bounding the wildcards of a scanned
	// rule; any matching candidate must start and end with them.
	head, tail string
}

// idxNone marks absent index entries.
const idxNone = -1

// litEntry records the best exact-match literal rule per key, split by
// directory-only applicability so winner can honour isDir without scanning.
type litEntry struct {
	plain int32 // best non-dirOnly rule, idxNone if none
	dir   int32 // best dirOnly rule, idxNone if none
}

func (e litEntry) pick(isDir bool) int32 {
	if isDir && e.dir > e.plain {
		return e.dir
	}
	return e.plain
}

// scopeGroup indexes the exact-match literal rules of one source scope. Rules
// anchored to the scope are keyed by their scope-relative path; bare-name rules
// are keyed by the candidate basename, which is scope-independent.
// anchoredMin/Max bound the byte lengths of the anchored keys so lookups for
// paths outside the range are skipped without hashing.
type scopeGroup struct {
	scope                    string
	strip                    int // len(scope)+1, or 0 for the root scope
	anchoredMin, anchoredMax int // inverted range when the map is empty
	anchored                 map[string]litEntry
	base                     map[string]litEntry
}

// eligible reports whether a non-slash candidate path lies inside the scope.
func (g *scopeGroup) eligible(path string, fold bool) bool {
	return g.scope == "" || scopeEligible(path, g.scope, fold)
}

// scopeEligible is the per-rule equivalent of scopeGroup.eligible.
func scopeEligible(path, scope string, fold bool) bool {
	return len(path) > len(scope) && path[len(scope)] == '/' && equalBytes(path[:len(scope)], scope, fold)
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
	// scopes indexes exact-match literal rules per source scope; wild lists the
	// remaining evaluable rules in ascending priority order. Both are built once
	// during Compile and only read afterwards.
	scopes []scopeGroup
	wild   []int32
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

// ruleBatch is the number of Rule values allocated per chunk. Batching keeps
// compilation allocations per source bounded by rule count / ruleBatch
// instead of one allocation per rule, with stable pointers into each chunk.
const ruleBatch = 256

// ruleArena hands out Rule storage from chunks sized to the expected rule
// count, so large compilations batch in ruleBatch chunks while per-directory
// compilations allocate exactly what they need. Handed-out pointers stay
// valid because chunks are never appended to after allocation.
type ruleArena struct {
	expected int
	chunk    []Rule
	used     int
}

func (a *ruleArena) next() *Rule {
	if a.used == len(a.chunk) {
		size := ruleBatch
		if a.expected > 0 && a.expected < size {
			size = a.expected
		}
		if size <= 0 {
			size = 1
		}
		a.chunk = make([]Rule, size)
		if a.expected > size {
			a.expected -= size
		} else {
			a.expected = 0
		}
		a.used = 0
	}
	r := &a.chunk[a.used]
	a.used++
	return r
}

// instrArena batches programs into stable backing arrays. A program always
// fits in one batch; starting another batch never copies earlier instructions
// or invalidates their slices. Small sources use their remaining rule count
// as the batch size; large sources bound normal batches at ruleBatch. A
// program larger than a normal batch gets exactly the space it needs. After
// the first compilation batch, new stores cover as much work again as has
// been stored so far, capped at one normal batch. Successive small additions
// share storage without changing one-shot Compile sizing. Rule
// chunks stay exact-sized: their larger slots make spare capacity a poor
// trade against the few chunk objects it saves.
type instrArena struct {
	store    []instruction
	scratch  []instruction
	expected int
	stored   int
	warm     bool
}

func newInstrArena(expected int) instrArena {
	return instrArena{expected: expected}
}

func (a *instrArena) add(prog []instruction) []instruction {
	if len(prog) > cap(a.store)-len(a.store) {
		size := max(len(prog), min(a.expected, ruleBatch))
		if a.warm {
			if floor := min(ruleBatch, a.stored); floor > size {
				size = floor
			}
		}
		a.store = make([]instruction, 0, size)
	}
	off := len(a.store)
	a.store = append(a.store, prog...)
	if a.expected > 0 {
		a.expected--
	}
	a.stored += len(prog)
	return a.store[off:len(a.store):len(a.store)]
}

// countRules counts the lines that produce rules, mirroring Compile's
// filter exactly (BOM, CRLF, blank and comment lines), so the rule pointer
// slice can be presized without a second copy of the source bytes.
func countRules(data []byte) int {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		data = data[3:]
	}
	n := 0
	start := 0
	consider := func(end int) {
		raw := data[start:end]
		if len(raw) > 0 && raw[len(raw)-1] == '\r' {
			raw = raw[:len(raw)-1]
		}
		if len(raw) > 0 && raw[0] != '#' {
			n++
		}
	}
	for i, b := range data {
		if b == '\n' {
			consider(i)
			start = i + 1
		}
	}
	if start < len(data) {
		consider(len(data))
	}
	return n
}

// Compile validates all inputs before compiling any source. Malformed rules are
// retained as inert rules with diagnostics; oversized sources are wholly skipped.
func Compile(sources []Source, opts Options) (*Matcher, []Diagnostic, error) {
	capBytes, err := sourceCap(opts.MaxSourceBytes)
	if err != nil {
		return nil, nil, err
	}
	if err := validateSources(sources, 0); err != nil {
		return nil, nil, err
	}
	c := newCompiler(opts.CaseFold)
	rules, diagnostics := c.compile(sources, 0, capBytes)
	m := &Matcher{fold: opts.CaseFold, rules: rules, diagnostics: diagnostics}
	m.buildIndex()
	sortDiagnostics(m.diagnostics)
	return m, m.Diagnostics(), nil
}

// sourceCap validates and resolves the per-source byte cap.
func sourceCap(capBytes int64) (int64, error) {
	if capBytes < 0 || capBytes > defaultSourceBytes {
		return 0, fmt.Errorf("source cap: %w", ErrInvalidInput)
	}
	if capBytes == 0 {
		capBytes = defaultSourceBytes
	}
	return capBytes, nil
}

// validateSources checks every source's scope and data, numbering error
// positions from base so incremental batches report cumulative offsets. NUL
// is rejected even in sources a size cap would skip.
func validateSources(sources []Source, base int) error {
	for i, source := range sources {
		if err := validatePath(source.Scope, true); err != nil {
			return fmt.Errorf("source %d scope: %w", base+i, err)
		}
		if slices.Contains(source.Data, byte(0)) {
			return fmt.Errorf("source %d NUL: %w", base+i, ErrInvalidInput)
		}
	}
	return nil
}

// rulePriority orders rules ascending by rank, scope depth, source sequence
// and physical line. Under that order the last matching rule wins, so
// candidate scans run in reverse.
func rulePriority(a, b *Rule) int {
	if a.rank != b.rank {
		if a.rank < b.rank {
			return -1
		}
		return 1
	}
	if a.depth != b.depth {
		if a.depth < b.depth {
			return -1
		}
		return 1
	}
	if a.sequence != b.sequence {
		if a.sequence < b.sequence {
			return -1
		}
		return 1
	}
	if a.line != b.line {
		if a.line < b.line {
			return -1
		}
		return 1
	}
	return 0
}

// compiler accumulates rule and program storage across one or more source
// batches. Compile uses a single batch; Builder reuses the arenas so accepted
// sources are never parsed or compiled twice.
type compiler struct {
	fold     bool
	rules    ruleArena
	programs instrArena
	scratch  []instruction
}

func newCompiler(fold bool) compiler {
	return compiler{fold: fold}
}

// compile parses sources into priority-sorted rules, numbering them from
// baseSeq. Oversized sources are wholly skipped with one diagnostic; malformed
// patterns become inert rules with diagnostics. No index is built: callers
// either merge the batch incrementally (Builder) or index the full rule set
// once (Compile). Inputs must already be validated.
func (c *compiler) compile(sources []Source, baseSeq int, capBytes int64) ([]*Rule, []Diagnostic) {
	total := 0
	for _, source := range sources {
		if int64(len(source.Data)) <= capBytes {
			total += countRules(source.Data)
		}
	}
	c.rules.expected += total
	c.programs.expected += total
	rules := make([]*Rule, 0, total)
	var diagnostics []Diagnostic
	for seq, source := range sources {
		if int64(len(source.Data)) > capBytes {
			diagnostics = append(diagnostics, Diagnostic{Source: source.Label, Kind: OversizeSource, Message: "source exceeds byte cap"})
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
			r := c.rules.next()
			r.source = source.Label
			r.scope = source.Scope
			r.raw = raw
			r.line = line
			r.rank = source.Rank
			r.sequence = baseSeq + seq
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
			prog, err := compilePatternBuf(c.scratch[:0], text, c.fold)
			if err != nil {
				r.invalid = true
				diagnostics = append(diagnostics, Diagnostic{Source: source.Label, Line: line, Pattern: r.pattern, Kind: MalformedPattern, Message: err.Error()})
			} else {
				c.scratch = prog
				r.program = c.programs.add(prog)
				if !isLiteralProgram(r.program) {
					r.head, r.tail = literalBounds(r.program)
				}
			}
			rules = append(rules, r)
		}
	}
	slices.SortStableFunc(rules, rulePriority)
	// Mark the program arena warm only after the batch completes: the first
	// batch (every one-shot Compile) keeps exact sizing, and later batches
	// share generously sized program stores.
	c.programs.warm = true
	return rules, diagnostics
}

// sortDiagnostics orders diagnostics by source label, line and kind, with
// stable input order breaking ties.
func sortDiagnostics(diagnostics []Diagnostic) {
	slices.SortStableFunc(diagnostics, func(a, b Diagnostic) int {
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

// isLiteralProgram reports whether a program is a single literal run (or empty),
// so its candidate test is exact equality and it can be served by the index.
func isLiteralProgram(program []instruction) bool {
	return len(program) == 0 || (len(program) == 1 && program[0].kind == literal)
}

// buildIndex populates the literal index and the scanned-candidate list from the
// priority-sorted rules. Maps keep the highest-priority rule per key; the wild
// list keeps ascending priority order so winner can scan it in reverse and stop
// at the indexed lower bound. Invalid rules match nothing and are dropped.
func (m *Matcher) buildIndex() {
	scopeOf := make(map[string]int32)
	for i, r := range m.rules {
		r.idx = int32(i)
		if r.invalid {
			continue
		}
		gi, ok := scopeOf[r.scope]
		if !ok {
			gi = int32(len(m.scopes))
			scopeOf[r.scope] = gi
			strip := 0
			if r.scope != "" {
				strip = len(r.scope) + 1
			}
			m.scopes = append(m.scopes, scopeGroup{scope: r.scope, strip: strip})
		}
		r.scopeID = gi
		if !isLiteralProgram(r.program) {
			m.wild = append(m.wild, r.idx)
		}
	}
	// Presize the exact-match maps per scope to avoid repeated map growth.
	anchoredCount := make([]int, len(m.scopes))
	baseCount := make([]int, len(m.scopes))
	for _, r := range m.rules {
		if r.invalid || !isLiteralProgram(r.program) {
			continue
		}
		if r.anchored {
			anchoredCount[r.scopeID]++
		} else {
			baseCount[r.scopeID]++
		}
	}
	for gi := range m.scopes {
		m.scopes[gi].anchored = make(map[string]litEntry, anchoredCount[gi])
		m.scopes[gi].base = make(map[string]litEntry, baseCount[gi])
	}
	for _, r := range m.rules {
		if r.invalid || !isLiteralProgram(r.program) {
			continue
		}
		key := ""
		if len(r.program) == 1 {
			key = r.program[0].text
		}
		if m.fold {
			key = foldKeyString(key)
		}
		group := &m.scopes[r.scopeID]
		index := group.base
		if r.anchored {
			index = group.anchored
		}
		entry, ok := index[key]
		if !ok {
			entry = litEntry{plain: idxNone, dir: idxNone}
		}
		if r.dirOnly {
			entry.dir = r.idx
		} else {
			entry.plain = r.idx
		}
		index[key] = entry
	}
	for gi := range m.scopes {
		g := &m.scopes[gi]
		g.anchoredMin, g.anchoredMax = 1, 0
		for key := range g.anchored {
			if len(key) < g.anchoredMin {
				g.anchoredMin = len(key)
			}
			if len(key) > g.anchoredMax {
				g.anchoredMax = len(key)
			}
		}
	}
}

// literalBounds returns the literal runs a wildcard program starts and ends
// with; matches must begin and end with them. Adjacent escape-merged runs are
// not joined, keeping the bounds conservative but valid.
func literalBounds(program []instruction) (head, tail string) {
	if len(program) > 1 {
		if program[0].kind == literal {
			head = program[0].text
		}
		if program[len(program)-1].kind == literal {
			tail = program[len(program)-1].text
		}
	}
	return head, tail
}

// foldKeyString folds pattern text for index keys.
func foldKeyString(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			folded := []byte(s)
			for j := i; j < len(folded); j++ {
				folded[j] = foldByte(folded[j], true)
			}
			return string(folded)
		}
	}
	return s
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
	d, _, err := m.decide(path, isDir)
	return d, err
}

// decide is the shared evaluation behind Match and Descend. Beyond the
// decision, it reports bareExclude: for a directory endpoint without a
// block, the bare-directory winner is a positive exclusion and therefore
// supplies the endpoint decision. That is exactly the condition under
// which Git declines to descend into the directory; a canonical
// slash-form exclusion alone is not, because deeper negations may still
// re-include the directory's children.
func (m *Matcher) decide(path string, isDir bool) (d Decision, bareExclude bool, err error) {
	if m == nil {
		return Decision{}, false, fmt.Errorf("nil matcher: %w", ErrInvalidInput)
	}
	if err := validatePath(path, false); err != nil {
		return Decision{}, false, fmt.Errorf("path: %w", err)
	}
	var scratch evalScratch
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if r := m.winner(path[:i], true, false, &scratch); r != nil && !r.negated {
				return Decision{Block: &Block{Directory: path[:i], Rule: r}}, false, nil
			}
		}
	}
	r := m.winner(path, isDir, false, &scratch)
	bareExclude = isDir && r != nil && !r.negated
	if isDir && !bareExclude {
		// The canonical slash-form evaluation replaces the bare result.
		r = m.winner(path, true, true, &scratch)
	}
	if r == nil {
		return Decision{}, bareExclude, nil
	}
	action := Exclude
	if r.negated {
		action = Include
	}
	return Decision{Action: action, Rule: r}, bareExclude, nil
}

// Descend evaluates the directory at path once and reports whether Git's
// traversal would descend into it, alongside that directory's canonical
// decision: d is always identical to Match(path, true). descend is false
// exactly when a strict ancestor is excluded (d.Block != nil) or the
// directory's own bare-directory decision positively excludes it — the
// conditions under which Git descends no further and no deeper negation
// can re-include anything. A canonical slash-form exclusion alone (the
// directory's contents, not itself, such as /cache/* matching cache)
// leaves descend true, so deeper negations may still re-include children,
// exactly as in git status traversal; the decision reported for such a
// directory remains its canonical result.
//
// path follows Match's input contract: a clean slash-relative path naming
// the directory, without a trailing separator. The traversal root itself
// has no path below the source scopes and is rejected like any other
// invalid input; callers evaluate their roots separately. Reserved paths
// are the caller's concern. Descend performs no filesystem access or
// policy discovery, is safe for concurrent use, and a zero-value matcher
// descends everywhere.
func (m *Matcher) Descend(path string) (d Decision, descend bool, err error) {
	d, bareExclude, err := m.decide(path, true)
	if err != nil {
		return Decision{}, false, err
	}
	return d, d.Block == nil && !bareExclude, nil
}

// lookupLiteral resolves an exact-match key, folding it first when needed. The
// buffer is reused across lookups so folded candidates avoid repeated allocation.
func lookupLiteral(index map[string]litEntry, key string, fold bool, buf []byte) (litEntry, bool, []byte) {
	if !fold {
		entry, ok := index[key]
		return entry, ok, buf
	}
	at := -1
	for i := 0; i < len(key); i++ {
		if key[i] >= 'A' && key[i] <= 'Z' {
			at = i
			break
		}
	}
	if at < 0 {
		entry, ok := index[key]
		return entry, ok, buf
	}
	if cap(buf) < len(key) {
		buf = make([]byte, len(key))
	}
	folded := buf[:len(key)]
	copy(folded, key)
	for i := at; i < len(folded); i++ {
		folded[i] = foldByte(folded[i], true)
	}
	entry, ok := index[string(folded)]
	return entry, ok, buf
}

// winner returns the highest-priority matching rule. Non-slash calls use the
// literal index: exact-match rules are resolved per eligible scope, and only
// wildcard candidates ranked above the best literal hit are scanned.
func (m *Matcher) winner(path string, isDir, slash bool, scratch *evalScratch) *Rule {
	if scratch == nil {
		scratch = new(evalScratch)
	}
	if slash {
		return m.scanWinner(path, isDir, true, scratch)
	}
	at := strings.LastIndexByte(path, '/')
	return m.bareWinner(path, path[at+1:], isDir, scratch, false)
}

// bareWinner is the non-slash winner with the candidate basename precomputed
// and optional structural eligibility: when allEligible is set, every scope
// group is known to contain path (the policy layer establishes this from the
// scope chain), so per-group byte comparisons are skipped.
func (m *Matcher) bareWinner(path, base string, isDir bool, scratch *evalScratch, allEligible bool) *Rule {
	if scratch == nil {
		scratch = new(evalScratch)
	}
	best := int32(idxNone)
	var bestRule *Rule
	buf := scratch.fold[:0]
	var elig []bool
	if !allEligible {
		elig = scratch.eligibility(len(m.scopes))
	}
	for gi := range m.scopes {
		g := &m.scopes[gi]
		if !allEligible {
			ok := g.eligible(path, m.fold)
			elig[gi] = ok
			if !ok {
				continue
			}
		}
		rel := path[g.strip:]
		if len(g.anchored) > 0 && len(rel) >= g.anchoredMin && len(rel) <= g.anchoredMax {
			entry, hit, newBuf := lookupLiteral(g.anchored, rel, m.fold, buf)
			buf = newBuf
			if hit {
				if idx := entry.pick(isDir); idx > best {
					best, bestRule = idx, m.rules[idx]
				}
			}
		}
		if len(g.base) > 0 {
			entry, hit, newBuf := lookupLiteral(g.base, base, m.fold, buf)
			buf = newBuf
			if hit {
				if idx := entry.pick(isDir); idx > best {
					best, bestRule = idx, m.rules[idx]
				}
			}
		}
	}
	for i := len(m.wild) - 1; i >= 0; i-- {
		idx := m.wild[i]
		if idx <= best {
			break
		}
		r := m.rules[idx]
		if r.dirOnly && !isDir {
			continue
		}
		if !allEligible && !elig[r.scopeID] {
			continue
		}
		rel := path
		if r.scope != "" {
			rel = path[len(r.scope)+1:]
		}
		if !r.anchored {
			rel = base
		}
		if r.head != "" && (len(rel) < len(r.head) || !equalBytes(rel[:len(r.head)], r.head, m.fold)) {
			continue
		}
		if r.tail != "" && (len(rel) < len(r.tail) || !equalBytes(rel[len(rel)-len(r.tail):], r.tail, m.fold)) {
			continue
		}
		if runProgram(r.program, rel, false, m.fold, scratch) {
			return r
		}
	}
	return bestRule
}

// scanWinner is the exhaustive reverse-priority scan. It serves the directory
// slash-form endpoint call, whose virtual trailing separator defeats exact-match
// lookup, and acts as the differential reference for the indexed path.
func (m *Matcher) scanWinner(path string, isDir, slash bool, scratch *evalScratch) *Rule {
	if scratch == nil {
		scratch = new(evalScratch)
	}
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
		if runProgram(r.program, rel, slash && r.anchored && rel != "", m.fold, scratch) {
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
