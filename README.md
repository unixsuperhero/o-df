# o-df

`o-df` (so `o df`) finds and clears the usual disk hogs on a Mac: package
caches, browser caches, build caches, model downloads, the Downloads folder.
Each culprit is a row of data, not code: o-df ships a default list and reads
yours from `~/.config/o-df/culprits.json`.

```sh
o-df                      # the same as o-df scan
o-df scan [--all] [--json] [--timeout 60s] [--external]
o-df top [PATH] [--depth 2] [--n 30] [--min 100M] [--timeout 5m] [--external] [--json]
o-df culprit list [--all] [--format table]
o-df culprit show NAME
o-df culprit size NAME
o-df culprit clear NAME [--yes]
o-df culprit relocate NAME [--to DIR] [--link] [--yes]
o-df tui [--smoke]        # the culprits page
o-df catalog              # the nouns, verbs and page for a host
```

## scan

`o-df scan` prints the volume's totals and every culprit with at least one
path present, biggest first: name, size, rebuildable, when it was last used
(the newest access or change time inside it), the advice (delete, relocate
or keep) and its paths. A path that is a symlink shows as `path → target`
and is not counted: it already lives elsewhere. `--all` lists absent
culprits too; `--json` prints `{disk, total, culprits: [report...]}`.

Sizes are allocated bytes (`st_blocks`), as `du` counts them, so a sparse
disk image counts what it uses. A hard-linked file counts once. A size
marked `≥` met an unreadable directory (for example `~/.Trash` without Full
Disk Access) and is a lower bound. A path whose walk takes longer than
`--timeout` (default 60s; a directory another process holds in an
uninterruptible wait never answers) shows `timeout` instead of hanging the
scan.

### Other volumes

o-df reads only the home directory's volume. The walk never crosses into
another volume or follows a symlink, and a culprit path that resolves
(through a symlinked directory above it) onto another volume shows as
`skipped` and is neither sized nor cleared. To read one anyway, set
`"external": true` on the row, or at the top of the culprits file for every
row and `top`, or pass `--external` to `scan`, `top` or `culprit list`.

## top

`o-df top PATH` walks PATH once, in parallel, without crossing into another
volume or following symlinks, and prints its biggest directories (down to
`--depth` levels) and files of at least `--min`, biggest first. Use it to
find what no culprit row covers yet. A PATH on another volume is refused
without `--external`; after `--timeout` (default 5m) it prints the
directories it finished, marked partial:

```sh
o-df top ~ --depth 2 --min 1G
o-df top ~/Downloads --depth 1 --min 500M
```

## culprit verbs

`culprit` is a noun with five verbs, the same on the command line, in the
TUI and from a host (`o-df catalog`). Every one prints JSON
(`--format table|markdown` for the list).

- `list`: rows `{noun, id: culprit/NAME, label, data: report, cells}`.
- `show NAME`: the row and every path it expands to, sized.
- `size NAME`: `{name, bytes, human, files, last_used}`.
- `clear NAME`: only for a rebuildable culprit, after a confirm (`--yes`
  answers it). It runs the row's `clear` commands, or removes the contents
  of each present path (the directories stay). Read-only trees are made
  writable first. Prints `{name, before, after, freed}`.
- `relocate NAME [--to DIR] [--link]`: copies each present path to
  `DIR/<path under home>` (default DIR: `relocate_to`), compares the copy
  with the source entry by entry (kind, size, symlink target), deletes the
  source and, with `--link`, leaves a symlink to the copy. It refuses a
  destination that exists or that is on the source's volume. Prints
  `{name, dir, moved: [{from, to, bytes, linked}], freed}`.

Both changes refuse `/`, the home directory and anything above it.

## The TUI

`o-df tui` opens the culprits page: the list on the left, the focused
culprit's detail (every path, its size, the rebuild hint, the clear
commands) on the right, and a status line with the disk's totals and the
size of the marked rows.

| Key | Does |
|---|---|
| `j` / `k` | move |
| space | mark or unmark a row |
| `d d` | clear the marked rows (the focused one when none is marked), one confirm for all |
| `m` | relocate the marked rows to `relocate_to`, one confirm for all |
| `R` | size everything again |
| `?` | every key |
| `q` | quit |

The keys are binding rows (`list.apply {verb: clear}`, `list.apply {verb:
relocate, args: {to}}`), so a `bindings.json` row can add another
destination key without code.

## Configuring culprits

The defaults are in [culprits.json](culprits.json), embedded in the binary.
Your file (`~/.config/o-df/culprits.json`, or `$XDG_CONFIG_HOME/o-df/`, or
the path in `$O_DF_CULPRITS`) has the same shape. A row with a new name is
added; a row with a default's name replaces it whole; `"disabled": true`
drops it. Unknown fields are an error, so a typo does not pass silently.

```json
{
  "relocate_to": "/Volumes/Backup/relocated",
  "culprits": [
    {"name": "sorbet-cache", "title": "Sorbet caches", "category": "ruby",
     "paths": ["~/src/*/sorbet/.cache"], "rebuildable": true,
     "rebuild": "srb tc rebuilds it", "recommend": "delete"},
    {"name": "docker-build-cache", "paths": ["~/Library/Containers/com.docker.docker"],
     "rebuildable": true, "size": ["sh", "-c", "docker system df --format '{{.Size}}' | head -1"],
     "clear": [["docker", "builder", "prune", "--all", "--force"]], "recommend": "delete"},
    {"name": "trash", "disabled": true, "paths": ["~/.Trash"]}
  ]
}
```

| Field | Meaning |
|---|---|
| `name` | one word, the id (`culprit/NAME`) |
| `title`, `category`, `notes` | shown in the detail |
| `paths` | `~`, `$HOME` and `*` (one path segment, `filepath.Glob`); a missing path is absent |
| `rebuildable` | the owning tool recreates the data; only these can be cleared |
| `rebuild` | how it comes back |
| `size` | a command whose stdout's first integer is the size in bytes, instead of walking `paths` |
| `clear` | commands (each an argv) instead of removing the paths' contents; a command holding `{path}` runs once per present path |
| `recommend` | `delete`, `relocate` or `keep`: the advice the scan prints |
| `external` | allow paths that resolve onto another volume (top level: every row and `top`) |
| `disabled` | drop a default row |

## Build

```sh
GOWORK=off GOFLAGS=-trimpath go build -o o-df .
gofmt -l . && GOWORK=off go vet ./... && GOWORK=off go test ./...
GOWORK=off go run . tui --smoke
```

Tests build a temp HOME fixture and never touch the real one.
