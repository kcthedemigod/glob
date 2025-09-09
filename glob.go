// Package glob adds a minimal "**" (recursive) glob on top of filepath semantics.
// Usage:
//   matches, err := glob.Glob(".", "src/**/test/*.go", nil)
package glob

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Opts struct {
	FollowSymlinks bool // off by default; avoids cycles
	IncludeFiles   bool // default true
	IncludeDirs    bool // default true
	Dot            bool // default false; match dotfiles only when pattern seg starts with '.'
}

func Glob(root, pattern string, o *Opts) ([]string, error) {
	opts := applyDefaults(o)

	// Fast path: if there's no "**", let filepath.Glob do its thing.
	if !strings.Contains(pattern, "**") {
		return filepath.Glob(filepath.Join(root, pattern))
	}

	// Normalize slashes for current OS and split on separator.
	segs := split(filepath.FromSlash(pattern))
	if len(segs) == 0 {
		return nil, nil
	}

	type qitem struct {
		path string
		i    int // next segment index
	}

	var (
		work    = []qitem{{path: root, i: 0}}
		results []string
		seen    = map[string]struct{}{}
	)

	for len(work) > 0 {
		var next []qitem
		for _, it := range work {
			// Done consuming pattern? Decide if we emit this path.
			if it.i >= len(segs) {
				if emit(it.path, opts) {
					if _, ok := seen[it.path]; !ok {
						seen[it.path] = struct{}{}
						results = append(results, it.path)
					}
				}
				continue
			}

			seg := segs[it.i]

			// "**" means: zero or more dirs.
			if seg == "**" {
				// Zero dirs:
				next = append(next, qitem{path: it.path, i: it.i + 1})

				// Terminal "**" -> collect everything underneath (files + dirs).
				if it.i+1 == len(segs) {
					all, _ := collectAll(it.path, opts)
					for _, p := range all {
						if _, ok := seen[p]; !ok && emit(p, opts) {
							seen[p] = struct{}{}
							results = append(results, p)
						}
					}
					// Don't enqueue more for this branch.
					continue
				}

				// One-or-more dirs:
				subs, _ := listSubdirs(it.path, opts.FollowSymlinks)
				for _, name := range subs {
					next = append(next, qitem{path: filepath.Join(it.path, name), i: it.i})
				}
				continue
			}

			// Normal segment (literal/wildcards) within current dir.
			ents, err := os.ReadDir(it.path)
			if err != nil {
				// Not a dir or unreadable; skip.
				continue
			}
			for _, de := range ents {
				name := de.Name()
				ok, err := match(seg, name, opts.Dot)
				if err != nil || !ok {
					continue
				}
				next = append(next, qitem{
					path: filepath.Join(it.path, name),
					i:    it.i + 1,
				})
			}
		}
		work = next
	}

	sort.Strings(results)
	return results, nil
}

func applyDefaults(o *Opts) *Opts {
	if o == nil {
		return &Opts{IncludeFiles: true, IncludeDirs: true}
	}
	cp := *o
	if !cp.IncludeFiles && !cp.IncludeDirs {
		cp.IncludeFiles = true // sensible default
	}
	return &cp
}

func split(p string) []string {
	if p == "" {
		return nil
	}
	sep := string(filepath.Separator)
	raw := strings.Split(p, sep)
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func match(pattern, name string, dot bool) (bool, error) {
	// No meta? literal compare is faster.
	if !strings.ContainsAny(pattern, "*?[") {
		return pattern == name, nil
	}
	// filepath.Glob rule: names starting with '.' only match when pattern starts with '.'
	if !dot && strings.HasPrefix(name, ".") && !strings.HasPrefix(pattern, ".") {
		return false, nil
	}
	return filepath.Match(pattern, name)
}

func listSubdirs(dir string, follow bool) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ents))
	for _, de := range ents {
		// Default: skip symlinked dirs to avoid loops.
		if de.IsDir() {
			if (de.Type()&fs.ModeSymlink) != 0 && !follow {
				continue
			}
			if follow && (de.Type()&fs.ModeSymlink) != 0 {
				// Only include if it actually points to a dir.
				if info, err := os.Stat(filepath.Join(dir, de.Name())); err == nil && info.IsDir() {
					out = append(out, de.Name())
				}
				continue
			}
			out = append(out, de.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func collectAll(root string, o *Opts) ([]string, error) {
	var out []string
	// NOTE: WalkDir doesn't follow dir symlinks; fine unless FollowSymlinks=true is critical.
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable branches; keep going.
			return nil
		}
		// Skip symlinked dirs when not following.
		if (d.Type()&fs.ModeSymlink) != 0 && !o.FollowSymlinks {
			return nil
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, nil
}

func emit(p string, o *Opts) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	if fi.IsDir() {
		return o.IncludeDirs
	}
	// Non-regular files (sockets, fifos, symlinks) count as "files" here.
	return o.IncludeFiles
}
