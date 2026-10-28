package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Probing: a culprit's paths expanded and sized. A path is present (sized
// by a walk), absent, or a symlink (relocated: its target is not sized,
// since it lives elsewhere). A culprit with a size command takes that
// command's number instead of the walk's.

// PathUsage is one expanded path.
type PathUsage struct {
	Path   string `json:"path"`
	State  string `json:"state"` // present, absent, symlink, external
	Target string `json:"target,omitempty"`
	// Timeout says the walk did not finish within ProbeTimeout.
	Timeout bool  `json:"timeout,omitempty"`
	Usage   Usage `json:"usage"`
}

// Report is a probed culprit.
type Report struct {
	Culprit
	Bytes    int64       `json:"bytes"`
	Present  bool        `json:"present"`
	LastUsed time.Time   `json:"last_used,omitzero"`
	Files    int64       `json:"files"`
	Errors   int64       `json:"errors,omitempty"`
	Found    []PathUsage `json:"found"`
	SizeErr  string      `json:"size_error,omitempty"`
}

// Probe sizes one culprit.
func Probe(ctx context.Context, env Env, c Culprit) Report {
	r := Report{Culprit: c}
	for _, p := range c.Paths {
		matches, literal := env.Expand(p)
		if len(matches) == 0 {
			r.Found = append(r.Found, PathUsage{Path: literal, State: "absent"})
			continue
		}
		for _, m := range matches {
			if resolved, ok := env.Elsewhere(m); ok && !env.External && !c.External {
				r.Found = append(r.Found, PathUsage{Path: m, State: "external", Target: resolved})
				continue
			}
			r.Found = append(r.Found, probePath(ctx, m))
		}
	}
	for _, f := range r.Found {
		if f.State == "present" {
			r.Present = true
		}
		r.Bytes += f.Usage.Bytes
		r.Files += f.Usage.Files
		r.Errors += f.Usage.Errors
		r.LastUsed = newer(r.LastUsed, f.Usage.LastUsed)
	}
	if len(c.Size) > 0 {
		n, err := sizeCommand(ctx, c.Size)
		if err != nil {
			r.SizeErr = err.Error()
		} else {
			r.Bytes, r.Present = n, n > 0 || r.Present
		}
	}
	return r
}

// deviceOf is the device a path resolves onto (a test hook).
var deviceOf = func(p string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil
}

// Elsewhere reports whether p, with every symlink in it resolved, lives on
// another volume than the home directory; it returns the resolved path. A
// symlink at p itself is not followed here (probe shows it as a symlink),
// only the directories above it.
func (env Env) Elsewhere(p string) (string, bool) {
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return "", false
	}
	resolved := filepath.Join(dir, filepath.Base(p))
	home, err := deviceOf(env.Home)
	if err != nil {
		return resolved, false
	}
	dev, err := deviceOf(dir)
	if err != nil {
		return resolved, false
	}
	if fi, err := os.Lstat(resolved); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		if d, err := deviceOf(resolved); err == nil {
			dev = d
		}
	}
	return resolved, dev != home
}

// ProbeTimeout bounds one culprit's walk. A directory whose read never
// returns (a file another process holds in an uninterruptible wait) must
// not hang the scan: the path is reported as timed out and the walk is
// left behind.
var ProbeTimeout = 60 * time.Second

func probePath(ctx context.Context, p string) PathUsage {
	fi, err := os.Lstat(p)
	if err != nil {
		return PathUsage{Path: p, State: "absent"}
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(p)
		return PathUsage{Path: p, State: "symlink", Target: target}
	}
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	done := make(chan Usage, 1)
	go func() {
		u, err := Walk(ctx, p)
		if err != nil {
			u.Errors++
		}
		done <- u
	}()
	select {
	case u := <-done:
		return PathUsage{Path: p, State: "present", Usage: u}
	case <-ctx.Done():
		return PathUsage{Path: p, State: "present", Timeout: true, Usage: Usage{Errors: 1}}
	}
}

var firstInt = regexp.MustCompile(`\d+`)

func sizeCommand(ctx context.Context, argv []string) (int64, error) {
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		return 0, fmt.Errorf("%s: %w", argv[0], err)
	}
	m := firstInt.Find(out)
	if m == nil {
		return 0, fmt.Errorf("%s printed no number", argv[0])
	}
	return strconv.ParseInt(string(m), 10, 64)
}

// ProbeAll sizes every culprit in parallel, biggest first (then by name).
func ProbeAll(ctx context.Context, env Env, cs []Culprit) []Report {
	out := make([]Report, len(cs))
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Add(1)
		go func() { defer wg.Done(); out[i] = Probe(ctx, env, c) }()
	}
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Human is a byte count in powers of 1024, df -h style.
func Human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	if v < 10 {
		return fmt.Sprintf("%.1f%c", v, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.0f%c", v, "KMGTPE"[exp])
}

// ParseSize reads 500M, 1G, 1.5G, 2048 (bytes).
func ParseSize(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		mult = 1 << 10
	case 'm', 'M':
		mult = 1 << 20
	case 'g', 'G':
		mult = 1 << 30
	case 't', 'T':
		mult = 1 << 40
	}
	num := s
	if mult > 1 {
		num = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("not a size: %q (use 500M, 1G, ...)", s)
	}
	return int64(f * float64(mult)), nil
}

// Ago is how long before now t was, in its largest whole unit.
func Ago(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Hour:
		return "now"
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d/(30*24*time.Hour)))
	}
	return fmt.Sprintf("%dy", int(d/(365*24*time.Hour)))
}
