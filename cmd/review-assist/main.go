// review-assist is a terminal UI for reviewing pull requests on a code host,
// with optional read-only AI review agents on a Messages API endpoint or
// Claude Code.
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
