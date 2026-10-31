package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"ofam.dev/ochazuke"
	ocli "ofam.dev/ochazuke/cli"
)

// runTUI opens the culprits page (or, with --smoke, renders one frame).
func runTUI(a *App, args []string) error {
	fs := flags("o-df tui", a.Stderr)
	smoke := fs.Bool("smoke", false, "size the culprits, render one frame to stdout and exit")
	all := fs.Bool("all", false, "list culprits none of whose paths exist too")
	width := fs.Int("width", 120, "--smoke frame width")
	height := fs.Int("height", 30, "--smoke frame height")
	if err := parse(fs, args); err != nil {
		return err
	}
	if _, err := a.r.Execute(ochazuke.Request{Noun: "culprit", Verb: "list", Target: pageID, Args: ochazuke.Arguments{"all": *all}}); err != nil {
		return err
	}
	if *smoke {
		if err := ocli.Settle(a.r, nil); err != nil {
			a.r.UI().Status(err.Error())
		}
		a.m.Width, a.m.Height = *width, *height
		_, err := fmt.Fprintln(a.Stdout, strings.TrimRight(ansi.Strip(a.m.View().Content), "\n"))
		return err
	}
	a.tui = true
	_, err := tea.NewProgram(&model{a: a}).Run()
	return err
}

type model struct{ a *App }

func (m *model) Init() tea.Cmd { return tea.Batch(m.a.m.Init(), m.a.r.PendingCommands()) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.a.m.Update(msg)
	return m, tea.Batch(cmd, m.a.r.PendingCommands())
}

func (m *model) View() tea.View { return m.a.m.View() }
