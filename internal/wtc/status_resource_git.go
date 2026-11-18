package wtc

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

type statusGitMemoKey struct{}
type statusGitMemo struct {
	mu      sync.Mutex
	entries map[string]*statusGitMemoEntry
}
type statusGitMemoEntry struct {
	done  chan struct{}
	value string
	err   error
}

// A memo belongs to one observation, never to a UI lifetime or action decision.
func statusResourceOperation(ctx context.Context) context.Context {
	return context.WithValue(ctx, statusGitMemoKey{}, &statusGitMemo{entries: map[string]*statusGitMemoEntry{}})
}

// Only repository identity reads are reusable. Mutable local facts are collected
// anew, and all other Git commands (including writes) bypass this memo.
func statusResourceGitKey(args []string) string {
	if len(args) < 3 {
		return ""
	}
	path := ""
	command := args
	if args[0] == "-C" {
		path = args[1]
		command = args[2:]
	} else if strings.HasPrefix(args[0], "--git-dir=") {
		path = strings.TrimPrefix(args[0], "--git-dir=")
		command = args[1:]
	} else {
		return ""
	}
	op := strings.Join(command, "\x00")
	switch op {
	case "rev-parse\x00--show-toplevel", "rev-parse\x00--path-format=absolute\x00--git-common-dir", "remote\x00get-url\x00origin", "remote":
	default:
		return ""
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	h := sha256.New()
	fmt.Fprintln(h, path)
	add := func(p string) bool {
		st, err := os.Lstat(p)
		if os.IsNotExist(err) {
			fmt.Fprintln(h, p, "absent")
			return true
		}
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return false
		}
		id, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return false
		}
		fmt.Fprintln(h, p, st.Mode(), st.Size(), st.ModTime().UnixNano(), id.Dev, id.Ino)
		if st.Mode().IsRegular() {
			b, e := statusResourceReadFile(p, 65536)
			if e != nil {
				return false
			}
			if strings.Contains(strings.ToLower(string(b)), "[include") {
				return false
			}
			h.Write(b)
		}
		return true
	}
	if !add(path) {
		return ""
	}
	gitdir := filepath.Join(path, ".git")
	if st, e := os.Lstat(gitdir); e == nil {
		if !add(gitdir) {
			return ""
		}
		if st.Mode().IsRegular() {
			b, e := statusResourceReadFile(gitdir, 4096)
			if e != nil {
				return ""
			}
			v, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
			if !ok {
				return ""
			}
			if !filepath.IsAbs(v) {
				v = filepath.Join(path, v)
			}
			gitdir = filepath.Clean(v)
		}
	} else if os.IsNotExist(e) && strings.HasPrefix(args[0], "--git-dir=") {
		gitdir = path
	} else {
		return ""
	}
	if !add(gitdir) || !add(filepath.Join(gitdir, "config")) || !add(filepath.Join(gitdir, "config.worktree")) || !add(filepath.Join(gitdir, "commondir")) {
		return ""
	}
	common := gitdir
	if b, e := statusResourceReadFile(filepath.Join(gitdir, "commondir"), 4096); e == nil {
		common = strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitdir, common)
		}
	} else if !os.IsNotExist(e) {
		return ""
	}
	if !add(common) || !add(filepath.Join(common, "config")) {
		return ""
	}
	// URL rewriting and includes can live in user/system configuration too.
	home, _ := os.UserHomeDir()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}

	configs := []string{}
	if global := os.Getenv("GIT_CONFIG_GLOBAL"); global != "" {
		configs = append(configs, global)
	} else {
		configs = append(configs, filepath.Join(home, ".gitconfig"), filepath.Join(xdg, "git", "config"))
	}
	if os.Getenv("GIT_CONFIG_NOSYSTEM") != "1" {
		if system := os.Getenv("GIT_CONFIG_SYSTEM"); system != "" {
			configs = append(configs, system)
		} else {
			return ""
		}
	}
	if os.Getenv("GIT_DIR") != "" || os.Getenv("GIT_WORK_TREE") != "" || os.Getenv("GIT_CONFIG_COUNT") != "" {
		return ""
	}
	for _, p := range configs {
		if !add(p) {
			return ""
		}
	}

	fmt.Fprintln(h, op)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func statusResourceGit(ctx context.Context, args []string, read func() (string, error)) (string, error) {
	memo, _ := ctx.Value(statusGitMemoKey{}).(*statusGitMemo)
	if memo == nil {
		return read()
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		key := statusResourceGitKey(args)
		if key == "" {
			return read()
		}
		memo.mu.Lock()
		entry, ok := memo.entries[key]
		if !ok {
			entry = &statusGitMemoEntry{done: make(chan struct{})}
			memo.entries[key] = entry
		}
		memo.mu.Unlock()
		if ok {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-entry.done:
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if key != statusResourceGitKey(args) {
				continue
			}
			return entry.value, entry.err
		}
		value, err := read()
		if ctx.Err() != nil {
			value, err = "", ctx.Err()
		}
		memo.mu.Lock()
		entry.value, entry.err = value, err
		if err != nil || key != statusResourceGitKey(args) {
			delete(memo.entries, key)
		}
		close(entry.done)
		memo.mu.Unlock()
		return value, err
	}
}

// Git resolves these worktree-specific paths together, including linked worktrees.
// An unsupported/malformed result uses the established per-path collector.
func statusResourceOperationState(ctx context.Context, t catchUpTarget) string {
	markers := []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"}
	args := []string{"rev-parse", "--path-format=absolute"}
	for _, marker := range markers {
		args = append(args, "--git-path", marker)
	}
	raw, err := statusGitContext(ctx, t.path, args...)
	if ctx.Err() != nil {
		return "unknown"
	}
	paths := strings.Split(raw, "\n")
	if err != nil || len(paths) != len(markers) {
		return statusResourceOperationFallback(ctx, t)
	}
	for i, path := range paths {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r") {
			return statusResourceOperationFallback(ctx, t)
		}
		if _, err := os.Lstat(path); err == nil {
			return markers[i]
		}
	}
	return ""
}

type statusResourceLocalState struct {
	branch, upstream, porcelain string
	changed                     int
	conflict                    bool
}

// V2 records preserve Git's quoting of paths, so embedded newlines and rename
// sources remain one changed entry, just as in the existing porcelain-v1 view.
func statusResourceLocalRead(ctx context.Context, path string) (statusResourceLocalState, bool) {
	raw, err := statusGitContext(ctx, path, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return statusResourceLocalState{}, false
	}
	s := statusResourceLocalState{}
	head, oid := false, false
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			v := strings.TrimPrefix(line, "# branch.oid ")
			if !validCheckHead(v) {
				return s, false
			}
			oid = true
		case strings.HasPrefix(line, "# branch.head "):
			s.branch = strings.TrimPrefix(line, "# branch.head ")
			head = true
		case strings.HasPrefix(line, "# branch.upstream "):
			s.upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "), strings.HasPrefix(line, "? "):
			s.changed++
		case strings.HasPrefix(line, "u "):
			s.changed++
			s.conflict = true
		case line == "":
		default:
			return s, false
		}
	}
	if !head || !oid || s.branch == "" {
		return s, false
	}
	return s, true
}

func statusResourceOperationFallback(ctx context.Context, t catchUpTarget) string {
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		path, err := statusGitContext(ctx, t.path, "rev-parse", "--git-path", marker)
		if err != nil {
			return marker
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(t.path, path)
		}
		if _, err := os.Lstat(path); err == nil {
			return marker
		}
	}
	return ""
}
