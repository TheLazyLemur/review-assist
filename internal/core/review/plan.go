package review

import (
	"fmt"
	"sort"
	"strings"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
)

// Level controls how many specialist agents the fan-out spawns.
type Level int

const (
	LevelQuick    Level = 1 // one correctness pass, no verifier
	LevelStandard Level = 2 // a few whole-PR lenses + verifier
	LevelThorough Level = 3 // correctness split by scope + risk lenses + verifier
	LevelMax      Level = 4 // widest fan-out the budget allows + verifier
)

var Levels = []Level{LevelQuick, LevelStandard, LevelThorough, LevelMax}

type levelSpec struct {
	name     string
	budget   int      // max specialists
	l1Scopes int      // how many scopes the correctness lens is split into
	wholePR  []LensID // extra lenses run once over the whole PR, in priority order
	verifier bool
}

var specs = map[Level]levelSpec{
	LevelQuick:    {"quick", 1, 1, nil, false},
	LevelStandard: {"standard", 4, 1, []LensID{L2, L4}, true},
	LevelThorough: {"thorough", 7, 3, []LensID{L2, L3, L4, L7}, true},
	LevelMax:      {"max", 10, 4, []LensID{L2, L3, L4, L7, L6, L5, L8}, true},
}

func (l Level) spec() levelSpec {
	s, ok := specs[l]
	if !ok {
		panic(fmt.Sprintf("unknown review level %d", l))
	}
	return s
}

func (l Level) String() string    { return l.spec().name }
func (l Level) Budget() int       { return l.spec().budget }
func (l Level) HasVerifier() bool { return l.spec().verifier }

// Agents is the total number of agents a level spawns, verifier included.
func (l Level) Agents(files []diff.File, hasRules bool) int {
	n := len(Plan(l, files, hasRules))
	if l.HasVerifier() {
		n++
	}
	return n
}

// Unit is one specialist's job: one lens over one scope.
type Unit struct {
	Lens  Lens
	Files []string
}

func (u Unit) Scope() string { return strings.Join(u.Files, ",") }

func (u Unit) Name() string {
	return fmt.Sprintf("%s %s · %s", u.Lens.ID, u.Lens.Name, scopeLabel(u.Files))
}

func scopeLabel(files []string) string {
	switch len(files) {
	case 0:
		return "no files"
	case 1:
		return files[0]
	default:
		return fmt.Sprintf("%s +%d", files[0], len(files)-1)
	}
}

// Plan decomposes a review into (scope × lens) units. Correctness (L1) covers
// every file exactly once; other lenses run over the whole PR, and L9 is added
// when a rules file governs the repo. The level's budget caps the total.
func Plan(level Level, files []diff.File, hasRules bool) []Unit {
	s := level.spec()
	all := make([]string, 0, len(files))
	for _, f := range files {
		all = append(all, f.Path())
	}

	var units []Unit
	for _, scope := range partition(files, s.l1Scopes) {
		units = append(units, Unit{Lens: lenses[L1], Files: scope})
	}

	extra := s.wholePR
	if hasRules && level != LevelQuick {
		// Rules checks are cheap and nothing else catches them.
		extra = append([]LensID{L9}, extra...)
	}
	for _, id := range extra {
		if len(units) >= s.budget {
			break
		}
		units = append(units, Unit{Lens: lenses[id], Files: all})
	}
	return units
}

// partition splits files into at most k scopes balanced by changed lines,
// keeping each scope's files in diff order.
func partition(files []diff.File, k int) [][]string {
	if k > len(files) {
		k = len(files)
	}
	if k <= 1 {
		all := make([]string, 0, len(files))
		for _, f := range files {
			all = append(all, f.Path())
		}
		return [][]string{all}
	}

	idx := make([]int, len(files))
	for i := range idx {
		idx[i] = i
	}
	weight := func(i int) int { return files[i].Additions + files[i].Deletions + 1 }
	sort.SliceStable(idx, func(a, b int) bool { return weight(idx[a]) > weight(idx[b]) })

	bins := make([][]int, k)
	loads := make([]int, k)
	for _, i := range idx {
		min := 0
		for b := 1; b < k; b++ {
			if loads[b] < loads[min] {
				min = b
			}
		}
		bins[min] = append(bins[min], i)
		loads[min] += weight(i)
	}

	out := make([][]string, 0, k)
	for _, bin := range bins {
		sort.Ints(bin)
		scope := make([]string, 0, len(bin))
		for _, i := range bin {
			scope = append(scope, files[i].Path())
		}
		out = append(out, scope)
	}
	return out
}
