package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unixsuperhero/ochazuke"
	ocli "github.com/unixsuperhero/ochazuke/cli"
)

// Every test runs against a temp HOME with a fixture of caches; nothing
// touches the real home directory.

func fixture(t *testing.T) Env {
	t.Helper()
	home := t.TempDir()
	write := func(rel string, size int) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".npm/_cacache/a", 64<<10)
	write(".npm/_cacache/b/c", 32<<10)
	write(".npm/_npx/x/package.json", 4<<10)
	write("Library/pnpm/store/v10/f", 128<<10)
	write("Library/pnpm/global/tool", 8<<10) // not part of the store row
	write("Downloads/big.zip", 256<<10)
	write("src/one/.turbo/cache/blob", 16<<10)
	write("src/two/.turbo/cache/blob", 16<<10)
	return Env{Home: home, UserFile: filepath.Join(home, ".config/o-df/culprits.json")}
}

func userFile(t *testing.T, env Env, cfg string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(env.UserFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.UserFile, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, env Env, args ...string) (string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	err := Main(args, env, strings.NewReader("y\n"), &out, &errb)
	return out.String() + errb.String(), err
}

func TestDefaultsLoadAndValidate(t *testing.T) {
	cfg, err := Load(Env{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Culprits) < 20 || cfg.RelocateTo == "" {
		t.Fatalf("defaults: %d rows, relocate_to %q", len(cfg.Culprits), cfg.RelocateTo)
	}
	for _, c := range cfg.Culprits {
		if c.Recommend != "delete" && c.Recommend != "relocate" && c.Recommend != "keep" {
			t.Errorf("%s: advice %q is not delete, relocate or keep", c.Name, c.Recommend)
		}
	}
}

func TestUserFileAddsReplacesAndDisables(t *testing.T) {
	env := fixture(t)
	userFile(t, env, `{"relocate_to": "/elsewhere", "culprits": [
		{"name": "mine", "paths": ["~/Downloads"], "rebuildable": true, "recommend": "delete"},
		{"name": "downloads", "paths": ["~/Downloads"], "rebuildable": false, "recommend": "keep", "notes": "mine now"},
		{"name": "trash", "paths": ["~/.Trash"], "disabled": true}
	]}`)
	cfg, err := Load(env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RelocateTo != "/elsewhere" {
		t.Errorf("relocate_to %q", cfg.RelocateTo)
	}
	if c, err := cfg.Find("mine"); err != nil || c.Source != env.UserFile {
		t.Errorf("mine: %v %+v", err, c)
	}
	if c, _ := cfg.Find("downloads"); c.Notes != "mine now" {
		t.Errorf("downloads not replaced: %+v", c)
	}
	if _, err := cfg.Find("trash"); err == nil {
		t.Error("trash is disabled but listed")
	}
}

func TestUserFileRejectsUnknownFields(t *testing.T) {
	env := fixture(t)
	userFile(t, env, `{"culprits": [{"name": "x", "paths": ["~/x"], "sise": 1}]}`)
	if _, err := Load(env); err == nil {
		t.Fatal("a misspelt field loaded")
	}
}

func TestProbeSizesGlobsAndAbsent(t *testing.T) {
	env := fixture(t)
	cfg, _ := Load(env)
	turbo, _ := cfg.Find("turbo-cache")
	r := Probe(context.Background(), env, turbo)
	if !r.Present || r.Files != 2 || r.Bytes < 32<<10 {
		t.Fatalf("turbo: %+v", r)
	}
	npm, _ := cfg.Find("npm-cache")
	r = Probe(context.Background(), env, npm)
	if r.Bytes < 100<<10 || r.Files != 3 {
		t.Fatalf("npm: bytes %d files %d", r.Bytes, r.Files)
	}
	bun, _ := cfg.Find("bun-cache")
	if r := Probe(context.Background(), env, bun); r.Present || r.Found[0].State != "absent" {
		t.Fatalf("bun: %+v", r)
	}
}

func TestProbeShowsSymlinkAsRelocated(t *testing.T) {
	env := fixture(t)
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(env.Home, "Library/Caches"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(env.Home, "Library/Caches/go-build")); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load(env)
	c, _ := cfg.Find("go-build-cache")
	r := Probe(context.Background(), env, c)
	if r.Present || r.Found[0].State != "symlink" || r.Found[0].Target != target {
		t.Fatalf("%+v", r.Found)
	}
}

func TestScanJSONSortsBySizeAndHidesAbsent(t *testing.T) {
	env := fixture(t)
	out, err := run(t, env, "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	var res ScanResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err, out)
	}
	var names []string
	for _, r := range res.Culprits {
		names = append(names, r.Name)
	}
	want := []string{"downloads", "pnpm-store", "npm-cache", "turbo-cache"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("culprits %v, want %v", names, want)
	}
	if res.Disk.Size == 0 {
		t.Error("no disk totals")
	}
}

func TestScanTable(t *testing.T) {
	env := fixture(t)
	out, err := run(t, env, "scan")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CULPRIT", "pnpm-store", "~/Library/pnpm/store", "total"} {
		if !strings.Contains(out, want) {
			t.Errorf("scan lacks %q:\n%s", want, out)
		}
	}
}

func TestClearEmptiesRebuildablePathsOnly(t *testing.T) {
	env := fixture(t)
	out, err := run(t, env, "culprit", "clear", "pnpm-store", "--yes")
	if err != nil {
		t.Fatal(err, out)
	}
	if !strings.Contains(out, `"freed"`) {
		t.Errorf("no freed in %s", out)
	}
	names, _ := readNames(filepath.Join(env.Home, "Library/pnpm/store"))
	if len(names) != 0 {
		t.Errorf("store still holds %v", names)
	}
	if _, err := os.Stat(filepath.Join(env.Home, "Library/pnpm/global/tool")); err != nil {
		t.Errorf("global tools were touched: %v", err)
	}
}

func TestClearRefusesNotRebuildable(t *testing.T) {
	env := fixture(t)
	if out, err := run(t, env, "culprit", "clear", "downloads", "--yes"); err == nil {
		t.Fatalf("cleared downloads: %s", out)
	}
	if _, err := os.Stat(filepath.Join(env.Home, "Downloads/big.zip")); err != nil {
		t.Fatal(err)
	}
}

func TestClearAsksWithoutYes(t *testing.T) {
	env := fixture(t)
	var out, errb bytes.Buffer
	err := Main([]string{"culprit", "clear", "npm-cache"}, env, strings.NewReader("n\n"), &out, &errb)
	if err == nil {
		t.Logf("answered no: %s %s", out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Join(env.Home, ".npm/_cacache/a")); err != nil {
		t.Fatalf("cleared after answering no: %v", err)
	}
}

func TestClearCommandsRunPerPath(t *testing.T) {
	env := fixture(t)
	log := filepath.Join(env.Home, "ran.log")
	userFile(t, env, `{"culprits": [{"name": "npm-cache", "paths": ["~/.npm/_cacache", "~/.npm/_npx"], "rebuildable": true,
		"clear": [["sh", "-c", "echo $0 >> `+log+`", "{path}"]]}]}`)
	if out, err := run(t, env, "culprit", "clear", "npm-cache", "--yes"); err != nil {
		t.Fatal(err, out)
	}
	data, _ := os.ReadFile(log)
	if got := strings.Count(string(data), "\n"); got != 2 {
		t.Fatalf("ran %d times: %s", got, data)
	}
}

func TestGuardRefusesHome(t *testing.T) {
	env := fixture(t)
	userFile(t, env, `{"culprits": [{"name": "home", "paths": ["~"], "rebuildable": true}]}`)
	if out, err := run(t, env, "culprit", "clear", "home", "--yes"); err == nil || !strings.Contains(out+err.Error(), "home directory") {
		t.Fatalf("cleared the home: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(env.Home, "Downloads/big.zip")); err != nil {
		t.Fatal(err)
	}
}

func TestRelocateCopiesVerifiesDeletesAndLinks(t *testing.T) {
	env := fixture(t)
	if err := os.Symlink("big.zip", filepath.Join(env.Home, "Downloads/link")); err != nil {
		t.Fatal(err)
	}
	to := t.TempDir()
	out, err := run(t, env, "culprit", "relocate", "downloads", "--to", to, "--link", "--same-volume", "--yes")
	if err != nil {
		t.Fatal(err, out)
	}
	dest := filepath.Join(to, "Downloads")
	if fi, err := os.Stat(filepath.Join(dest, "big.zip")); err != nil || fi.Size() != 256<<10 {
		t.Fatalf("copy: %v", err)
	}
	if l, _ := os.Readlink(filepath.Join(dest, "link")); l != "big.zip" {
		t.Errorf("symlink not kept: %q", l)
	}
	if l, err := os.Readlink(filepath.Join(env.Home, "Downloads")); err != nil || l != dest {
		t.Fatalf("no link back: %q %v", l, err)
	}
	// A second relocate finds nothing present (the path is a symlink).
	if _, err := run(t, env, "culprit", "relocate", "downloads", "--to", to, "--same-volume", "--yes"); err == nil {
		t.Error("relocated a symlink")
	}
}

func TestRelocateRefusesSameVolumeAndExisting(t *testing.T) {
	env := fixture(t)
	to := t.TempDir()
	if out, err := run(t, env, "culprit", "relocate", "downloads", "--to", to, "--yes"); err == nil || !strings.Contains(out+err.Error(), "same volume") {
		t.Fatalf("same volume allowed: %v %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(to, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, env, "culprit", "relocate", "downloads", "--to", to, "--same-volume", "--yes"); err == nil {
		t.Fatal("overwrote an existing destination")
	}
	if _, err := os.Stat(filepath.Join(env.Home, "Downloads/big.zip")); err != nil {
		t.Fatal(err)
	}
}

func TestTopListsBiggestFirst(t *testing.T) {
	env := fixture(t)
	out, err := run(t, env, "top", env.Home, "--depth", "1", "--min", "1K", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	var res TopResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err, out)
	}
	if len(res.Entries) < 3 || filepath.Base(res.Entries[0].Path) != "Downloads" && filepath.Base(res.Entries[0].Path) != "big.zip" {
		t.Fatalf("entries %+v", res.Entries)
	}
	for i := 1; i < len(res.Entries); i++ {
		if res.Entries[i].Usage.Bytes > res.Entries[i-1].Usage.Bytes {
			t.Fatal("not sorted")
		}
	}
}

func TestHardLinkCountsOnce(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	if err := os.WriteFile(a, bytes.Repeat([]byte("x"), 64<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}
	u, err := Walk(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if u.Bytes > 64<<10+16<<10 {
		t.Fatalf("hard link counted twice: %d", u.Bytes)
	}
}

func TestCulpritVerbs(t *testing.T) {
	env := fixture(t)
	out, err := run(t, env, "culprit", "size", "npm-cache")
	if err != nil || !strings.Contains(out, `"human"`) {
		t.Fatalf("size: %v %s", err, out)
	}
	out, err = run(t, env, "culprit", "show", "pnpm-store")
	if err != nil || !strings.Contains(out, `"found"`) {
		t.Fatalf("show: %v %s", err, out)
	}
	out, err = run(t, env, "culprit", "list", "--format", "table")
	if err != nil || !strings.Contains(out, "pnpm-store") {
		t.Fatalf("list: %v %s", err, out)
	}
	if _, err := run(t, env, "culprit", "size", "nope"); err == nil {
		t.Fatal("sized an unknown culprit")
	}
}

func TestSmokeFrame(t *testing.T) {
	env := fixture(t)
	out, err := run(t, env, "tui", "--smoke", "--width", "120", "--height", "24")
	if err != nil {
		t.Fatal(err, out)
	}
	for _, want := range []string{"Culprits 4", "Detail", "downloads", "rebuildable"} {
		if !strings.Contains(out, want) {
			t.Errorf("frame lacks %q:\n%s", want, out)
		}
	}
}

func TestMarkedRowsClearTogether(t *testing.T) {
	env := fixture(t)
	a, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.r.Execute(ochazuke.Request{Noun: "culprit", Verb: "list", Target: pageID}); err != nil {
		t.Fatal(err)
	}
	settle(t, a)
	for _, id := range []string{"culprit/npm-cache", "culprit/turbo-cache"} {
		if err := a.list.ToggleMark(id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.r.Execute(ochazuke.Request{Noun: "ochazuke.list", Verb: "apply", Args: ochazuke.Arguments{"id": culpritsID, "verb": "clear", "confirmed": true}}); err != nil {
		t.Fatal(err)
	}
	settle(t, a)
	for _, p := range []string{".npm/_cacache/a", "src/one/.turbo/cache"} {
		if _, err := os.Stat(filepath.Join(env.Home, p)); err == nil {
			t.Errorf("%s survived", p)
		}
	}
	if _, err := os.Stat(filepath.Join(env.Home, "Library/pnpm/store/v10/f")); err != nil {
		t.Error("an unmarked row was cleared")
	}
}

func settle(t *testing.T, a *App) {
	t.Helper()
	if err := ocli.Settle(a.r, nil); err != nil {
		t.Fatal(err)
	}
}

func TestBindingsAreValid(t *testing.T) {
	a, err := New(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if warnings, err := a.r.ValidateBindings(); err != nil || len(warnings) > 0 {
		t.Fatal(err, warnings)
	}
}

func TestParseSizeAndHuman(t *testing.T) {
	for in, want := range map[string]int64{"1G": 1 << 30, "500M": 500 << 20, "1.5K": 1536, "10": 10} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v", in, got, err)
		}
	}
	if Human(1<<30) != "1.0G" || Human(9_600_000_000) != "8.9G" || Human(12<<30) != "12G" {
		t.Errorf("Human: %s %s %s", Human(1<<30), Human(9_600_000_000), Human(12<<30))
	}
}

// external makes a directory outside HOME look like another volume
// (deviceOf), links ~/src/ext to it and puts a turbo cache in it.
func external(t *testing.T, env Env) string {
	t.Helper()
	ext, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ext, ".turbo/cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, ".turbo/cache/blob"), bytes.Repeat([]byte("x"), 512<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ext, filepath.Join(env.Home, "src/ext")); err != nil {
		t.Fatal(err)
	}
	real := deviceOf
	t.Cleanup(func() { deviceOf = real })
	deviceOf = func(p string) (uint64, error) {
		if r, err := filepath.EvalSymlinks(p); err == nil && (r == ext || strings.HasPrefix(r, ext+"/")) {
			return 999, nil
		}
		return real(p)
	}
	return ext
}

func TestExternalVolumeSkippedByDefault(t *testing.T) {
	env := fixture(t)
	ext := external(t, env)
	cfg, _ := Load(env)
	turbo, _ := cfg.Find("turbo-cache")
	r := Probe(context.Background(), env, turbo)
	var states []string
	for _, f := range r.Found {
		states = append(states, f.State)
	}
	if r.Bytes >= 512<<10 || !strings.Contains(strings.Join(states, ","), "external") {
		t.Fatalf("external path sized: bytes %d states %v", r.Bytes, states)
	}
	// clear never touches it.
	if out, err := run(t, env, "culprit", "clear", "turbo-cache", "--yes"); err != nil {
		t.Fatal(err, out)
	}
	if _, err := os.Stat(filepath.Join(ext, ".turbo/cache/blob")); err != nil {
		t.Fatalf("clear reached the external volume: %v", err)
	}
	// top refuses a PATH there.
	if out, err := run(t, env, "top", filepath.Join(env.Home, "src/ext")); err == nil || !strings.Contains(out+err.Error(), "another volume") {
		t.Fatalf("top read the external volume: %v %s", err, out)
	}
}

func TestExternalOptIn(t *testing.T) {
	env := fixture(t)
	external(t, env)
	cfg, _ := Load(env)
	turbo, _ := cfg.Find("turbo-cache")
	turbo.External = true
	if r := Probe(context.Background(), env, turbo); r.Bytes < 512<<10 {
		t.Fatalf("row opt-in: bytes %d", r.Bytes)
	}
	userFile(t, env, `{"external": true, "culprits": []}`)
	out, err := run(t, env, "scan", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	if !strings.Contains(out, `"state": "present"`) || strings.Contains(out, `"state": "external"`) {
		t.Fatalf("file opt-in ignored: %s", out)
	}
	if _, err := run(t, env, "top", filepath.Join(env.Home, "src/ext"), "--external"); err != nil {
		t.Fatal(err)
	}
}

func TestTopTimeoutIsPartial(t *testing.T) {
	env := fixture(t)
	out, err := run(t, env, "top", env.Home, "--timeout", "1ns", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	var res TopResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err, out)
	}
	if !res.Partial {
		t.Fatalf("not partial: %+v", res)
	}
}
