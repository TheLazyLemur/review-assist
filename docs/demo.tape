# Records docs/demo.gif. Run from the repo root: vhs docs/demo.tape
# Needs gh logged in, an agent backend configured, and an open PR titled
# "... shorten PR titles ...". It posts real comments on that PR.

Output docs/demo.gif

Set Shell bash
Set FontSize 14
Set Width 1400
Set Height 800
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 50ms

Hide
Type `bin="${TMPDIR:-/tmp}/review-assist-demo" && go build -o "$bin/review-assist" ./cmd/review-assist && export PATH="$bin:$PATH" && clear`
Enter
Wait
Show

Type "review-assist"
Sleep 300ms
Enter
Wait+Screen@20s /shorten PR titles/
Sleep 2s

# open the PR
Enter
Wait+Screen@20s /Overview/
Sleep 3s

# diff: comment on a line
Type "2"
Sleep 2s
Type@300ms "jjjjjjj"
Sleep 1s
Type "c"
Sleep 1s
Type "Slicing bytes can split a multi-byte rune here."
Sleep 1s
Ctrl+S
Sleep 4s

# standard agent review
Type "3"
Sleep 2s
Type "A"
Sleep 1.5s
Type "2"
Sleep 6s

# the run takes minutes; skip the wait
Hide
Wait+Screen@10m /Assessment:|No suggestions|review failed/
Show
Sleep 4s

# post the first finding's drafted comment
Type "c"
Sleep 3s
Ctrl+S
Sleep 4s

# request changes
Type "x"
Sleep 1.5s
Type "Please fix the title slicing before this lands."
Sleep 1s
Ctrl+S
Sleep 4s

# the posted comments show inline in the diff
Type "r"
Sleep 2s
Type "2"
Sleep 5s
