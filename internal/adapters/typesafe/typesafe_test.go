package typesafe_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/adapters/typesafe"
	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

// fakeJev answers every request with one canned response and keeps the last request.
func fakeJev(t *testing.T, status int, response string) (*httptest.Server, *[]byte, *string) {
	t.Helper()
	var body []byte
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path %s", r.URL.Path)
		}
		body, _ = io.ReadAll(r.Body)
		auth = r.Header.Get("Authorization")
		w.WriteHeader(status)
		io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &auth
}

func oneFile(t *testing.T) diff.File {
	t.Helper()
	files, err := diff.Parse("diff --git a/retry.go b/retry.go\n--- a/retry.go\n+++ b/retry.go\n@@ -1,2 +1,2 @@\n x := 1\n-tries := 5\n+tries := 3\n")
	if err != nil {
		t.Fatal(err)
	}
	return files[0]
}

func TestJudgeAsksJevAboutOneFileAndReadsTheAnswers(t *testing.T) {
	// given
	// ... Jev answering the four questions about a file
	srv, sent, auth := fakeJev(t, http.StatusOK, `{"model":"jev-1.14.0","answers":{
		"kind":{"type":"choice","choice":"behaviour_change","confidence":0.8},
		"central":{"type":"noul","noul":0.91},
		"contract":{"type":"noul","noul":0.12},
		"described":{"type":"noul","noul":0.85}}}`)
	judge := typesafe.New(typesafe.Config{APIKey: "k", BaseURL: srv.URL})

	// when
	// ... a file of a PR is judged
	got, err := judge.JudgeFile(context.Background(), "Cap retries", "Retries stop after three tries.", oneFile(t))

	// then
	// ... the answers come back as a judgement
	if err != nil {
		t.Fatal(err)
	}
	want := review.FileJudgement{Kind: review.KindBehaviour, Central: 0.91, Contract: 0.12, Described: 0.85}
	if got != want {
		t.Errorf("want %+v, got %+v", want, got)
	}

	// ... the request carried the key, the PR's title and description, the file's path and diff, and one option per change kind
	var req struct {
		State struct {
			PullRequest struct{ Title, Description string } `json:"pull_request"`
			File        struct{ Path, Diff string }         `json:"file"`
		} `json:"state"`
		Questions map[string]struct {
			Type     string          `json:"type"`
			Criteria json.RawMessage `json:"criteria"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	if *auth != "Bearer k" {
		t.Errorf("authorization %q", *auth)
	}
	if req.State.PullRequest.Title != "Cap retries" || req.State.PullRequest.Description != "Retries stop after three tries." {
		t.Errorf("pull request state %+v", req.State.PullRequest)
	}
	if req.State.File.Path != "retry.go" || !strings.Contains(req.State.File.Diff, "+tries := 3") || !strings.Contains(req.State.File.Diff, "-tries := 5") {
		t.Errorf("file state %+v", req.State.File)
	}
	var kinds map[review.ChangeKind]string
	if err := json.Unmarshal(req.Questions["kind"].Criteria, &kinds); err != nil {
		t.Fatal(err)
	}
	for _, k := range review.ChangeKinds {
		if kinds[k] == "" {
			t.Errorf("no option for %s", k)
		}
	}
	if req.Questions["central"].Type != "noul" || req.Questions["contract"].Type != "noul" || req.Questions["described"].Type != "noul" {
		t.Errorf("questions %+v", req.Questions)
	}
}

func TestJudgeFailsOnAnAnswerItCannotUse(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		response string
		want     string
	}{
		{"a refused request", http.StatusTooManyRequests, `{"detail":"slow down"}`, "429"},
		{"a kind it did not offer", http.StatusOK, `{"answers":{"kind":{"type":"choice","choice":"poetry"},"central":{"noul":0.5},"contract":{"noul":0.5},"described":{"noul":0.5}}}`, "poetry"},
		{"a missing answer", http.StatusOK, `{"answers":{"kind":{"type":"choice","choice":"test"},"central":{"noul":0.5}}}`, "contract"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			// ... Jev giving the case's response
			srv, _, _ := fakeJev(t, tc.status, tc.response)
			judge := typesafe.New(typesafe.Config{APIKey: "k", BaseURL: srv.URL})

			// when
			// ... a file is judged
			_, err := judge.JudgeFile(context.Background(), "t", "", oneFile(t))

			// then
			// ... it fails and says why, rather than guessing an answer
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}
