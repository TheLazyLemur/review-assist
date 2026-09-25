package bitbucket_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TheLazyLemur/review-assist/internal/adapters/bitbucket"
	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

var repo = pr.Repo{Platform: pr.Bitbucket, Hostname: "bitbucket.org", Owner: "acme", Name: "shop"}

const listPath = "/repositories/acme/shop/pullrequests"

const prPath = "/repositories/acme/shop/pullrequests/7"

// parseTime rather than time.Date: which Location a +00:00 offset decodes to
// depends on the machine's time zone.
func parseTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func basicAuth(email, token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token))
}

func pullRequestJSON(id int, state, nickname string) string {
	return fmt.Sprintf(`{"id":%d,"title":"t%d","state":%q,"author":{"nickname":%q}}`, id, id, state, nickname)
}

func TestEveryRequestCarriesBasicAuthOfEmailAndToken(t *testing.T) {
	// given
	// ... a server that records the Authorization header of every request, with pull requests over two pages
	var got []string
	var srv *httptest.Server
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/user":
			fmt.Fprint(w, `{"nickname":"dan"}`)
		case r.URL.Query().Get("page") == "2":
			fmt.Fprintf(w, `{"values":[%s]}`, pullRequestJSON(2, "OPEN", "dan"))
		default:
			fmt.Fprintf(w, `{"values":[%s],"next":"%s%s?state=OPEN&pagelen=50&page=2"}`, pullRequestJSON(1, "OPEN", "dan"), srv.URL, listPath)
		}
	})
	c := bitbucket.NewClient(srv.URL, "dan@example.com", "s3cret", repo, bitbucket.Options{})

	// when
	// ... the viewer is read and the pull requests are listed
	_, viewerErr := c.Viewer(context.Background())
	_, listErr := c.List(context.Background(), pr.Open)

	// then
	// ... all three requests are authenticated with the email and API token
	if viewerErr != nil || listErr != nil {
		t.Fatalf("viewer: %v, list: %v", viewerErr, listErr)
	}
	want := basicAuth("dan@example.com", "s3cret")
	if !slices.Equal(got, []string{want, want, want}) {
		t.Fatalf("Authorization: want %q on 3 requests, got %q", want, got)
	}
}

func TestUnauthorisedErrorLeadsWithBitbucketsMessage(t *testing.T) {
	// given
	// ... a server that refuses the credentials
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"error","error":{"message":"Invalid credentials"}}`)
	})
	c := bitbucket.NewClient(srv.URL, "dan@example.com", "wrong", repo, bitbucket.Options{})

	// when
	// ... pull requests are listed
	_, err := c.List(context.Background(), pr.Open)

	// then
	// ... the error starts with Bitbucket's message
	if err == nil || !strings.HasPrefix(err.Error(), "Invalid credentials") {
		t.Fatalf("got %v", err)
	}
}

func TestErrorFallsBackToTheStatusTextForAnUnexpectedBody(t *testing.T) {
	// given
	// ... a server that refuses the credentials with a body that is not JSON
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "<html>denied</html>")
	})
	c := bitbucket.NewClient(srv.URL, "dan@example.com", "wrong", repo, bitbucket.Options{})

	// when
	// ... pull requests are listed
	_, err := c.List(context.Background(), pr.Open)

	// then
	// ... the error starts with the HTTP status text
	if err == nil || !strings.HasPrefix(err.Error(), "Unauthorized") {
		t.Fatalf("got %v", err)
	}
}

func TestListRequestsTheBitbucketStatesForEachFilter(t *testing.T) {
	tests := []struct {
		state      pr.State
		wantQuery  []string
		wantStates []string
	}{
		{pr.Open, []string{"OPEN"}, []string{"OPEN"}},
		{pr.Merged, []string{"MERGED"}, []string{"MERGED"}},
		{pr.Closed, []string{"DECLINED", "SUPERSEDED"}, []string{"CLOSED", "CLOSED"}},
		{pr.All, []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED"}, []string{"OPEN", "MERGED", "CLOSED", "CLOSED"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			// given
			// ... a server that returns one pull request in each requested state
			var query []string
			srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
				query = r.URL.Query()["state"]
				var values []string
				for i, s := range query {
					values = append(values, pullRequestJSON(i+1, s, "dan"))
				}
				fmt.Fprintf(w, `{"values":[%s]}`, strings.Join(values, ","))
			})
			c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

			// when
			// ... pull requests are listed with the filter
			got, err := c.List(context.Background(), tt.state)

			// then
			// ... the Bitbucket states are requested and mapped to the domain's
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(query, tt.wantQuery) {
				t.Errorf("state query: want %v, got %v", tt.wantQuery, query)
			}
			var states []string
			for _, s := range got {
				states = append(states, s.State)
			}
			if !slices.Equal(states, tt.wantStates) {
				t.Errorf("states: want %v, got %v", tt.wantStates, states)
			}
		})
	}
}

func TestListMapsPullRequestFields(t *testing.T) {
	// given
	// ... a server with one draft pull request
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"values":[{"id":7,"title":"Add basket","state":"OPEN","draft":true,
			"author":{"nickname":"dan","display_name":"Dan R"},
			"source":{"branch":{"name":"feat/basket"}},"destination":{"branch":{"name":"main"}},
			"updated_on":"2026-09-24T10:11:12.123456+00:00"}]}`)
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... open pull requests are listed
	got, err := c.List(context.Background(), pr.Open)

	// then
	// ... its fields land in the summary
	if err != nil {
		t.Fatal(err)
	}
	want := []pr.Summary{{
		Number: 7, Title: "Add basket", Author: "dan", HeadRef: "feat/basket", BaseRef: "main",
		IsDraft: true, State: "OPEN", UpdatedAt: parseTime(t, "2026-09-24T10:11:12.123456+00:00"),
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %+v, got %+v", want, got)
	}
}

func TestListFollowsNextAcrossPages(t *testing.T) {
	// given
	// ... pull requests split over two pages, the first linking to the second
	var pagelen, sort string
	var srv *httptest.Server
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprintf(w, `{"values":[%s]}`, pullRequestJSON(3, "OPEN", "dan"))
			return
		}
		pagelen, sort = r.URL.Query().Get("pagelen"), r.URL.Query().Get("sort")
		fmt.Fprintf(w, `{"values":[%s,%s],"next":"%s%s?state=OPEN&pagelen=50&page=2"}`,
			pullRequestJSON(1, "OPEN", "dan"), pullRequestJSON(2, "OPEN", "dan"), srv.URL, listPath)
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... open pull requests are listed
	got, err := c.List(context.Background(), pr.Open)

	// then
	// ... all three come back, asked for 50 a page, most recently updated first
	if err != nil {
		t.Fatal(err)
	}
	var numbers []int
	for _, s := range got {
		numbers = append(numbers, s.Number)
	}
	if !slices.Equal(numbers, []int{1, 2, 3}) {
		t.Errorf("numbers: want [1 2 3], got %v", numbers)
	}
	if pagelen != "50" {
		t.Errorf("pagelen: want 50, got %q", pagelen)
	}
	if sort != "-updated_on" {
		t.Errorf("sort: want -updated_on, got %q", sort)
	}
}

func TestListStopsAtOneHundredPullRequests(t *testing.T) {
	// given
	// ... pages of 60 pull requests, each linking to the next
	var pages []int
	var srv *httptest.Server
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pages = append(pages, page)
		var values []string
		for i := range 60 {
			values = append(values, pullRequestJSON(i+1, "OPEN", "dan"))
		}
		fmt.Fprintf(w, `{"values":[%s],"next":"%s%s?page=%d"}`, strings.Join(values, ","), srv.URL, listPath, page+1)
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... open pull requests are listed
	got, err := c.List(context.Background(), pr.Open)

	// then
	// ... exactly 100 come back and the third page is never requested
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 {
		t.Errorf("want 100 pull requests, got %d", len(got))
	}
	if !slices.Equal(pages, []int{0, 1}) {
		t.Errorf("pages requested: want [0 1], got %v", pages)
	}
}

func TestListDoesNotSendCredentialsToAnotherHost(t *testing.T) {
	// given
	// ... a first page whose next link points at another server
	var otherHit bool
	other := serve(t, func(w http.ResponseWriter, r *http.Request) {
		otherHit = true
		fmt.Fprint(w, `{"values":[]}`)
	})
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"values":[],"next":"%s%s?page=2"}`, other.URL, listPath)
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... open pull requests are listed
	_, err := c.List(context.Background(), pr.Open)

	// then
	// ... the list fails without calling the other server
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if otherHit {
		t.Error("the other server was called")
	}
}

func TestListRejectsAnUnknownState(t *testing.T) {
	// given
	// ... a server that answers with a state Bitbucket has not documented
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"values":[%s]}`, pullRequestJSON(1, "QUEUED", "dan"))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... open pull requests are listed
	_, err := c.List(context.Background(), pr.Open)

	// then
	// ... the list fails naming the state rather than dropping the pull request
	if err == nil || !strings.Contains(err.Error(), "QUEUED") {
		t.Fatalf("got %v", err)
	}
}

func TestViewerOwnsThePullRequestsTheyOpened(t *testing.T) {
	// given
	// ... a viewer whose display name differs from their nickname, and their pull request
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			fmt.Fprint(w, `{"nickname":"dan","display_name":"Dan R","account_id":"712020:abc","uuid":"{u}"}`)
		case listPath:
			fmt.Fprint(w, `{"values":[{"id":1,"state":"OPEN","author":{"nickname":"dan","display_name":"Dan R","account_id":"712020:abc"}}]}`)
		}
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})
	viewer, err := c.Viewer(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// when
	// ... open pull requests are listed
	prs, err := c.List(context.Background(), pr.Open)

	// then
	// ... the viewer is the pull request's author
	if err != nil {
		t.Fatal(err)
	}
	if viewer != "dan" {
		t.Errorf("viewer: want dan, got %q", viewer)
	}
	if len(prs) != 1 {
		t.Fatalf("want 1 pull request, got %d", len(prs))
	}
	if !pr.IsOwn(prs[0].Author, viewer) {
		t.Errorf("IsOwn(%q, %q) is false", prs[0].Author, viewer)
	}
}

func pullRequestWithParticipants(participants string) string {
	return `{"id":7,"title":"Add basket","description":"Adds a basket.","state":"OPEN","draft":false,
		"author":{"nickname":"dan"},
		"source":{"branch":{"name":"feat/basket"},"commit":{"hash":"a1b2c3d4e5f6"}},
		"destination":{"branch":{"name":"main"},"commit":{"hash":"f6e5d4c3b2a1"}},
		"created_on":"2026-09-20T08:00:00.000001+00:00","updated_on":"2026-09-24T10:11:12.123456+00:00",
		"links":{"html":{"href":"https://bitbucket.org/acme/shop/pull-requests/7"}},
		"participants":[` + participants + `]}`
}

func TestGetMapsPullRequestFields(t *testing.T) {
	// given
	// ... a pull request with no participants
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != prPath {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, pullRequestWithParticipants(""))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... the pull request is read
	got, err := c.Get(context.Background(), 7)

	// then
	// ... head and base are the source and destination commits, and the rest maps across
	if err != nil {
		t.Fatal(err)
	}
	want := &pr.PR{
		Summary: pr.Summary{
			Number: 7, Title: "Add basket", Author: "dan", HeadRef: "feat/basket", BaseRef: "main",
			State: "OPEN", UpdatedAt: parseTime(t, "2026-09-24T10:11:12.123456+00:00"),
		},
		Body: "Adds a basket.", URL: "https://bitbucket.org/acme/shop/pull-requests/7",
		HeadSHA: "a1b2c3d4e5f6", BaseSHA: "f6e5d4c3b2a1", CreatedAt: parseTime(t, "2026-09-20T08:00:00.000001+00:00"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %+v, got %+v", want, got)
	}
}

func TestGetTurnsParticipantDecisionsIntoVerdicts(t *testing.T) {
	// given
	// ... one participant approved, one requested changes and one only commented
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, pullRequestWithParticipants(`
			{"user":{"nickname":"ann"},"state":"approved","participated_on":"2026-09-21T09:00:00+00:00"},
			{"user":{"nickname":"bob"},"state":"changes_requested","participated_on":"2026-09-22T09:00:00+00:00"},
			{"user":{"nickname":"cat"},"state":null,"participated_on":"2026-09-23T09:00:00+00:00"}`))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... the pull request is read
	got, err := c.Get(context.Background(), 7)

	// then
	// ... the approval and the request for changes are verdicts, the comment is not
	if err != nil {
		t.Fatal(err)
	}
	want := []pr.Verdict{
		{Author: "ann", Decision: pr.Approve, At: parseTime(t, "2026-09-21T09:00:00+00:00")},
		{Author: "bob", Decision: pr.RequestChanges, At: parseTime(t, "2026-09-22T09:00:00+00:00")},
	}
	if !reflect.DeepEqual(got.Verdicts, want) {
		t.Errorf("want %+v, got %+v", want, got.Verdicts)
	}
}

func TestGetRejectsAnUnknownParticipantState(t *testing.T) {
	// given
	// ... a participant in a state Bitbucket has not documented
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, pullRequestWithParticipants(`{"user":{"nickname":"ann"},"state":"vetoed"}`))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... the pull request is read
	_, err := c.Get(context.Background(), 7)

	// then
	// ... it fails naming the state rather than dropping the verdict
	if err == nil || !strings.Contains(err.Error(), "vetoed") {
		t.Fatalf("got %v", err)
	}
}

func TestDiffFollowsTheRedirectWithCredentials(t *testing.T) {
	// given
	// ... the diff URL redirects to another path on the same server
	const text = `diff --git a/basket.go b/basket.go
new file mode 100644
--- /dev/null
+++ b/basket.go
@@ -0,0 +1 @@
+package basket
diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1 +1 @@
-package old
+package main
`
	var redirectedAuth string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case prPath + "/diff":
			http.Redirect(w, r, "/repositories/acme/shop/diff/a1b2c3d4e5f6..f6e5d4c3b2a1", http.StatusFound)
		case "/repositories/acme/shop/diff/a1b2c3d4e5f6..f6e5d4c3b2a1":
			redirectedAuth = r.Header.Get("Authorization")
			fmt.Fprint(w, text)
		default:
			http.NotFound(w, r)
		}
	})
	c := bitbucket.NewClient(srv.URL, "dan@example.com", "s3cret", repo, bitbucket.Options{})

	// when
	// ... the diff is read
	got, err := c.Diff(context.Background(), 7)

	// then
	// ... the redirected request is authenticated and the text parses into both files
	if err != nil {
		t.Fatal(err)
	}
	wantAuth := basicAuth("dan@example.com", "s3cret")
	if redirectedAuth != wantAuth {
		t.Errorf("redirected Authorization: want %q, got %q", wantAuth, redirectedAuth)
	}
	files, err := diff.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path())
	}
	if !slices.Equal(paths, []string{"basket.go", "main.go"}) {
		t.Errorf("want [basket.go main.go], got %v", paths)
	}
}

func commentJSON(id int, extra string) string {
	return fmt.Sprintf(`{"id":%d,"content":{"raw":"c%d"},"user":{"nickname":"dan"},
		"created_on":"2026-09-24T10:00:00+00:00"%s}`, id, id, extra)
}

func TestCommentsFollowsNextAcrossPages(t *testing.T) {
	// given
	// ... comments split over two pages, the first linking to the second
	var srv *httptest.Server
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprintf(w, `{"values":[%s]}`, commentJSON(3, ""))
			return
		}
		fmt.Fprintf(w, `{"values":[%s,%s],"next":"%s%s/comments?pagelen=50&page=2"}`,
			commentJSON(1, ""), commentJSON(2, ""), srv.URL, prPath)
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... the comments are read
	got, err := c.Comments(context.Background(), 7)

	// then
	// ... all three come back
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, cm := range got {
		ids = append(ids, cm.ID)
	}
	if !slices.Equal(ids, []int64{1, 2, 3}) {
		t.Errorf("want [1 2 3], got %v", ids)
	}
}

func TestCommentsMapWhereEachPoints(t *testing.T) {
	// given
	// ... comments on the pull request, a file, a new line, a removed line and ranges
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != prPath+"/comments" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"values":[%s,%s,%s,%s,%s,%s,%s,%s]}`,
			commentJSON(1, ""),
			commentJSON(2, `,"inline":{"path":"a.go","from":null,"to":null}`),
			commentJSON(3, `,"inline":{"path":"b.go","from":4,"to":5}`),
			commentJSON(4, `,"inline":{"path":"c.go","from":6,"to":null}`),
			commentJSON(5, `,"inline":{"path":"d.go","from":null,"to":9,"start_from":null,"start_to":7}`),
			commentJSON(6, `,"inline":{"path":"e.go","from":null,"to":9,"start_from":3,"start_to":null}`),
			commentJSON(7, `,"inline":{"path":"f.go","from":null,"to":9,"start_from":null,"start_to":9}`),
			commentJSON(8, `,"inline":{"path":"g.go","from":null,"to":9,"start_from":9,"start_to":null}`))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... the comments are read
	got, err := c.Comments(context.Background(), 7)

	// then
	// ... each carries the anchor its inline block describes
	if err != nil {
		t.Fatal(err)
	}
	at := parseTime(t, "2026-09-24T10:00:00+00:00")
	want := []pr.Comment{
		{ID: 1, Author: "dan", Body: "c1", CreatedAt: at},
		{ID: 2, Author: "dan", Body: "c2", CreatedAt: at, Anchor: &pr.Anchor{Path: "a.go"}},
		{ID: 3, Author: "dan", Body: "c3", CreatedAt: at, Anchor: &pr.Anchor{Path: "b.go", Line: 5, Side: diff.Head}},
		{ID: 4, Author: "dan", Body: "c4", CreatedAt: at, Anchor: &pr.Anchor{Path: "c.go", Line: 6, Side: diff.Base}},
		{ID: 5, Author: "dan", Body: "c5", CreatedAt: at, Anchor: &pr.Anchor{Path: "d.go", Line: 9, Side: diff.Head, StartLine: 7, StartSide: diff.Head}},
		{ID: 6, Author: "dan", Body: "c6", CreatedAt: at, Anchor: &pr.Anchor{Path: "e.go", Line: 9, Side: diff.Head, StartLine: 3, StartSide: diff.Base}},
		{ID: 7, Author: "dan", Body: "c7", CreatedAt: at, Anchor: &pr.Anchor{Path: "f.go", Line: 9, Side: diff.Head}},
		{ID: 8, Author: "dan", Body: "c8", CreatedAt: at, Anchor: &pr.Anchor{Path: "g.go", Line: 9, Side: diff.Head, StartLine: 9, StartSide: diff.Base}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %+v, got %+v", want, got)
	}
}

func TestCommentsLinkAReplyToItsParent(t *testing.T) {
	// given
	// ... a comment and a reply to it
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"values":[%s,%s]}`, commentJSON(1, ""), commentJSON(2, `,"parent":{"id":1}`))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... the comments are read
	got, err := c.Comments(context.Background(), 7)

	// then
	// ... the reply points at its parent
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 comments, got %+v", got)
	}
	if got[1].ReplyTo != 1 {
		t.Errorf("ReplyTo: want 1, got %d", got[1].ReplyTo)
	}
}

func TestCommentsLeaveOutDeletedOnes(t *testing.T) {
	// given
	// ... a comment and a deleted one
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"values":[%s,%s]}`, commentJSON(1, ""), commentJSON(2, `,"deleted":true`))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... the comments are read
	got, err := c.Comments(context.Background(), 7)

	// then
	// ... only the live comment comes back
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("want only comment 1, got %+v", got)
	}
}

func TestCommentsLeaveOutOutdatedOnes(t *testing.T) {
	// given
	// ... a comment on a current line and one on a line from an older commit
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"values":[%s,%s]}`,
			commentJSON(1, `,"inline":{"path":"a.go","from":null,"to":3,"outdated":false}`),
			commentJSON(2, `,"inline":{"path":"a.go","from":null,"to":3,"outdated":true}`))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... the comments are read
	got, err := c.Comments(context.Background(), 7)

	// then
	// ... only the current comment comes back
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("want only comment 1, got %+v", got)
	}
}

func TestCommentsLeaveOutPendingOnes(t *testing.T) {
	// given
	// ... a posted comment and an unsubmitted draft
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"values":[%s,%s]}`, commentJSON(1, `,"pending":false`), commentJSON(2, `,"pending":true`))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... the comments are read
	got, err := c.Comments(context.Background(), 7)

	// then
	// ... only the posted comment comes back
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("want only comment 1, got %+v", got)
	}
}

func TestCommentsRejectAnInlineCommentWithNoPath(t *testing.T) {
	// given
	// ... an inline comment without the path Bitbucket's schema requires
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"values":[%s]}`, commentJSON(42, `,"inline":{"from":null,"to":3}`))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... the comments are read
	_, err := c.Comments(context.Background(), 7)

	// then
	// ... it fails naming the comment
	if err == nil || !strings.Contains(err.Error(), "42") {
		t.Fatalf("got %v", err)
	}
}

func TestCommentsRejectARangeStartWithNoEndLine(t *testing.T) {
	// given
	// ... an inline comment with a range start but neither from nor to
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"values":[%s]}`, commentJSON(42, `,"inline":{"path":"a.go","from":null,"to":null,"start_to":3}`))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... the comments are read
	_, err := c.Comments(context.Background(), 7)

	// then
	// ... it fails naming the comment
	if err == nil || !strings.Contains(err.Error(), "42") {
		t.Fatalf("got %v", err)
	}
}

type sentRequest struct {
	Method      string
	Path        string
	ContentType string
	Body        map[string]any
}

// record serves every request with status and keeps what was sent.
func record(t *testing.T, status func(r *http.Request) int) (*bitbucket.Client, *[]sentRequest) {
	t.Helper()
	var sent []sentRequest
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		s := sentRequest{Method: r.Method, Path: r.URL.Path, ContentType: r.Header.Get("Content-Type")}
		if err := json.NewDecoder(r.Body).Decode(&s.Body); err != nil && !errors.Is(err, io.EOF) {
			t.Errorf("decode %s %s: %v", r.Method, r.URL.Path, err)
		}
		sent = append(sent, s)
		w.WriteHeader(status(r))
	})
	return bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{}), &sent
}

func accept(*http.Request) int { return http.StatusOK }

func TestPostCommentOnThePullRequestHasNoInline(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... a comment without an anchor is posted
	err := c.PostComment(context.Background(), 7, pr.NewComment{Body: "Looks good"})

	// then
	// ... it posts only the content to the comments of the pull request
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{{Method: "POST", Path: prPath + "/comments", ContentType: "application/json", Body: map[string]any{
		"content": map[string]any{"raw": "Looks good"},
	}}}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestPostCommentOnAFileSendsThePathAndNoLine(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... a comment anchored on a file is posted
	err := c.PostComment(context.Background(), 7, pr.NewComment{Body: "Split this", Anchor: &pr.Anchor{Path: "a.go"}, HeadSHA: "a1b2"})

	// then
	// ... inline has the path and no line
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{{Method: "POST", Path: prPath + "/comments", ContentType: "application/json", Body: map[string]any{
		"content": map[string]any{"raw": "Split this"},
		"inline":  map[string]any{"path": "a.go"},
	}}}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestPostCommentOnAHeadLineSendsTo(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... a comment on line 12 of the head side is posted
	err := c.PostComment(context.Background(), 7, pr.NewComment{Body: "Off by one", Anchor: &pr.Anchor{Path: "a.go", Line: 12, Side: diff.Head}, HeadSHA: "a1b2"})

	// then
	// ... inline.to is the line
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{{Method: "POST", Path: prPath + "/comments", ContentType: "application/json", Body: map[string]any{
		"content": map[string]any{"raw": "Off by one"},
		"inline":  map[string]any{"path": "a.go", "to": float64(12)},
	}}}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestPostCommentOnABaseLineSendsFrom(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... a comment on line 8 of the base side is posted
	err := c.PostComment(context.Background(), 7, pr.NewComment{Body: "Why remove this?", Anchor: &pr.Anchor{Path: "a.go", Line: 8, Side: diff.Base}, HeadSHA: "a1b2"})

	// then
	// ... inline.from is the line
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{{Method: "POST", Path: prPath + "/comments", ContentType: "application/json", Body: map[string]any{
		"content": map[string]any{"raw": "Why remove this?"},
		"inline":  map[string]any{"path": "a.go", "from": float64(8)},
	}}}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestPostCommentOnARangeFailsWithoutARequest(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... a comment on lines 3 to 9 of the head side is posted
	err := c.PostComment(context.Background(), 7, pr.NewComment{Body: "All of this", HeadSHA: "a1b2",
		Anchor: &pr.Anchor{Path: "a.go", Line: 9, Side: diff.Head, StartLine: 3, StartSide: diff.Head}})

	// then
	// ... it fails naming ranges and sends nothing
	if err == nil || !strings.Contains(err.Error(), "range") {
		t.Fatalf("got %v", err)
	}
	if len(*sent) != 0 {
		t.Errorf("want no requests, got %+v", *sent)
	}
}

func TestReplyToAReplySendsTheCommentsOwnIDAsParent(t *testing.T) {
	// given
	// ... a server that records requests, and comment 5 that replies to comment 2
	c, sent := record(t, accept)
	parent := pr.Comment{ID: 5, ReplyTo: 2, Body: "Agreed"}

	// when
	// ... a reply to comment 5 is posted
	err := c.Reply(context.Background(), 7, parent, "Done")

	// then
	// ... parent.id is 5, not 2
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{{Method: "POST", Path: prPath + "/comments", ContentType: "application/json", Body: map[string]any{
		"content": map[string]any{"raw": "Done"},
		"parent":  map[string]any{"id": float64(5)},
	}}}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestDeleteCommentSendsADeleteToItsURL(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... comment 42 is deleted
	err := c.DeleteComment(context.Background(), 7, pr.Comment{ID: 42})

	// then
	// ... the request is a DELETE to that comment
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{{Method: "DELETE", Path: prPath + "/comments/42"}}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestApproveWithNoMessageSendsOnlyTheApproval(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... the pull request is approved with no message
	err := c.SubmitVerdict(context.Background(), 7, pr.Approve, "")

	// then
	// ... only the approve request is sent
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{{Method: "POST", Path: prPath + "/approve"}}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestRequestChangesSendsTheRequestChangesRequest(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... changes are requested with no message
	err := c.SubmitVerdict(context.Background(), 7, pr.RequestChanges, "")

	// then
	// ... the request-changes request is sent
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{{Method: "POST", Path: prPath + "/request-changes"}}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestVerdictWithAMessagePostsTheCommentFirst(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... changes are requested with a message
	err := c.SubmitVerdict(context.Background(), 7, pr.RequestChanges, "Needs tests")

	// then
	// ... the message is posted as a comment on the pull request, then the verdict
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{
		{Method: "POST", Path: prPath + "/comments", ContentType: "application/json", Body: map[string]any{"content": map[string]any{"raw": "Needs tests"}}},
		{Method: "POST", Path: prPath + "/request-changes"},
	}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestVerdictThatFailsAfterItsMessageSaysTheCommentWasPosted(t *testing.T) {
	// given
	// ... a server that accepts comments and refuses the approval
	c, sent := record(t, func(r *http.Request) int {
		if strings.HasSuffix(r.URL.Path, "/approve") {
			return http.StatusBadRequest
		}
		return http.StatusOK
	})

	// when
	// ... the pull request is approved with a message
	err := c.SubmitVerdict(context.Background(), 7, pr.Approve, "Nice work")

	// then
	// ... the comment and then the approval were sent, and the error says the comment was posted and keeps Bitbucket's reason
	if err == nil || !strings.Contains(err.Error(), "comment was posted") || !strings.Contains(err.Error(), "Bad Request") {
		t.Fatalf("got %v", err)
	}
	want := []sentRequest{
		{Method: "POST", Path: prPath + "/comments", ContentType: "application/json", Body: map[string]any{"content": map[string]any{"raw": "Nice work"}}},
		{Method: "POST", Path: prPath + "/approve"},
	}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestPostCommentFailsWhenThePostIsRedirected(t *testing.T) {
	// given
	// ... a server that redirects the comments POST to a URL that answers GET with 200
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prPath+"/comments" {
			http.Redirect(w, r, "/moved", http.StatusFound)
		}
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{})

	// when
	// ... a comment is posted
	err := c.PostComment(context.Background(), 7, pr.NewComment{Body: "Looks good"})

	// then
	// ... it fails naming the method rather than reporting a comment that was never posted
	if err == nil || !strings.Contains(err.Error(), "POST") {
		t.Fatalf("got %v", err)
	}
}

func TestMergeSendsTheBitbucketStrategyForEachMethod(t *testing.T) {
	tests := []struct {
		method       pr.MergeMethod
		wantStrategy string
	}{
		{pr.MergeCommit, "merge_commit"},
		{pr.Squash, "squash"},
		{pr.Rebase, "rebase_fast_forward"},
	}
	for _, tt := range tests {
		t.Run(string(tt.method), func(t *testing.T) {
			// given
			// ... a server that records requests
			c, sent := record(t, accept)

			// when
			// ... the pull request is merged with the method
			err := c.Merge(context.Background(), 7, tt.method)

			// then
			// ... it posts the matching merge_strategy to merge
			if err != nil {
				t.Fatal(err)
			}
			want := []sentRequest{{Method: "POST", Path: prPath + "/merge", ContentType: "application/json", Body: map[string]any{
				"merge_strategy": tt.wantStrategy,
			}}}
			if !reflect.DeepEqual(*sent, want) {
				t.Errorf("want %+v, got %+v", want, *sent)
			}
		})
	}
}

const taskPath = prPath + "/merge/task-status/t1"

// mergeTask answers the merge with 202 and a Location of its task status,
// which reports PENDING until the pending polls are used up and then finish.
// The client waits at most wait for the task.
func mergeTask(t *testing.T, pending int, wait time.Duration, finish http.HandlerFunc) (*bitbucket.Client, *[]string) {
	t.Helper()
	var polls []string
	var srv *httptest.Server
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case prPath + "/merge":
			w.Header().Set("Location", srv.URL+taskPath)
			w.WriteHeader(http.StatusAccepted)
		case taskPath:
			polls = append(polls, r.Method)
			if len(polls) <= pending {
				_, _ = fmt.Fprint(w, `{"task_status":"PENDING"}`)
				return
			}
			finish(w, r)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	return bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{PollInterval: time.Millisecond, MergeWait: wait}), &polls
}

func TestMergeAnsweredWithATaskPollsUntilItSucceeds(t *testing.T) {
	// given
	// ... a server that queues the merge as a task that is pending twice, then succeeds
	c, polls := mergeTask(t, 2, time.Minute, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"task_status":"SUCCESS"}`)
	})

	// when
	// ... the pull request is merged
	err := c.Merge(context.Background(), 7, pr.MergeCommit)

	// then
	// ... it succeeds after polling the task status three times
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*polls, []string{"GET", "GET", "GET"}) {
		t.Errorf("want 3 GET polls, got %v", *polls)
	}
}

func TestMergeWhoseTaskFailsReturnsBitbucketsReason(t *testing.T) {
	// given
	// ... a server that queues the merge as a task that fails with a conflict
	c, _ := mergeTask(t, 1, time.Minute, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"message":"Merge conflict in a.go"}}`)
	})

	// when
	// ... the pull request is merged
	err := c.Merge(context.Background(), 7, pr.Squash)

	// then
	// ... the error starts with Bitbucket's reason
	if err == nil || !strings.HasPrefix(err.Error(), "Merge conflict in a.go") {
		t.Fatalf("got %v", err)
	}
}

func TestMergeWhoseTaskStaysPendingFailsAtTheWaitCap(t *testing.T) {
	// given
	// ... a server whose merge task never leaves PENDING, and a client that waits 50ms
	c, _ := mergeTask(t, math.MaxInt, 50*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the task never finishes")
	})

	// when
	// ... the pull request is merged with a context that never ends
	err := c.Merge(context.Background(), 7, pr.MergeCommit)

	// then
	// ... it fails with the deadline, saying the merge is still pending
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("got %v", err)
	}
}

func TestMergeDoesNotPollATaskOnAnotherHost(t *testing.T) {
	// given
	// ... a server that queues the merge with a Location on a second server
	var elsewhere []string
	other := serve(t, func(w http.ResponseWriter, r *http.Request) {
		elsewhere = append(elsewhere, r.Method+" "+r.URL.Path)
	})
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", other.URL+taskPath)
		w.WriteHeader(http.StatusAccepted)
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{PollInterval: time.Millisecond})

	// when
	// ... the pull request is merged
	err := c.Merge(context.Background(), 7, pr.MergeCommit)

	// then
	// ... it fails and the second server receives nothing
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("got %v", err)
	}
	if len(elsewhere) != 0 {
		t.Errorf("want no requests to the other host, got %v", elsewhere)
	}
}

func TestCloseDeclinesThePullRequest(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... the pull request is closed
	err := c.Close(context.Background(), 7)

	// then
	// ... the request is a POST to decline
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{{Method: "POST", Path: prPath + "/decline"}}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestReopenFailsNamingReopeningWithoutARequest(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... the pull request is reopened
	err := c.Reopen(context.Background(), 7)

	// then
	// ... it fails naming reopening and sends nothing
	if err == nil || !strings.Contains(err.Error(), "reopen") {
		t.Fatalf("got %v", err)
	}
	if len(*sent) != 0 {
		t.Errorf("want no requests, got %+v", *sent)
	}
}

func TestMarkReadyPutsDraftFalse(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... the pull request is marked ready
	err := c.MarkReady(context.Background(), 7)

	// then
	// ... it puts draft false on the pull request
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{{Method: "PUT", Path: prPath, ContentType: "application/json", Body: map[string]any{"draft": false}}}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func TestConvertToDraftPutsDraftTrue(t *testing.T) {
	// given
	// ... a server that records requests
	c, sent := record(t, accept)

	// when
	// ... the pull request is converted to a draft
	err := c.ConvertToDraft(context.Background(), 7)

	// then
	// ... it puts draft true on the pull request
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRequest{{Method: "PUT", Path: prPath, ContentType: "application/json", Body: map[string]any{"draft": true}}}
	if !reflect.DeepEqual(*sent, want) {
		t.Errorf("want %+v, got %+v", want, *sent)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// pullRequestFrom is pull request 7 from branch of source into main of
// acme/shop.
func pullRequestFrom(source, branch string) string {
	return fmt.Sprintf(`{"id":7,"title":"t","state":"OPEN","author":{"nickname":"dan"},
		"source":{"branch":{"name":%q},"repository":{"full_name":%q}},
		"destination":{"branch":{"name":"main"},"repository":{"full_name":"acme/shop"}}}`, branch, source)
}

// cloneWithBranch is a clone on main whose remote "upstream" is a bare
// repository with main and branch at the same commit, and a client whose pull
// request 7 comes from branch.
func cloneWithBranch(t *testing.T, branch string) (*bitbucket.Client, string) {
	t.Helper()
	remote := t.TempDir()
	git(t, remote, "init", "--bare", "-b", "main")
	clone := t.TempDir()
	git(t, clone, "init", "-b", "main")
	git(t, clone, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "base")
	git(t, clone, "remote", "add", "upstream", remote)
	git(t, clone, "push", "upstream", "main", "main:"+branch)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, pullRequestFrom("acme/shop", branch))
	})
	return bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{Remote: "upstream", Dir: clone}), clone
}

// pushCommitOnto pushes a new empty commit with parent onto upstream's
// branch, without touching the clone's branches, and returns its hash.
func pushCommitOnto(t *testing.T, clone, parent, branch string) string {
	t.Helper()
	sha := git(t, clone, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit-tree", "HEAD^{tree}", "-p", parent, "-m", "remote work")
	git(t, clone, "push", "upstream", sha+":refs/heads/"+branch)
	return sha
}

func TestCheckoutSwitchesToTheSourceBranch(t *testing.T) {
	// given
	// ... a clone whose remote "upstream" has branch feat/basket, and a server with pull request 7 from it
	c, clone := cloneWithBranch(t, "feat/basket")

	// when
	// ... pull request 7 is checked out
	err := c.Checkout(context.Background(), 7)

	// then
	// ... the clone is on feat/basket, tracking upstream
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, clone, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat/basket" {
		t.Errorf("want branch feat/basket, got %q", got)
	}
	if got := git(t, clone, "rev-parse", "--abbrev-ref", "@{upstream}"); got != "upstream/feat/basket" {
		t.Errorf("want upstream upstream/feat/basket, got %q", got)
	}
}

func TestCheckoutAgainFastForwardsTheLocalBranch(t *testing.T) {
	// given
	// ... pull request 7 checked out once, then a new commit on upstream's feat/basket, and the clone back on main
	c, clone := cloneWithBranch(t, "feat/basket")
	if err := c.Checkout(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	next := pushCommitOnto(t, clone, "feat/basket", "feat/basket")
	git(t, clone, "switch", "main")

	// when
	// ... pull request 7 is checked out again
	err := c.Checkout(context.Background(), 7)

	// then
	// ... the clone is on feat/basket at the new commit
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, clone, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat/basket" {
		t.Errorf("want branch feat/basket, got %q", got)
	}
	if got := git(t, clone, "rev-parse", "HEAD"); got != next {
		t.Errorf("want HEAD at %s, got %s", next, got)
	}
}

func TestCheckoutOfADivergedLocalBranchFailsWithoutMovingHead(t *testing.T) {
	// given
	// ... a local feat/basket with a commit upstream lacks, upstream's with one the clone lacks, and the clone on main
	c, clone := cloneWithBranch(t, "feat/basket")
	if err := c.Checkout(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "local work")
	pushCommitOnto(t, clone, "main", "feat/basket")
	git(t, clone, "switch", "main")

	// when
	// ... pull request 7 is checked out again
	err := c.Checkout(context.Background(), 7)

	// then
	// ... it fails and the clone is still on main
	if err == nil {
		t.Fatal("want an error for a diverged branch")
	}
	if got := git(t, clone, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("want HEAD still on main, got %q", got)
	}
}

func TestCheckoutRefusesABranchNameGitWouldReadAsAPattern(t *testing.T) {
	// given
	// ... upstream with branch featx the clone has no tracking ref for, and pull request 7 from a branch named feat*
	_, clone := cloneWithBranch(t, "featx")
	git(t, clone, "update-ref", "-d", "refs/remotes/upstream/featx")
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, pullRequestFrom("acme/shop", "feat*"))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{Remote: "upstream", Dir: clone})
	before := git(t, clone, "for-each-ref", "refs/remotes")

	// when
	// ... pull request 7 is checked out
	err := c.Checkout(context.Background(), 7)

	// then
	// ... it fails naming the branch, and no remote-tracking ref changed
	if err == nil || !strings.Contains(err.Error(), `"feat*"`) {
		t.Fatalf("got %v", err)
	}
	if got := git(t, clone, "for-each-ref", "refs/remotes"); got != before {
		t.Errorf("want remote-tracking refs unchanged, got %q", got)
	}
}

func TestCheckoutOfAPullRequestFromAForkFailsNamingForks(t *testing.T) {
	// given
	// ... a server with pull request 7 from a fork, and a directory that is not a clone
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, pullRequestFrom("someone/shop", "feat/basket"))
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo, bitbucket.Options{Remote: "origin", Dir: t.TempDir()})

	// when
	// ... pull request 7 is checked out
	err := c.Checkout(context.Background(), 7)

	// then
	// ... it fails naming forks
	if err == nil || !strings.Contains(err.Error(), "fork") {
		t.Fatalf("got %v", err)
	}
}

func TestOpenInBrowserOpensThePullRequestOnBitbucket(t *testing.T) {
	// given
	// ... an opener that records the URL
	var opened []string
	open := func(u string) error { opened = append(opened, u); return nil }
	c := bitbucket.NewClient("http://127.0.0.1:0", "e", "t", repo, bitbucket.Options{Open: open})

	// when
	// ... pull request 7 is opened in the browser
	err := c.OpenInBrowser(context.Background(), 7)

	// then
	// ... the pull request's page on bitbucket.org is opened
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://bitbucket.org/acme/shop/pull-requests/7"}
	if !slices.Equal(opened, want) {
		t.Errorf("want %v, got %v", want, opened)
	}
}
