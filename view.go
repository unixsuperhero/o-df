package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/unixsuperhero/ochazuke"
)

// The culprits page: a status line (the disk, the marked total, what is
// loading), the culprit list on the left and the focused culprit's detail
// on the right (stacked below stackWidth columns). The divider follows the
// page's pane keys (> < =; Runtime.PaneLayout).

const stackWidth = 80

func (a *App) render(r *ochazuke.Runtime, width, height int) ochazuke.Block {
	s := ochazuke.NewStyles(*a.m.Appearance)
	l := r.PaneLayout(pageID, ochazuke.Layout{Width: width, Height: height, Styles: s})
	status := s.Muted.Render(ansi.Truncate(a.statusLine(), width, "…"))
	bh := max(0, height-1)
	if bh < 6 || width < 20 {
		return ochazuke.Text(status)
	}
	title := fmt.Sprintf("Culprits %d", a.list.Len())
	panel := func(w, h int) ochazuke.Block {
		cw, ch := ochazuke.PanelContentSize(w, h)
		return l.PanelBlock(title, ochazuke.Text(a.list.View(cw, ch)), w, h, a.list.Focused())
	}
	detail := func(w, h int) ochazuke.Block {
		cw, _ := ochazuke.PanelContentSize(w, h)
		return l.PanelBlock("Detail", ochazuke.Text(ansi.Hardwrap(a.detailText(), max(1, cw), true)), w, h, false)
	}
	if width < stackWidth {
		lh := l.Divide(bh*3/5, bh, ochazuke.PaneMinHeight)
		return ochazuke.Text(status + "\n" + panel(width, lh).Text + "\n" + detail(width, bh-lh).Text)
	}
	lw := l.Divide(width*3/5, width-1, ochazuke.PaneMinWidth)
	left, right := strings.Split(panel(lw, bh).Text, "\n"), strings.Split(detail(width-lw-1, bh).Text, "\n")
	rows := make([]string, bh)
	for i := range rows {
		rows[i] = pad(at(left, i), lw) + " " + at(right, i)
	}
	return ochazuke.Text(status + "\n" + strings.Join(rows, "\n"))
}

func at(rows []string, i int) string {
	if i < len(rows) {
		return rows[i]
	}
	return ""
}

func pad(line string, width int) string {
	return line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line)))
}

func (a *App) statusLine() string {
	var parts []string
	if a.disk.Size > 0 {
		parts = append(parts, diskLine(a.disk))
	}
	var total int64
	for _, r := range a.reports {
		total += r.Bytes
	}
	parts = append(parts, "culprits "+Human(total))
	if marked := a.list.Marked(); len(marked) > 0 {
		var m int64
		for _, it := range marked {
			if rep, ok := it.Data.(Report); ok {
				m += rep.Bytes
			}
		}
		parts = append(parts, fmt.Sprintf("%d marked %s", len(marked), Human(m)))
	}
	if a.loading != "" {
		parts = append(parts, a.loading+"…")
	}
	if a.note != "" {
		parts = append(parts, a.note)
	}
	if a.listErr != nil {
		parts = append(parts, "error: "+a.listErr.Error())
	}
	return strings.Join(parts, "  ·  ")
}

// detailText describes the focused culprit.
func (a *App) detailText() string {
	item, ok := a.list.Selected()
	if !ok {
		if a.loading != "" {
			return "Sizing the culprits…"
		}
		return "No culprit is present. o-df culprit list --all lists every row."
	}
	r, _ := item.Data.(Report)
	return describe(a.Env, r, a.Now())
}

// describe is a culprit's detail: its row and every path it expands to.
func describe(env Env, r Report, now time.Time) string {
	var b strings.Builder
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%-11s %s\n", k, v)
		}
	}
	line("culprit", r.Name)
	line("what", r.Title)
	line("category", r.Category)
	line("size", fmt.Sprintf("%s in %d files", Human(r.Bytes), r.Files))
	if r.Errors > 0 {
		line("unreadable", fmt.Sprintf("%d entries (the size is a lower bound)", r.Errors))
	}
	line("size error", r.SizeErr)
	if !r.LastUsed.IsZero() {
		line("last used", r.LastUsed.Format("2006-01-02 15:04")+" ("+Ago(now, r.LastUsed)+" ago)")
	}
	rb := "no: clear refuses; relocate moves it"
	if r.Rebuildable {
		rb = "yes"
	}
	line("rebuildable", rb)
	line("rebuild", r.Rebuild)
	line("advice", r.Recommend)
	line("notes", r.Notes)
	for _, cmd := range r.Clear {
		line("clear runs", strings.Join(cmd, " "))
	}
	line("source", env.Tilde(r.Source))
	b.WriteString("\npaths\n")
	for _, f := range r.Found {
		switch f.State {
		case "present":
			size := Human(f.Usage.Bytes)
			if f.Timeout {
				size = "timeout"
			}
			fmt.Fprintf(&b, "  %7s  %s\n", size, env.Tilde(f.Path))
		case "symlink":
			fmt.Fprintf(&b, "  %7s  %s → %s\n", "link", env.Tilde(f.Path), f.Target)
		case "external":
			fmt.Fprintf(&b, "  %7s  %s → %s (another volume; external: true reads it)\n", "skipped", env.Tilde(f.Path), f.Target)
		default:
			fmt.Fprintf(&b, "  %7s  %s\n", "absent", env.Tilde(f.Path))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
