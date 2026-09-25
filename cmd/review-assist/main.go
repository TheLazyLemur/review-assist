// review-assist is a terminal UI for reviewing GitHub pull requests with
// optional read-only AI review agents on any Anthropic-compatible endpoint.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "review-assist:", err)
		os.Exit(1)
	}
}
