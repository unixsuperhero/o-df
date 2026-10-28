# o-df: agent guide

`o-df` (so `o df`) finds and clears disk hogs. Usage, keys and the culprits file are in README.md. It is an ochazuke application (ochazuke's `AGENTS.md`, "Building an application on ochazuke", and its Invariants apply unchanged) with oden dispatch.

## Plans and todos

Plans and todos are kept outside this repository; do not add a TODO.md or docs/plans/ here.

## Where does X live

| Concern | File |
|---|---|
| The culprit row, the embedded defaults, the user file merge, path expansion | `culprits.go`, `culprits.json` |
| The parallel walk (allocated bytes, one volume, hard links once), disk totals | `walk.go`, `stat_*.go` |
| Probing a culprit, sizes and ages as text | `probe.go` |
| clear and relocate (copy, verify, delete, link), the home guard | `actions.go` |
| `App`, the runtime, the culprit verbs, key rows, rows and cells | `app.go` |
| The page | `view.go` |
| `tui`, `--smoke` | `tui.go` |
| `main`, oden dispatch, `scan`, `top` | `main.go` |

## Invariants

- No special cases: every culprit is a row of data (paths, rebuildable, size command, clear commands, advice). Code never branches on a culprit's name. A new hog is a row in `culprits.json` or the user's file.
- clear acts only on rebuildable rows, after the verb's confirm; relocate never overwrites, verifies before it deletes, and refuses the source's own volume. Both refuse `/`, the home directory and its parents.
- The walk never follows a symlink and never crosses into another volume; a path resolving onto another volume is skipped unless a row, the file or a flag sets `external` (`Env.Elsewhere`, the `deviceOf` hook in tests). Walks are bounded (`ProbeTimeout`, `top --timeout`) because a stuck directory read never returns.
- Tests run on a temp HOME fixture only.

## Checks

```sh
gofmt -l . && GOWORK=off go vet ./... && GOWORK=off GOFLAGS=-trimpath go test ./...
GOWORK=off go run . tui --smoke
```

ochazuke (`v0.35.0`) and oden (`v0.1.1`) are required at their tags; never commit a `go.work` or a `replace`.
