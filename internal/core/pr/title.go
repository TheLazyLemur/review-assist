package pr

// ShortTitle cuts a PR title to fit a list column of width max.
func ShortTitle(title string, max int) string {
	if len(title) < max {
		return title
	}
	return title[:max] + "…"
}
