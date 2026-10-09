// SPDX-License-Identifier: Apache-2.0

package dataset

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// Generator parameters. Every knob is fixed here or in the per-set seed table
// so `cmd/benchsetup` output is byte-for-byte reproducible for a given commit
// of this module.
const (
	// queriesPerSet caps the labelled query list per rule set.
	queriesPerSet = 512
	// maxQueryDepth bounds generated query path depth (components).
	maxQueryDepth = 6
)

// words is the fixed vocabulary for names; a cycling counter with numeric
// suffixes keeps generated names unique across a whole set.
var words = []string{
	"vendor", "reports", "cache", "build", "dist", "out", "bin", "logs",
	"notes", "docs", "media", "assets", "static", "tmp", "scratch",
	"coverage", "target", "lib", "core", "api", "web", "app", "tools",
	"scripts", "tests", "bench", "data", "config", "local", "sample",
	"draft", "alpha", "beta", "delta", "gamma", "omega", "kappa",
	"sigma", "lambda", "atlas", "beacon", "cobalt", "domino", "ember",
	"fable", "granite", "harbor", "indigo", "jasper", "kernel",
}

// extensions is the fixed suffix vocabulary.
var extensions = []string{"log", "tmp", "bak", "swp", "o", "exe", "class", "pyc", "gen", "old", "orig", "part"}

// seeds fixes one RNG per rule set name. Changing a seed changes the dataset.
var seeds = map[string]uint64{
	"lit-100":      101,
	"wild-100":     102,
	"anchored-100": 103,
	"dir-100":      104,
	"mixed-10":     110,
	"mixed-100":    111,
	"mixed-1000":   112,
	"mixed-10000":  113,
	"wild-complex": 120,
}

// category enumerates generated pattern classes.
type category int

const (
	catBareLit category = iota
	catBareWild
	catAnchoredLit
	catAnchoredWild
	catDirOnly
	catCharClass
)

// profile fixes the category mix of a generated rule set (weights sum to 100
// for scaled sets; category-pure sets use weight 100).
type profile struct {
	bareLit, bareWild, anchoredLit, anchoredWild, dirOnly, charClass int
}

var mixedProfile = profile{bareLit: 40, bareWild: 20, anchoredLit: 15, anchoredWild: 15, dirOnly: 8, charClass: 2}

var profiles = map[string]profile{
	"lit-100":      {bareLit: 100},
	"wild-100":     {bareWild: 100},
	"anchored-100": {anchoredLit: 50, anchoredWild: 50},
	"dir-100":      {dirOnly: 100},
	"mixed-10":     mixedProfile,
	"mixed-100":    mixedProfile,
	"mixed-1000":   mixedProfile,
	"mixed-10000":  mixedProfile,
	"wild-complex": {bareWild: 60, anchoredWild: 40},
}

// rule is one generated pattern together with the data needed to construct
// one matching and one non-matching path. Keeping the construction data next
// to the pattern (instead of reparsing it) makes the hit/miss pair provably
// consistent with the emitted pattern text.
type rule struct {
	pattern string
	dirOnly bool
	kind    ruleKind
	a, b    string
}

type ruleKind int

const (
	kBareLit  ruleKind = iota // a                    -> basename
	kBareExt                  // *.b                  -> any .b basename
	kBarePre                  // a*                   -> prefix a
	kBareSuf                  // *a                   -> suffix a
	kBareQ                    // ?.b                  -> single-char .b
	kBareMid                  // a*b                  -> contains
	kAnchLit                  // /a/b                 -> exact root path
	kAnchFile                 // /a.b                 -> exact root file
	kAnchDir                  // /a                   -> root child
	kAnchExt                  // /a/*.b               -> direct children
	kAnchDbl                  // /a/**/b              -> a, any depth, b
	kLeadDbl                  // **/a/*.b             -> a dir anywhere
	kDirBare                  // a/                   -> dir anywhere
	kDirAnch                  // /a/b/                -> root path dir
	kDirDbl                   // **/a/                -> dir anywhere
	kClsNum                   // a[0-9].b             -> digit class
	kClsPre                   // [a-c]a               -> class prefix
)

// genState carries one rule set's deterministic generator.
type genState struct {
	rng  *rand.Rand
	word int
	ext  int
}

func (g *genState) nextWord() string {
	w := words[g.word%len(words)]
	n := g.word / len(words)
	g.word++
	if n == 0 {
		return w
	}
	return fmt.Sprintf("%s%d", w, n)
}

func (g *genState) nextExt() string {
	e := extensions[g.ext%len(extensions)]
	g.ext++
	return e
}

// dirPrefix returns a random relative directory of 0..maxQueryDepth-1
// components drawn from the vocabulary.
func (g *genState) dirPrefix() string {
	n := g.rng.IntN(maxQueryDepth)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = words[g.rng.IntN(len(words))]
	}
	return strings.Join(parts, "/")
}

func join(dir string, elems ...string) string {
	parts := make([]string, 0, len(elems)+1)
	if dir != "" {
		parts = append(parts, dir)
	}
	return strings.Join(append(parts, elems...), "/")
}

// buildRule emits one rule of a category; i varies the shape within it.
func (g *genState) buildRule(cat category, i int) rule {
	w := g.nextWord()
	switch cat {
	case catBareLit:
		// Literals always carry an extension so their basenames never
		// collide with vocabulary words used as directory components in
		// other queries (a file and directory cannot share a name).
		e := g.nextExt()
		return rule{pattern: w + "." + e, kind: kBareLit, a: w + "." + e}
	case catBareWild:
		switch i % 5 {
		case 0:
			e := g.nextExt()
			return rule{pattern: "*." + e, kind: kBareExt, a: e}
		case 1:
			return rule{pattern: w + "*", kind: kBarePre, a: w}
		case 2:
			return rule{pattern: "*" + w, kind: kBareSuf, a: w}
		case 3:
			e := g.nextExt()
			return rule{pattern: "?." + e, kind: kBareQ, a: e}
		default:
			return rule{pattern: w + "*" + g.nextExt(), kind: kBareMid, a: w, b: g.nextExt()}
		}
	case catAnchoredLit:
		switch i % 3 {
		case 0:
			b := g.nextWord()
			return rule{pattern: "/" + w + "/" + b, kind: kAnchLit, a: w, b: b}
		case 1:
			e := g.nextExt()
			return rule{pattern: "/" + w + "." + e, kind: kAnchFile, a: w + "." + e}
		default:
			return rule{pattern: "/" + w, kind: kAnchDir, a: w}
		}
	case catAnchoredWild:
		switch i % 4 {
		case 0:
			e := g.nextExt()
			return rule{pattern: "/" + w + "/*." + e, kind: kAnchExt, a: w, b: e}
		case 1:
			b := g.nextWord()
			return rule{pattern: "/" + w + "/**/" + b, kind: kAnchDbl, a: w, b: b}
		case 2:
			e := g.nextExt()
			return rule{pattern: "**/" + w + "/*." + e, kind: kLeadDbl, a: w, b: e}
		default:
			b := g.nextWord()
			return rule{pattern: "/" + w + "/**/" + b, kind: kAnchDbl, a: w, b: b}
		}
	case catDirOnly:
		switch i % 3 {
		case 0:
			return rule{pattern: w + "/", dirOnly: true, kind: kDirBare, a: w}
		case 1:
			b := g.nextWord()
			return rule{pattern: "/" + w + "/" + b + "/", dirOnly: true, kind: kDirAnch, a: w, b: b}
		default:
			e := g.nextExt()
			return rule{pattern: "**/" + w + e + "/", dirOnly: true, kind: kDirDbl, a: w + e}
		}
	default: // catCharClass
		if i%2 == 0 {
			e := g.nextExt()
			return rule{pattern: w + "[0-9]." + e, kind: kClsNum, a: w, b: e}
		}
		return rule{pattern: "[a-c]" + w, kind: kClsPre, a: w}
	}
}

// hitMiss constructs a matching file path and a non-matching file path for r
// under the directory prefix dir.
func (g *genState) hitMiss(r rule, dir string) (hit, miss string) {
	w := g.nextWord()
	switch r.kind {
	case kBareLit:
		return join(dir, r.a), join(dir, r.a+"x")
	case kBareExt:
		return join(dir, w+"."+r.a), join(dir, w+"."+r.a+"z")
	case kBarePre:
		return join(dir, r.a+"-"+w), join(dir, "q"+r.a+"-"+w)
	case kBareSuf:
		return join(dir, w+"-"+r.a), join(dir, r.a+"-lead")
	case kBareQ:
		return join(dir, "q."+r.a), join(dir, "qq."+r.a)
	case kBareMid:
		return join(dir, r.a+w+"."+r.b), join(dir, r.a+"."+r.b)
	case kAnchLit:
		return r.a + "/" + r.b, "outside/" + r.a + "/" + r.b
	case kAnchFile:
		return r.a, "outside/" + r.a
	case kAnchDir:
		return r.a, join(dir, r.a)
	case kAnchExt:
		return r.a + "/" + w + "." + r.b, r.a + "/" + w + "." + r.b + "z"
	case kAnchDbl:
		return r.a + "/" + w + "/deep/" + r.b, "outside/" + r.a + "/" + r.b
	case kLeadDbl:
		return join(dir, r.a, w+"."+r.b), join(dir, r.a, w+"."+r.b+"z")
	case kDirBare:
		return join(dir, r.a, w+".txt"), join(dir, r.a+"x", w+".txt")
	case kDirAnch:
		return r.a + "/" + r.b + "/" + w + ".txt", "outside/" + r.a + "/" + r.b + "/" + w + ".txt"
	case kDirDbl:
		return join(dir, r.a, w+".txt"), join(dir, r.a+"x", w+".txt")
	case kClsNum:
		return join(dir, r.a+"7"+"."+r.b), join(dir, r.a+"x"+"."+r.b)
	default: // kClsPre
		return join(dir, "b"+r.a), join(dir, "z"+r.a)
	}
}

// dirEndpoint returns a matching directory path for dir-only rules (empty
// for rules whose directory form is impractical to summarize).
func (g *genState) dirEndpoint(r rule, dir string) string {
	switch r.kind {
	case kDirBare:
		return join(dir, r.a)
	case kDirAnch:
		return r.a + "/" + r.b
	case kDirDbl:
		return join(dir, r.a)
	}
	return ""
}

// GenerateRuleSet produces rule text and candidate queries for one named
// synthetic set. Ignored labels are assigned by the Git oracle later; the
// generator only shapes the mix.
func GenerateRuleSet(name string) (rules string, count int, queries []Query) {
	prof, ok := profiles[name]
	if !ok {
		panic("unknown rule set " + name)
	}
	total := 100
	switch name {
	case "mixed-10":
		total = 10
	case "mixed-1000":
		total = 1000
	case "mixed-10000":
		total = 10000
	case "wild-complex":
		total = 500
	}
	g := &genState{rng: rand.New(rand.NewPCG(seeds[name], seeds[name]^0x5eed))}
	var lines []string
	var gen []rule
	cats := []struct {
		cat    category
		weight int
	}{
		{catBareLit, prof.bareLit},
		{catBareWild, prof.bareWild},
		{catAnchoredLit, prof.anchoredLit},
		{catAnchoredWild, prof.anchoredWild},
		{catDirOnly, prof.dirOnly},
		{catCharClass, prof.charClass},
	}
	for i := 0; i < total; i++ {
		pick := g.rng.IntN(100)
		acc := 0
		cat := cats[len(cats)-1].cat
		for _, c := range cats {
			acc += c.weight
			if pick < acc {
				cat = c.cat
				break
			}
		}
		r := g.buildRule(cat, i)
		gen = append(gen, r)
		lines = append(lines, r.pattern)
		// Every 12th bare *.ext wildcard gets a paired negation.
		if cat == catBareWild && i%12 == 0 && r.kind == kBareExt {
			lines = append(lines, "!keep-"+g.nextWord()+"."+r.a)
		}
		if i%25 == 0 && i > 0 {
			lines = append(lines, "", "# generated section "+fmt.Sprint(i/25))
		}
	}
	// Queries: a hit/miss pair per rule, directory endpoints for some
	// dir-only rules, plus neutral clean paths.
	var cands []Query
	for i, r := range gen {
		dir := g.dirPrefix()
		hit, miss := g.hitMiss(r, dir)
		cands = append(cands,
			Query{Path: hit, IsDir: false},
			Query{Path: miss, IsDir: false},
		)
		if r.dirOnly && i%4 == 0 {
			if ep := g.dirEndpoint(r, dir); ep != "" {
				cands = append(cands, Query{Path: ep, IsDir: true})
			}
		}
	}
	for i := 0; i < total/2+16; i++ {
		cands = append(cands, Query{Path: join(g.dirPrefix(), fmt.Sprintf("clean%d.md", i)), IsDir: false})
	}
	g.rng.Shuffle(len(cands), func(a, b int) { cands[a], cands[b] = cands[b], cands[a] })
	if len(cands) > queriesPerSet {
		cands = cands[:queriesPerSet]
	}
	return strings.Join(lines, "\n") + "\n", countRuleLines(lines), cands
}

// countRuleLines counts non-blank, non-comment pattern lines.
func countRuleLines(lines []string) int {
	n := 0
	for _, l := range lines {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		n++
	}
	return n
}
