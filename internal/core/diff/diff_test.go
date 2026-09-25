package diff_test

import (
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
)

const sample = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,4 +1,5 @@ package main
 package main
-import "fmt"
+import (
+	"fmt"
+)
 func main() {}
@@ -10,2 +11,2 @@ func other() {
-	a := 1
+	a := 2
 	return
\ No newline at end of file
diff --git a/old name.txt b/new name.txt
similarity index 100%
rename from old name.txt
rename to new name.txt
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
index 3333333..0000000
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
diff --git a/logo.png b/logo.png
index 4444444..5555555 100644
Binary files a/logo.png and b/logo.png differ
`

func TestParseMapsEveryLineToItsCommentableSide(t *testing.T) {
	// given
	// ... a multi-file PR diff with hunks, a rename, a deletion and a binary
	input := sample

	// when
	// ... the diff is parsed
	files, err := diff.Parse(input)

	// then
	// ... every file is found with its display path and stats
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(files) != 4 {
		t.Fatalf("want 4 files, got %d", len(files))
	}
	assertEqual(t, "main.go", files[0].Path())
	assertEqual(t, 4, files[0].Additions)
	assertEqual(t, 2, files[0].Deletions)
	assertEqual(t, "new name.txt", files[1].Path())
	assertEqual(t, "old name.txt", files[1].OldPath)
	assertEqual(t, "gone.txt", files[2].Path())
	assertEqual(t, true, files[2].Deleted)
	assertEqual(t, true, files[3].Binary)

	// ... and each line maps to the side and number GitHub expects for inline comments
	h0 := files[0].Hunks[0].Lines
	assertTarget(t, h0[0], diff.Right, 1) // context "package main"
	assertTarget(t, h0[1], diff.Left, 2)  // removed import
	assertTarget(t, h0[2], diff.Right, 2) // added "import ("
	assertTarget(t, h0[4], diff.Right, 4) // added ")"
	assertTarget(t, h0[5], diff.Right, 5) // context "func main"
	h1 := files[0].Hunks[1].Lines
	assertEqual(t, 3, len(h1)) // the "\ No newline" marker is not a line
	assertTarget(t, h1[0], diff.Left, 10)
	assertTarget(t, h1[1], diff.Right, 11)
	assertTarget(t, h1[2], diff.Right, 12)
	assertTarget(t, files[2].Hunks[0].Lines[0], diff.Left, 1)
}

func assertTarget(t *testing.T, l diff.Line, side diff.Side, line int) {
	t.Helper()
	gotSide, gotLine := l.Target()
	if gotSide != side || gotLine != line {
		t.Errorf("line %q: want %s:%d, got %s:%d", l.Text, side, line, gotSide, gotLine)
	}
}

func assertEqual[T comparable](t *testing.T, want, got T) {
	t.Helper()
	if want != got {
		t.Errorf("want %v, got %v", want, got)
	}
}
