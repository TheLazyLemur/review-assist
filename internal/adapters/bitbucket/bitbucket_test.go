package bitbucket_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TheLazyLemur/review-assist/internal/adapters/bitbucket"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

var repo = pr.Repo{Platform: pr.Bitbucket, Hostname: "bitbucket.org", Owner: "acme", Name: "shop"}

const listPath = "/repositories/acme/shop/pullrequests"

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func pullRequestJSON(id int, state, nickname string) string {
	return fmt.Sprintf(`{"id":%d,"title":"t%d","state":%q,"author":{"nickname":%q}}`, id, id, state, nickname)
}

func TestEveryRequestCarriesBasicAuthOfEmailAndToken(t *testing.T) {
	// given
	// ... a server that records the Authorization header
	var got string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"nickname":"dan"}`)
	})
	c := bitbucket.NewClient(srv.URL, "dan@example.com", "s3cret", repo)

	// when
	// ... the viewer is read
	_, err := c.Viewer(context.Background())

	// then
	// ... the request is authenticated with the email and API token
	if err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("dan@example.com:s3cret"))
	if got != want {
		t.Fatalf("Authorization: want %q, got %q", want, got)
	}
}

func TestUnauthorisedErrorLeadsWithBitbucketsMessage(t *testing.T) {
	// given
	// ... a server that refuses the credentials
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"error","error":{"message":"Invalid credentials"}}`)
	})
	c := bitbucket.NewClient(srv.URL, "dan@example.com", "wrong", repo)

	// when
	// ... pull requests are listed
	_, err := c.List(context.Background(), pr.Open)

	// then
	// ... the error starts with Bitbucket's message
	if err == nil || !strings.HasPrefix(err.Error(), "Invalid credentials") {
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
			c := bitbucket.NewClient(srv.URL, "e", "t", repo)

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
	c := bitbucket.NewClient(srv.URL, "e", "t", repo)

	// when
	// ... open pull requests are listed
	got, err := c.List(context.Background(), pr.Open)

	// then
	// ... its fields land in the summary
	if err != nil {
		t.Fatal(err)
	}
	want := pr.Summary{
		Number: 7, Title: "Add basket", Author: "dan", HeadRef: "feat/basket", BaseRef: "main",
		IsDraft: true, State: "OPEN", UpdatedAt: time.Date(2026, 9, 24, 10, 11, 12, 123456000, time.UTC),
	}
	if len(got) != 1 {
		t.Fatalf("want 1 summary, got %d", len(got))
	}
	if !got[0].UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("UpdatedAt: want %v, got %v", want.UpdatedAt, got[0].UpdatedAt)
	}
	got[0].UpdatedAt = want.UpdatedAt
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("want %+v, got %+v", want, got[0])
	}
}

func TestListFollowsNextAcrossPages(t *testing.T) {
	// given
	// ... pull requests split over two pages, the first linking to the second
	var pagelen string
	var srv *httptest.Server
	srv = serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprintf(w, `{"values":[%s]}`, pullRequestJSON(3, "OPEN", "dan"))
			return
		}
		pagelen = r.URL.Query().Get("pagelen")
		fmt.Fprintf(w, `{"values":[%s,%s],"next":"%s%s?state=OPEN&pagelen=50&page=2"}`,
			pullRequestJSON(1, "OPEN", "dan"), pullRequestJSON(2, "OPEN", "dan"), srv.URL, listPath)
	})
	c := bitbucket.NewClient(srv.URL, "e", "t", repo)

	// when
	// ... open pull requests are listed
	got, err := c.List(context.Background(), pr.Open)

	// then
	// ... all three come back, asked for 50 a page
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
	c := bitbucket.NewClient(srv.URL, "e", "t", repo)

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
	c := bitbucket.NewClient(srv.URL, "e", "t", repo)

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
	c := bitbucket.NewClient(srv.URL, "e", "t", repo)

	// when
	// ... the viewer and the list are read
	viewer, err := c.Viewer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prs, err := c.List(context.Background(), pr.Open)

	// then
	// ... the viewer is the pull request's author
	if err != nil {
		t.Fatal(err)
	}
	if viewer != "dan" {
		t.Errorf("viewer: want dan, got %q", viewer)
	}
	if !pr.IsOwn(prs[0].Author, viewer) {
		t.Errorf("IsOwn(%q, %q) is false", prs[0].Author, viewer)
	}
}
