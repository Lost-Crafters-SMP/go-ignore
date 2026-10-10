// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"math"
	"slices"
)

// Builder incrementally compiles ignore sources into one Matcher, so callers
// that discover scoped sources lazily can match, descend and observe
// diagnostics between discoveries without recompiling inherited sources. After
// accepting sources s1..sn in Add order the builder is exactly equivalent to
// Compile on the concatenated list: matching decisions, descent verdicts,
// diagnostics and rule provenance included. Sources compile once and stay
// loaded, including scopes of sibling branches visited earlier; scope
// eligibility keeps one branch's rules out of another's matches.
//
// NewBuilder validates options; a zero-value Builder adopts the defaults.
// Builders are not safe for concurrent use and require external
// serialisation. Add validates and prepares a whole batch before publishing
// it, so whenever Add returns an error the builder is unchanged; malformed
// patterns remain nonfatal diagnostics. Build seals the builder permanently
// and publishes its existing compiled state as an immutable Matcher that is
// safe for concurrent use.
type Builder struct {
	// s is shared by every copy of a Builder handle, so sealing one seals all.
	s *builderState
}

// builderState owns the matcher under construction beside bookkeeping that
// only exists while additions are possible.
type builderState struct {
	m        *Matcher
	capBytes int64
	comp     compiler
	// count is the number of accepted sources and the sequence base of the
	// next batch.
	count int
	// scopeOf maps scope strings to stable scopeGroup positions.
	scopeOf map[string]int32
	// shift remaps displaced rule positions during interleaved merges;
	// batchWild stages a batch's wildcard positions. Both are scratch.
	shift     []int32
	batchWild []int32
	// orderBatch stages a batch's new scope IDs for the length-ordered index;
	// like shift it is scratch and released at Build.
	orderBatch []int32
	// hints counts a batch's literal rules per scope, anchored first, so new
	// literal maps are presized exactly like one-shot index construction.
	hints map[int32][2]int
	// diagClean records whether m.diagnostics is already in sorted order.
	diagClean bool
	sealed    bool
	// preCommit runs between batch preparation and the infallible commit when
	// set. Fault-injection tests use it to prove error atomicity; production
	// code leaves it nil.
	preCommit func() error
}

func newBuilderState(opts Options, capBytes int64) *builderState {
	return &builderState{
		m:        &Matcher{fold: opts.CaseFold},
		capBytes: capBytes,
		comp:     newCompiler(opts.CaseFold),
		scopeOf:  make(map[string]int32),
		hints:    make(map[int32][2]int),
	}
}

// NewBuilder validates opts and returns an empty incremental builder. It
// performs no compilation; sources arrive through Add.
func NewBuilder(opts Options) (*Builder, error) {
	capBytes, err := sourceCap(opts.MaxSourceBytes)
	if err != nil {
		return nil, err
	}
	return &Builder{s: newBuilderState(opts, capBytes)}, nil
}

// state returns the shared builder state, adopting the default options for a
// zero-value Builder.
func (b *Builder) state() *builderState {
	if b.s == nil {
		b.s = newBuilderState(Options{}, defaultSourceBytes)
	}
	return b.s
}

// Add validates, compiles and publishes a batch of sources, numbering them
// after every previously accepted source. Batches may mix arbitrary scopes and
// ranks. Empty sources and sources above MaxSourceBytes consume sequence
// positions without rules (the latter with one diagnostic); malformed patterns
// become inert rules with diagnostics. Whenever Add returns an error the
// builder is unchanged, and error positions are cumulative across batches.
// Add(nil) is a no-op on an open builder; on a sealed builder every Add,
// including Add(nil), wraps ErrInvalidInput.
func (b *Builder) Add(sources []Source) error {
	if b == nil {
		return fmt.Errorf("nil builder: %w", ErrInvalidInput)
	}
	s := b.state()
	if s.sealed {
		return fmt.Errorf("sealed builder: %w", ErrInvalidInput)
	}
	if len(sources) == 0 {
		return nil
	}
	if s.count+len(sources) < s.count {
		return fmt.Errorf("source count: %w", ErrInvalidInput)
	}
	if err := validateSources(sources, s.count); err != nil {
		return err
	}
	rules, diagnostics := s.comp.compile(sources, s.count, s.capBytes)
	// Rule and scope positions are int32-indexed; a batch can add at most one
	// scope per rule, so the rule bound covers the scope bound as well.
	if len(rules) > math.MaxInt32-len(s.m.rules) {
		return fmt.Errorf("rule index: %w", ErrInvalidInput)
	}
	if len(rules) > math.MaxInt32-len(s.m.scopes) {
		return fmt.Errorf("scope index: %w", ErrInvalidInput)
	}
	if s.preCommit != nil {
		if err := s.preCommit(); err != nil {
			return err
		}
	}
	// Commit: every step below is infallible for the validated, prepared
	// batch. Slice and map growth may allocate but cannot fail logically.
	s.count += len(sources)
	s.commit(rules, diagnostics)
	return nil
}

// commit publishes a prepared batch into the matcher: diagnostics, scope
// groups, the priority-ordered rule slice, the wildcard list and the literal
// index. It must only run for batches that passed Add's validation and limit
// checks.
func (s *builderState) commit(rules []*Rule, diagnostics []Diagnostic) {
	m := s.m
	m.diagnostics = append(m.diagnostics, diagnostics...)
	if len(diagnostics) > 0 {
		s.diagClean = false
	}
	for _, r := range rules {
		if r.invalid {
			continue
		}
		gi, ok := s.scopeOf[r.scope]
		if !ok {
			gi = int32(len(m.scopes))
			s.scopeOf[r.scope] = gi
			strip := 0
			if r.scope != "" {
				strip = len(r.scope) + 1
			}
			m.scopes = append(m.scopes, scopeGroup{scope: r.scope, strip: strip, anchoredMin: 1})
		}
		r.scopeID = gi
	}
	s.orderScopes()
	oldN := len(m.rules)
	start := oldN
	if len(rules) > 0 && oldN > 0 {
		// start is the first position any batch rule can occupy: every rule
		// before it sorts before the batch's lowest-priority rule.
		start, _ = slices.BinarySearchFunc(m.rules, rules[0], func(a, c *Rule) int {
			if ruleOutranks(a, c) {
				return 1
			}
			return -1
		})
	}
	m.rules = slices.Grow(m.rules, len(rules))[:oldN+len(rules)]
	if start == oldN {
		copy(m.rules[oldN:], rules)
		for i, r := range rules {
			r.idx = int32(oldN + i)
		}
	} else {
		s.spliceRules(rules, start, oldN)
	}
	s.mergeWild(rules)
	s.indexLiterals(rules)
}

// orderScopes merges this batch's newly appended scope groups into the
// length-ordered index. Scope IDs are stable and groups only ever append, so
// earlier merges never need repairing and rule splicing cannot disturb the
// order; matchers with no scopes keep no index. Called inside the infallible
// commit, after Add's validation and fault points.
func (s *builderState) orderScopes() {
	m := s.m
	added := len(m.scopes) - len(m.scopeOrder)
	if added <= 0 {
		return
	}
	if len(m.scopeOrder) == 0 {
		// The first published scope builds the index whole.
		m.scopeOrder = make([]int32, len(m.scopes))
		for i := range m.scopeOrder {
			m.scopeOrder[i] = int32(i)
		}
		slices.SortFunc(m.scopeOrder, m.compareScopeOrder)
		return
	}
	s.orderBatch = slices.Grow(s.orderBatch[:0], added)[:added]
	for i := range s.orderBatch {
		s.orderBatch[i] = int32(len(m.scopeOrder) + i)
	}
	if added == 1 {
		// Single-scope batches (the common lazy-discovery shape) insert by
		// binary search instead of sorting a one-element batch.
		at, _ := slices.BinarySearchFunc(m.scopeOrder, s.orderBatch[0], m.compareScopeOrder)
		old := len(m.scopeOrder)
		m.scopeOrder = slices.Grow(m.scopeOrder, 1)[:old+1]
		copy(m.scopeOrder[at+1:], m.scopeOrder[at:old])
		m.scopeOrder[at] = s.orderBatch[0]
		return
	}
	slices.SortFunc(s.orderBatch, m.compareScopeOrder)
	old := len(m.scopeOrder)
	m.scopeOrder = slices.Grow(m.scopeOrder, added)[:old+added]
	i, j, out := old-1, added-1, len(m.scopeOrder)-1
	for j >= 0 {
		if i >= 0 && m.compareScopeOrder(m.scopeOrder[i], s.orderBatch[j]) > 0 {
			m.scopeOrder[out] = m.scopeOrder[i]
			i--
		} else {
			m.scopeOrder[out] = s.orderBatch[j]
			j--
		}
		out--
	}
}

// spliceRules merges a batch into the priority-ordered rule slice from its
// first affected position upward. Displaced rules keep their identity and no
// previously published Rule field is rewritten; only wildcard positions and
// the literal entries whose winners moved are repaired. Old positions are
// processed in descending order so a moved winner's new position can never be
// mistaken for the smaller old position of a shadowed rule.
func (s *builderState) spliceRules(rules []*Rule, start, oldN int) {
	m := s.m
	s.shift = slices.Grow(s.shift[:0], oldN-start)[:oldN-start]
	relocate := func(r *Rule, old, next int32) {
		if r.invalid || !isLiteralProgram(r.program) {
			return
		}
		g := &m.scopes[r.scopeID]
		index := g.base
		if r.anchored {
			index = g.anchored
		}
		key := literalKey(r, m.fold)
		e := index[key]
		if r.dirOnly && e.dir == old {
			e.dir = next
			index[key] = e
		} else if !r.dirOnly && e.plain == old {
			e.plain = next
			index[key] = e
		}
	}
	i, j, out := oldN-1, len(rules)-1, len(m.rules)-1
	for j >= 0 {
		if i >= start && ruleOutranks(m.rules[i], rules[j]) {
			s.shift[i-start] = int32(out)
			m.rules[out] = m.rules[i]
			relocate(m.rules[out], int32(i), int32(out))
			i--
		} else {
			m.rules[out] = rules[j]
			rules[j].idx = int32(out)
			j--
		}
		out--
	}
	for i >= start {
		s.shift[i-start] = int32(i)
		i--
	}
	first, _ := slices.BinarySearch(m.wild, int32(start))
	for k := first; k < len(m.wild); k++ {
		m.wild[k] = s.shift[m.wild[k]-int32(start)]
	}
}

// mergeWild splices the batch's wildcard positions into the ascending
// wildcard list.
func (s *builderState) mergeWild(rules []*Rule) {
	m := s.m
	wild := 0
	for _, r := range rules {
		if !r.invalid && !isLiteralProgram(r.program) {
			wild++
		}
	}
	if wild == 0 {
		return
	}
	s.batchWild = slices.Grow(s.batchWild[:0], wild)[:wild]
	k := 0
	for _, r := range rules {
		if !r.invalid && !isLiteralProgram(r.program) {
			s.batchWild[k] = r.idx
			k++
		}
	}
	oldW := len(m.wild)
	m.wild = slices.Grow(m.wild, wild)[:oldW+wild]
	i, j, out := oldW-1, wild-1, len(m.wild)-1
	for j >= 0 {
		if i >= 0 && m.wild[i] > s.batchWild[j] {
			m.wild[out] = m.wild[i]
			i--
		} else {
			m.wild[out] = s.batchWild[j]
			j--
		}
		out--
	}
}

// indexLiterals inserts the batch's literal rules into their scope maps,
// keeping the highest-priority rule per key and side, and extends the anchored
// key-length bounds. New maps are presized to the batch's counts, mirroring
// one-shot index construction.
func (s *builderState) indexLiterals(rules []*Rule) {
	m := s.m
	clear(s.hints)
	for _, r := range rules {
		if r.invalid || !isLiteralProgram(r.program) {
			continue
		}
		counts := s.hints[r.scopeID]
		if r.anchored {
			counts[0]++
		} else {
			counts[1]++
		}
		s.hints[r.scopeID] = counts
	}
	for _, r := range rules {
		if r.invalid || !isLiteralProgram(r.program) {
			continue
		}
		g := &m.scopes[r.scopeID]
		index := &g.base
		if r.anchored {
			index = &g.anchored
		}
		if *index == nil {
			size := s.hints[r.scopeID][1]
			if r.anchored {
				size = s.hints[r.scopeID][0]
			}
			*index = make(map[string]litEntry, size)
		}
		key := literalKey(r, m.fold)
		e, ok := (*index)[key]
		if !ok {
			e = litEntry{plain: idxNone, dir: idxNone}
		}
		if r.dirOnly {
			e.dir = max(e.dir, r.idx)
		} else {
			e.plain = max(e.plain, r.idx)
		}
		(*index)[key] = e
		if r.anchored {
			if g.anchoredMax < g.anchoredMin {
				g.anchoredMin, g.anchoredMax = len(key), len(key)
			} else {
				g.anchoredMin, g.anchoredMax = min(g.anchoredMin, len(key)), max(g.anchoredMax, len(key))
			}
		}
	}
}

// literalKey returns the literal index key of a rule's exact-match program.
func literalKey(r *Rule, fold bool) string {
	key := ""
	if len(r.program) == 1 {
		key = r.program[0].text
	}
	if fold {
		key = foldKeyString(key)
	}
	return key
}

// Match evaluates a path exactly like the Matcher compiled from every source
// accepted so far.
func (b *Builder) Match(path string, isDir bool) (Decision, error) {
	if b == nil {
		return Decision{}, fmt.Errorf("nil builder: %w", ErrInvalidInput)
	}
	return b.state().m.Match(path, isDir)
}

// Descend evaluates a directory's canonical decision and Git's descent
// verdict exactly like the Matcher compiled from every source accepted so far.
func (b *Builder) Descend(path string) (Decision, bool, error) {
	if b == nil {
		return Decision{}, false, fmt.Errorf("nil builder: %w", ErrInvalidInput)
	}
	return b.state().m.Descend(path)
}

// Diagnostics returns a detached copy of the cumulative compilation
// diagnostics in deterministic source/line/kind order with stable
// source-sequence tie-breaking, exactly like Compile's.
func (b *Builder) Diagnostics() []Diagnostic {
	if b == nil {
		return nil
	}
	s := b.state()
	if !s.diagClean {
		sortDiagnostics(s.m.diagnostics)
		s.diagClean = true
	}
	return slices.Clone(s.m.diagnostics)
}

// Build seals the builder permanently and returns its matcher beside fresh
// detached diagnostics. The matcher is the builder's existing compiled state:
// no recompilation, cloning or copying happens, previously published Rule
// values are unchanged, and the matcher is safe for concurrent use from here
// on. Repeated calls return the same Matcher with independently detached
// diagnostics. Match, Descend and Diagnostics keep working afterwards; every
// Add, including Add(nil), fails. An empty builder builds an empty matcher,
// and Build on a nil builder returns (nil, nil).
func (b *Builder) Build() (*Matcher, []Diagnostic) {
	if b == nil {
		return nil, nil
	}
	s := b.state()
	if !s.sealed {
		s.sealed = true
		s.scopeOf = nil
		s.shift = nil
		s.batchWild = nil
		s.orderBatch = nil
		s.hints = nil
		s.comp = compiler{}
		s.preCommit = nil
	}
	if !s.diagClean {
		sortDiagnostics(s.m.diagnostics)
		s.diagClean = true
	}
	return s.m, s.m.Diagnostics()
}
