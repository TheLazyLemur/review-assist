package review_test

import (
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

func TestMaxLevelPlanCoversEveryFileWithCorrectnessWithinBudget(t *testing.T) {
	// given
	// ... a PR touching six files of uneven size, governed by a rules file
	var files []diff.File
	for i, size := range []int{400, 10, 10, 10, 10, 200} {
		files = append(files, diff.File{NewPath: string(rune('a' + i)), Additions: size})
	}

	// when
	// ... the max review level is planned
	assignments := review.Plan(review.LevelMax, files, true)

	// then
	// ... every file is reviewed for correctness exactly once
	l1 := map[string]int{}
	seen := map[string]bool{}
	for _, u := range assignments {
		key := string(u.Lens.ID) + ":" + u.Scope()
		if seen[key] {
			t.Errorf("two assignments own %s", key)
		}
		seen[key] = true
		if u.Lens.ID == review.L1 {
			for _, f := range u.Files {
				l1[f]++
			}
		}
	}
	for _, f := range files {
		if l1[f.Path()] != 1 {
			t.Errorf("file %s has %d correctness reviewers, want 1", f.Path(), l1[f.Path()])
		}
	}

	// ... the specialist budget holds and the rules lens is included
	if len(assignments) > review.LevelMax.Budget() {
		t.Errorf("planned %d assignments, budget is %d", len(assignments), review.LevelMax.Budget())
	}
	if !hasLens(assignments, review.L9) {
		t.Error("rules lens missing although a rules file governs the change")
	}
}

func TestQuickLevelPlansASingleCorrectnessPass(t *testing.T) {
	// given
	// ... a PR touching three files
	files := []diff.File{{NewPath: "a"}, {NewPath: "b"}, {NewPath: "c"}}

	// when
	// ... the quick level is planned
	assignments := review.Plan(review.LevelQuick, files, true)

	// then
	// ... one correctness agent owns every file
	if len(assignments) != 1 || assignments[0].Lens.ID != review.L1 || len(assignments[0].Files) != 3 {
		t.Fatalf("want one L1 unit over 3 files, got %+v", assignments)
	}
}

func hasLens(assignments []review.Assignment, id review.LensID) bool {
	for _, u := range assignments {
		if u.Lens.ID == id {
			return true
		}
	}
	return false
}
