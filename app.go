package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/unixsuperhero/ochazuke"
	ocli "github.com/unixsuperhero/ochazuke/cli"
)

// The application: one ochazuke runtime holding the culprit list and the
// culprit noun's verbs, used by the command line (the shell runs one verb
// and settles it) and by the TUI. What o-df read from the disk is the
// list's rows; the runtime holds interaction state only.

const (
	pageID     = "df"
	culpritsID = "df/culprits"
)

// Version is the catalog's version.
const Version = "0.1.0"

var culpritColumns = []ochazuke.Column{
	{Title: "culprit", Min: 12},
	{Title: "size", Align: ochazuke.AlignRight, Priority: 1},
	{Title: "rebuild", Priority: 2},
	{Title: "used", Align: ochazuke.AlignRight, Priority: 3},
	{Title: "advice", Priority: 4},
	{Title: "path", Priority: 5},
}

// App is o-df's state.
type App struct {
	r    *ochazuke.Runtime
	m    *ochazuke.Model
	list *ochazuke.List

	Env Env
	Cfg Config
	Now func() time.Time

	reports []Report
	disk    Disk
	loading string
	note    string // the last change's outcome, shown on the status line
	listErr error
	tui     bool

	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// New builds the runtime: the page holding the culprit list, the noun
// with its columns, verbs and bindings, and the model.
func New(env Env) (*App, error) {
	cfg, err := Load(env)
	if err != nil {
		return nil, err
	}
	r := ochazuke.NewRuntime()
	env.External = env.External || cfg.External
	a := &App{r: r, Env: env, Cfg: cfg, Now: time.Now, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	r.Instance(pageID, ochazuke.Kind("df"))
	if a.list, err = r.AddList(culpritsID, nil, ochazuke.Parent(pageID), ochazuke.Columns(culpritColumns...)); err != nil {
		return nil, err
	}
	r.Define("culprit").Columns(culpritColumns...)
	a.registerVerbs()
	a.m = ochazuke.NewModel(r)
	a.m.Title = "o-df"
	if err := a.bind(); err != nil {
		return nil, err
	}
	// Declared as a data page so a host's catalog can draw the list; the
	// TUI draws it itself (render).
	if err := ocli.DeclarePage(r, ocli.CatalogPage{
		ID: pageID, Title: "Disk", Layout: ocli.LayoutSplit,
		Lists:  []ocli.CatalogList{{ID: culpritsID, Noun: "culprit", Title: "Culprits"}},
		Detail: &ocli.CatalogDetail{Fields: []string{"name", "title", "bytes", "files", "last_used", "rebuildable", "rebuild", "recommend", "notes", "paths", "found"}},
	}); err != nil {
		return nil, err
	}
	page := r.PageOf(pageID)
	page.Render, page.Focus, page.Remembered = a.render, culpritsID, culpritsID
	if err := r.OpenPage(pageID); err != nil {
		return nil, err
	}
	return a, nil
}

// Shell is the NOUN VERB command line over the runtime.
func (a *App) Shell() *ocli.Shell {
	sh := ocli.New(a.r, "o-df", a.Stdin, a.Stdout, a.Stderr)
	sh.Usage = ocli.DefaultUsage("o-df")
	sh.Version = Version
	return sh
}

func (a *App) registerVerbs() {
	r := a.r
	r.Define("culprit").
		Verb("list", a.culpritList, "every culprit with its size, biggest first: rows {noun, id: culprit/NAME, label, data: report, cells}; --all includes the absent ones").Rows("culprit").Fetch().Quiet().
		Verb("show", a.culpritShow, "one culprit's row (paths, rebuildable, clear commands, advice) and every path it expands to, sized").Fetch().Quiet().Positional("name").
		Verb("size", a.culpritSize, "one culprit's size: {name, bytes, human, files, last_used}").Fetch().Quiet().Positional("name").
		Verb("clear", a.culpritClear, "empty a rebuildable culprit (its clear commands, else its paths' contents): {name, before, after, freed}").Confirm("Clear {{label}}? Its tool rebuilds it on demand.").Positional("name").
		Verb("relocate", a.culpritRelocate, "copy a culprit's paths to TO/<path under home>, verify, delete the source; --link leaves a symlink: {name, dir, moved, freed}").Confirm("Relocate {{label}} (copy, verify, delete)?").Positional("name")
	setHistory(r, ochazuke.HistoryChange, "culprit.clear", "culprit.relocate")
	name := ochazuke.Parameter{Type: "string", Description: "a culprit name (o-df culprit list; default: the focused row's)"}
	set := func(key, result string, params map[string]ochazuke.Parameter) {
		op := r.Operations[r.CanonicalKey(key)]
		op.Parameters, op.Result = params, result
		r.Operations[r.CanonicalKey(key)] = op
	}
	set("culprit.list", "row[]", map[string]ochazuke.Parameter{
		"all":      {Type: "boolean", Description: "include culprits none of whose paths exist"},
		"external": {Type: "boolean", Description: "also size paths that resolve onto another volume"},
	})
	set("culprit.show", "report", map[string]ochazuke.Parameter{"name": name})
	set("culprit.size", "{name, bytes, human, files, last_used}", map[string]ochazuke.Parameter{"name": name})
	set("culprit.clear", "{name, before, after, freed}", map[string]ochazuke.Parameter{"name": name})
	set("culprit.relocate", "{name, dir, moved, freed}", map[string]ochazuke.Parameter{
		"name":        name,
		"to":          {Type: "string", Description: "the destination root (default: relocate_to in the culprits file)"},
		"link":        {Type: "boolean", Description: "leave a symlink at each old path"},
		"same-volume": {Type: "boolean", Description: "allow a destination on the source's volume (frees nothing)"},
	})
}

// bind installs the key rows: d d clears the marked rows (the focused one
// when none is marked), m relocates them to relocate_to, R reloads, q
// quits. Space marks (the list noun's row); j/k move.
func (a *App) bind() error {
	r := a.r
	r.Define("df").Mode("normal").
		Bind("R", ochazuke.Do("culprit.list")).
		Bind("q", ochazuke.Do("app.quit"))
	r.Define("culprit").Mode("normal").
		Bind("d d", ochazuke.Do("list.apply", ochazuke.Arguments{"verb": "clear"}).Hinted("clear")).
		Bind("m", ochazuke.Do("list.apply", ochazuke.Arguments{"verb": "relocate", "args": map[string]any{"to": a.Cfg.RelocateTo}}).Hinted("relocate"))
	return nil
}

// culprit is the one a verb acts on: --name, else the targeted row's.
func (a *App) culprit(c ochazuke.Ctx) (Culprit, error) {
	if n := ochazuke.StringArg(c.Args, "name", ""); n != "" {
		return a.Cfg.Find(n)
	}
	if c.Instance != nil {
		if rep, ok := c.Instance.Data.(Report); ok {
			return rep.Culprit, nil
		}
	}
	return Culprit{}, errors.New("a culprit is required: o-df culprit VERB NAME (o-df culprit list names them)")
}

func (a *App) culpritList(c ochazuke.Ctx) ochazuke.Result {
	all, _ := c.Args["all"].(bool)
	if ext, _ := c.Args["external"].(bool); ext {
		a.Env.External = true
	}
	a.loading = "sizing culprits"
	var disk Disk // read with the rows, used by Then (after the work)
	return ochazuke.Pending(func(ctx context.Context) (any, error) {
		reps := ProbeAll(ctx, a.Env, a.Cfg.Culprits)
		if !all {
			reps = presentOnly(reps)
		}
		disk, _ = DiskOf(a.Env.Home)
		return a.rows(reps), ctx.Err()
	}).Replacing("list").Then(func(_ ochazuke.Ctx, v any, err error) {
		a.loading, a.listErr = "", err
		if err != nil {
			return
		}
		rows := v.([]ocli.Row)
		a.disk, a.reports = disk, make([]Report, len(rows))
		items := make([]ochazuke.ListItem, len(rows))
		for i, r := range rows {
			a.reports[i] = r.Data.(Report)
			items[i] = ochazuke.ListItem{ID: r.ID, Label: r.Label, Noun: r.Noun, Data: r.Data, Cells: r.Cells}
		}
		_ = a.list.SetItems(items)
	})
}

func presentOnly(reps []Report) []Report {
	out := reps[:0:0]
	for _, r := range reps {
		if r.Present {
			out = append(out, r)
		}
	}
	return out
}

func (a *App) culpritShow(c ochazuke.Ctx) ochazuke.Result {
	cu, err := a.culprit(c)
	if err != nil {
		return ochazuke.Failed(err)
	}
	return ochazuke.Pending(func(ctx context.Context) (any, error) { return Probe(ctx, a.Env, cu), nil })
}

// SizeResult is culprit size's answer.
type SizeResult struct {
	Name     string    `json:"name"`
	Bytes    int64     `json:"bytes"`
	Human    string    `json:"human"`
	Files    int64     `json:"files"`
	LastUsed time.Time `json:"last_used,omitzero"`
}

func (a *App) culpritSize(c ochazuke.Ctx) ochazuke.Result {
	cu, err := a.culprit(c)
	if err != nil {
		return ochazuke.Failed(err)
	}
	return ochazuke.Pending(func(ctx context.Context) (any, error) {
		r := Probe(ctx, a.Env, cu)
		return SizeResult{Name: r.Name, Bytes: r.Bytes, Human: Human(r.Bytes), Files: r.Files, LastUsed: r.LastUsed}, nil
	})
}

func (a *App) culpritClear(c ochazuke.Ctx) ochazuke.Result {
	cu, err := a.culprit(c)
	if err != nil {
		return ochazuke.Failed(err)
	}
	if !cu.Rebuildable {
		return ochazuke.Failed(fmt.Errorf("%s is not rebuildable; o-df clears only rebuildable culprits (relocate moves it instead)", cu.Name))
	}
	return ochazuke.Pending(func(ctx context.Context) (any, error) {
		return Clear(ctx, a.Env, cu)
	}).Then(a.changed(func(v any) string {
		res := v.(ClearResult)
		return "cleared " + res.Name + ", freed " + Human(res.Freed)
	}))
}

func (a *App) culpritRelocate(c ochazuke.Ctx) ochazuke.Result {
	cu, err := a.culprit(c)
	if err != nil {
		return ochazuke.Failed(err)
	}
	o := RelocateOptions{To: ochazuke.StringArg(c.Args, "to", a.Cfg.RelocateTo)}
	o.Link, _ = c.Args["link"].(bool)
	o.SameVolume, _ = c.Args["same-volume"].(bool)
	return ochazuke.Pending(func(ctx context.Context) (any, error) {
		return Relocate(ctx, a.Env, cu, o)
	}).Then(a.changed(func(v any) string {
		res := v.(RelocateResult)
		return "relocated " + res.Name + " to " + a.Env.Tilde(res.Dir) + ", freed " + Human(res.Freed)
	}))
}

// changed reports a change on the TUI and reads the sizes again.
func (a *App) changed(say func(any) string) func(ochazuke.Ctx, any, error) {
	return func(ctx ochazuke.Ctx, v any, err error) {
		if !a.tui {
			return
		}
		if err != nil {
			a.note = "error: " + err.Error()
		} else {
			a.note = say(v)
			ctx.Info(a.note)
		}
		a.refresh()
	}
}

func (a *App) refresh() {
	_, err := a.r.Launch(culpritsID, "culprit.list", a.culpritList(ochazuke.Ctx{Args: ochazuke.Arguments{}}), ochazuke.Ctx{Runtime: a.r, Context: a.r.Context(), Args: ochazuke.Arguments{}, Instance: a.r.Instances[culpritsID]})
	if err != nil {
		a.r.UI().Status(err.Error())
	}
}

// Rows: id culprit/NAME, label NAME, one cell per column.

func (a *App) cells(r Report) []string {
	rb := "no"
	if r.Rebuildable {
		rb = "yes"
	}
	size := Human(r.Bytes)
	if r.Errors > 0 {
		size = "≥" + size
	}
	for _, f := range r.Found {
		if f.Timeout {
			size = "timeout"
		}
	}
	return []string{r.Name, size, rb, Ago(a.Now(), r.LastUsed), r.Recommend, a.pathText(r)}
}

func (a *App) pathText(r Report) string {
	var parts []string
	for _, f := range r.Found {
		switch f.State {
		case "present":
			parts = append(parts, a.Env.Tilde(f.Path))
		case "symlink":
			parts = append(parts, a.Env.Tilde(f.Path)+" → "+f.Target)
		case "external":
			parts = append(parts, a.Env.Tilde(f.Path)+" (other volume, skipped)")
		}
	}
	if len(parts) == 0 && len(r.Found) > 0 {
		return a.Env.Tilde(r.Found[0].Path) + " (absent)"
	}
	return strings.Join(parts, ", ")
}

func (a *App) items(reps []Report) []ochazuke.ListItem {
	out := make([]ochazuke.ListItem, len(reps))
	for i, r := range reps {
		out[i] = ochazuke.ListItem{ID: "culprit/" + r.Name, Label: r.Name, Noun: "culprit", Data: r, Cells: a.cells(r)}
	}
	return out
}

func (a *App) rows(reps []Report) []ocli.Row {
	out := make([]ocli.Row, len(reps))
	for i, it := range a.items(reps) {
		out[i] = ocli.Row{Noun: it.Noun, ID: it.ID, Label: it.Label, Data: it.Data, Cells: it.Cells}
	}
	return out
}

// setHistory declares the history kind of each named verb (from o-git).
func setHistory(r *ochazuke.Runtime, kind string, keys ...string) {
	for _, key := range keys {
		op, ok := r.Operations[r.CanonicalKey(key)]
		if !ok {
			panic("setHistory: no operation " + key)
		}
		op.HistoryKind = kind
		r.Operations[r.CanonicalKey(key)] = op
	}
}
