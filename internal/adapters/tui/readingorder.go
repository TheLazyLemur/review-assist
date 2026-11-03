package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

// readingOrder lives on the open PR and goes when it closes.
type readingOrder struct {
	cancel   context.CancelFunc
	ch       chan tea.Msg
	started  time.Time
	running  bool
	progress string // the last file judged or tool called
	steps    []review.ReadingStep
	shown    bool
	unseen   bool // arrived while another tab was open
}

// The messages carry their order, so a cancelled or replaced order is ignored.
type readingOrderEventMsg struct {
	order  *readingOrder
	detail string
}

type readingOrderMsg struct {
	order *readingOrder
	res   review.ReadingOrder
	err   error
}

// readingOrderKey starts a reading order, cancels a running one, or toggles
// between it and path order without asking again.
func (m *Model) readingOrderKey() tea.Cmd {
	p := m.pr
	if o := p.order; o != nil {
		if o.running {
			o.cancel()
			p.order = nil
			return m.setStatus("reading order cancelled", false)
		}
		o.shown = !o.shown
		p.rows = nil
		if o.shown {
			return m.setStatus("reading order (O for path order)", false)
		}
		return m.setStatus("path order (O for the reading order)", false)
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := &readingOrder{cancel: cancel, ch: make(chan tea.Msg, 64), started: time.Now(), running: true}
	p.order = o
	reviews, repo, details := m.deps.Reviews, m.deps.PRs.Repo(), p.data
	go func() {
		defer close(o.ch)
		defer cancel()
		emit := func(ev review.Event) {
			if ev.Kind != review.EventTool {
				return
			}
			// Progress may drop when the screen lags behind; the result never does.
			select {
			case o.ch <- readingOrderEventMsg{o, ev.Detail}:
			default:
			}
		}
		res, err := reviews.ReadingOrder(ctx, repo, details, emit)
		o.ch <- readingOrderMsg{o, res, err}
	}()
	return tea.Batch(waitAgent(o.ch), m.setStatus("reading order running… (O cancels)", false))
}

// orderBadge marks the Diff tab while an order runs, and once it arrives
// until the tab is opened.
func (m *Model) orderBadge() string {
	switch o := m.pr.order; {
	case o == nil:
		return ""
	case o.running:
		return " " + m.spinner.View()
	case o.unseen:
		return " " + lipgloss.NewStyle().Foreground(purple).Render("●")
	}
	return ""
}

// orderRunning says how long the open PR's reading order has run, and what
// it did last; empty when none runs.
func (m *Model) orderRunning() string {
	if m.pr == nil || m.pr.order == nil || !m.pr.order.running {
		return ""
	}
	o := m.pr.order
	d := time.Since(o.started).Round(time.Second)
	s := fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
	if o.progress != "" {
		s += " · " + o.progress
	}
	return s
}

// closePR leaves the PR screen; a running reading order dies with the PR.
func (m *Model) closePR() {
	if o := m.pr.order; o != nil && o.running {
		o.cancel()
	}
	m.screen, m.pr = screenList, nil
}

func (m *Model) readingOrderDone(msg readingOrderMsg) tea.Cmd {
	p := m.pr
	if p == nil || p.order != msg.order {
		return nil
	}
	if msg.err != nil {
		p.order = nil
		return m.setStatus("reading order failed: "+msg.err.Error(), true)
	}
	o := msg.order
	o.running, o.steps, o.shown = false, msg.res.Steps, true
	o.unseen = p.tab != tabDiff
	if idx := p.fileOrder(); len(idx) > 0 {
		p.selectFile(idx[0])
	}
	status := fmt.Sprintf("reading order of %d files (O for path order)", len(o.steps))
	if len(msg.res.Notes) > 0 {
		status += ": " + strings.Join(msg.res.Notes, "; ")
	}
	return m.setStatus(status, false)
}

// fileOrder is the indices of the PR's files in the order shown: the reading
// order when it is on, else path order. Files a push added after the order
// was made come last.
func (p *prModel) fileOrder() []int {
	n := len(p.data.Files)
	if p.order == nil || !p.order.shown {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	idx := make([]int, 0, n)
	for _, s := range p.order.steps {
		if i := slices.IndexFunc(p.data.Files, func(f diff.File) bool { return f.Path() == s.Path }); i >= 0 {
			idx = append(idx, i)
		}
	}
	for i := range n {
		if !slices.Contains(idx, i) {
			idx = append(idx, i)
		}
	}
	return idx
}

// stepFile moves dir files along the order shown.
func (p *prModel) stepFile(dir int) {
	idx := p.fileOrder()
	if pos := slices.Index(idx, p.fileIdx) + dir; pos >= 0 && pos < len(idx) {
		p.selectFile(idx[pos])
	}
}

// step is the reading order's entry for path, when the order is shown.
func (p *prModel) step(path string) (review.ReadingStep, bool) {
	if p.order == nil || !p.order.shown {
		return review.ReadingStep{}, false
	}
	i := slices.IndexFunc(p.order.steps, func(s review.ReadingStep) bool { return s.Path == path })
	if i < 0 {
		return review.ReadingStep{}, false
	}
	return p.order.steps[i], true
}

var tierGlyph = map[review.Tier]string{
	review.TierRead: lipgloss.NewStyle().Foreground(red).Render("●"),
	review.TierSkim: lipgloss.NewStyle().Foreground(orange).Render("◐"),
	review.TierSkip: faintStyle.Render("·"),
}

func stepNote(s review.ReadingStep, w int) string {
	return lipgloss.NewStyle().Foreground(purple).Render("  ▸ ") + fit(tierGlyph[s.Tier]+" "+string(s.Tier)+"  "+commentStyle.Render(s.Reason), w)
}
