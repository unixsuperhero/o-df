package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"syscall"
	"time"
)

// The walk: one parallel, deterministic pass over a tree that sums the
// allocated bytes (st_blocks, so a sparse disk image counts what it uses),
// counts files, finds the newest access or change time, skips other
// volumes and symlinks, and counts a hard-linked file once. Directories it
// cannot read are counted as errors, not failures.

// Usage is what a walk found under one path.
type Usage struct {
	Bytes    int64     `json:"bytes"`
	Files    int64     `json:"files"`
	Dirs     int64     `json:"dirs"`
	Errors   int64     `json:"errors,omitempty"`
	LastUsed time.Time `json:"last_used,omitzero"`
}

func (u *Usage) add(o Usage) {
	u.Bytes += o.Bytes
	u.Files += o.Files
	u.Dirs += o.Dirs
	u.Errors += o.Errors
	if o.LastUsed.After(u.LastUsed) {
		u.LastUsed = o.LastUsed
	}
}

// Entry is one directory or file a walk reports (top).
type Entry struct {
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Usage Usage  `json:"usage"`
}

// walker holds one walk's shared state.
type walker struct {
	ctx   context.Context
	dev   uint64
	sem   chan struct{}
	seen  sync.Map // inode key -> struct{}, for files with more than one link
	depth int      // directories at or above this depth (root 0) are reported
	min   int64    // files at least this big are reported
	mu    sync.Mutex
	out   []Entry
}

// Walk sizes the tree at root (a file sizes itself). It does not follow a
// symlink, and stops at another volume.
func Walk(ctx context.Context, root string) (Usage, error) {
	w, err := newWalker(ctx, root, -1, -1)
	if err != nil {
		return Usage{}, err
	}
	return w.entry(root, 0), ctx.Err()
}

// Top walks root and reports every directory down to depth levels below
// it, and every file of at least min bytes, biggest first. When ctx ends
// first (a directory whose read never returns, a deadline), it returns
// what the walk had finished, partial set and the total zero; the stuck
// walk is left behind.
func Top(ctx context.Context, root string, depth int, min int64) (entries []Entry, total Usage, partial bool, err error) {
	w, err := newWalker(ctx, root, depth, min)
	if err != nil {
		return nil, Usage{}, false, err
	}
	done := make(chan Usage, 1)
	go func() { done <- w.entry(root, 0) }()
	select {
	case total = <-done:
		// A walk that saw ctx end skipped directories: also partial.
		partial = ctx.Err() != nil
	case <-ctx.Done():
		partial = true
	}
	if partial {
		total = Usage{}
	}
	w.mu.Lock()
	entries = append([]Entry(nil), w.out...)
	w.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Usage.Bytes != entries[j].Usage.Bytes {
			return entries[i].Usage.Bytes > entries[j].Usage.Bytes
		}
		return entries[i].Path < entries[j].Path
	})
	return entries, total, partial, nil
}

func newWalker(ctx context.Context, root string, depth int, min int64) (*walker, error) {
	fi, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("no stat information on this system")
	}
	return &walker{ctx: ctx, dev: uint64(st.Dev), sem: make(chan struct{}, 4*runtime.NumCPU()), depth: depth, min: min}, nil
}

// entry sizes one path at depth d.
func (w *walker) entry(path string, d int) Usage {
	fi, err := os.Lstat(path)
	if err != nil {
		return Usage{Errors: 1}
	}
	st, _ := fi.Sys().(*syscall.Stat_t)
	if st == nil || uint64(st.Dev) != w.dev {
		return Usage{}
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return Usage{Files: 1, Bytes: st.Blocks * 512}
	}
	if !fi.IsDir() {
		u := Usage{Files: 1, LastUsed: stamp(st)}
		if st.Nlink > 1 {
			if _, dup := w.seen.LoadOrStore([2]uint64{uint64(st.Dev), st.Ino}, struct{}{}); dup {
				return u
			}
		}
		u.Bytes = st.Blocks * 512
		if w.min >= 0 && u.Bytes >= w.min && d > 0 {
			w.report(Entry{Path: path, Usage: u})
		}
		return u
	}
	u := Usage{Dirs: 1, Bytes: st.Blocks * 512}
	if w.ctx.Err() != nil {
		return u
	}
	names, err := readNames(path)
	if err != nil {
		u.Errors++
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, name := range names {
		child := filepath.Join(path, name)
		select {
		case w.sem <- struct{}{}:
			wg.Add(1)
			go func() {
				defer wg.Done()
				cu := w.entry(child, d+1)
				<-w.sem
				mu.Lock()
				u.add(cu)
				mu.Unlock()
			}()
		default:
			cu := w.entry(child, d+1)
			mu.Lock()
			u.add(cu)
			mu.Unlock()
		}
	}
	wg.Wait()
	if d > 0 && d <= w.depth {
		w.report(Entry{Path: path, Dir: true, Usage: u})
	}
	return u
}

func (w *walker) report(e Entry) {
	w.mu.Lock()
	w.out = append(w.out, e)
	w.mu.Unlock()
}

func readNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

// Disk is a volume's totals.
type Disk struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Used  int64  `json:"used"`
	Free  int64  `json:"free"`
	Usage int    `json:"capacity_percent"`
}

// DiskOf reads the totals of the volume holding path.
func DiskOf(path string) (Disk, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return Disk{}, err
	}
	bs := int64(s.Bsize)
	size, free := int64(s.Blocks)*bs, int64(s.Bavail)*bs
	used := size - int64(s.Bfree)*bs
	d := Disk{Path: path, Size: size, Used: used, Free: free}
	if used+free > 0 {
		d.Usage = int((used*100 + used + free - 1) / (used + free))
	}
	return d, nil
}

func newer(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
