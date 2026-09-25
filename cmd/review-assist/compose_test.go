package main

import (
	"context"
	"os/exec"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/adapters/github"
)

func TestPastedPRURLInOtherCaseIsTheLocalRepository(t *testing.T) {
	// given
	// ... a clone whose origin spells owner and name in mixed case
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", "git@github.com:TheLazyLemur/Review-Assist.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	// when
	// ... a PR URL spelled in lower case is the target
	_, number, localRepo, err := resolveTarget(context.Background(), dir, github.ExecRunner{Dir: dir}, "https://github.com/thelazylemur/review-assist/pull/3")

	// then
	// ... it is the repository checked out here
	if err != nil {
		t.Fatal(err)
	}
	if number != 3 || !localRepo {
		t.Fatalf("want #3 in the local repo, got #%d local=%v", number, localRepo)
	}
}
