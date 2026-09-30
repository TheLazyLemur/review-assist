package gitrepo

import "testing"

// SetBitbucketURL points the token helper's scope at url for one test.
func SetBitbucketURL(t *testing.T, url string) {
	// t.Setenv panics under t.Parallel, which a package variable needs as much as the environment does.
	t.Setenv("REVIEW_ASSIST_TEST_BITBUCKET_URL", url)
	old := bitbucketURL
	bitbucketURL = url
	t.Cleanup(func() { bitbucketURL = old })
}
