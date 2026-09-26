package gitrepo

import "testing"

// SetBitbucketURL points the token helper's scope at url for one test.
func SetBitbucketURL(t *testing.T, url string) {
	old := bitbucketURL
	bitbucketURL = url
	t.Cleanup(func() { bitbucketURL = old })
}
