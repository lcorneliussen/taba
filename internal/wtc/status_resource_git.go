package wtc

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type statusGitMemoKey struct{}
type statusIdentityMemoKey struct{}

func newStatusIdentityMemo() *statusGitMemo {
	memo := newStatusGitMemo()
	memo.discoverConfig = true
	memo.provenanceGate = make(chan struct{}, 1)
	memo.provenanceGate <- struct{}{}
	return memo
}

func newStatusGitMemo() *statusGitMemo {
	return &statusGitMemo{entries: map[string]*statusGitMemoEntry{}}
}

type statusGitMemo struct {
	mu              sync.Mutex
	entries         map[string]*statusGitMemoEntry
	discoverConfig  bool
	provenanceGate  chan struct{}
	provenanceKey   string
	provenancePaths []string
}
type statusGitMemoEntry struct {
	done  chan struct{}
	value string
	err   error
}

// Standalone operations own a fresh memo. Status receivers may carry a bounded
// advisory identity memo; every hit still requires current input validation.
func statusResourceOperation(ctx context.Context) context.Context {
	memo, _ := ctx.Value(statusIdentityMemoKey{}).(*statusGitMemo)
	if memo == nil {
		memo = newStatusGitMemo()
	}
	return context.WithValue(ctx, statusGitMemoKey{}, memo)
}

// Only repository identity reads are reusable. Mutable local facts are collected
// anew, and all other Git commands (including writes) bypass this memo.
func statusResourceGitKey(args []string) string { return statusResourceGitKeyConfig(args, nil, "") }

func statusResourceGitKeyConfig(args []string, provenConfigs []string, provenance string) string {
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
	case "rev-parse\x00--git-common-dir", "rev-parse\x00--show-toplevel", "rev-parse\x00--path-format=absolute\x00--git-common-dir", "remote\x00get-url\x00origin", "remote":
	default:
		return ""
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	h := sha256.New()
	fmt.Fprintln(h, path, provenance)
	add := func(p string) bool {
		st, err := os.Lstat(p)
		if os.IsNotExist(err) {
			fmt.Fprintln(h, p, "absent")
			return true
		}
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if !st.IsDir() && !st.Mode().IsRegular() && p != "/dev/null" {
			return false
		}
		id, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return false
		}
		if st.Mode().IsRegular() && (st.Mode().Perm()&0022 != 0 || (id.Uid != 0 && id.Uid != uint32(os.Getuid()))) {
			return false
		}
		if st.IsDir() {
			real, err := filepath.EvalSymlinks(p)
			if err != nil {
				return false
			}
			// Namespace identity is independent of index locks and other child writes.
			fmt.Fprintln(h, p, real, st.Mode(), id.Dev, id.Ino, id.Uid, id.Gid)
		} else {
			fmt.Fprintln(h, p, st.Mode(), st.Size(), st.ModTime().UnixNano(), id.Dev, id.Ino, id.Uid, id.Gid)
		}
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
	if provenConfigs != nil {
		configs = provenConfigs
	} else if os.Getenv("GIT_CONFIG_NOSYSTEM") != "1" {
		if system := os.Getenv("GIT_CONFIG_SYSTEM"); system != "" {
			configs = append(configs, system)
		} else {
			return ""
		}
	}
	if os.Getenv("GIT_DIR") != "" || os.Getenv("GIT_WORK_TREE") != "" || os.Getenv("GIT_CONFIG_COUNT") != "" || os.Getenv("GIT_COMMON_DIR") != "" || os.Getenv("GIT_CONFIG_PARAMETERS") != "" {
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
		key := memo.fingerprint(ctx, args)
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if key == "" {
			v, e := read()
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return v, e
		}
		memo.mu.Lock()
		entry, ok := memo.entries[key]
		if !ok && len(memo.entries) >= 128 {
			for old, candidate := range memo.entries {
				select {
				case <-candidate.done:
					delete(memo.entries, old)
				default:
				}
				if len(memo.entries) < 128 {
					break
				}
			}
			if len(memo.entries) >= 128 {
				memo.mu.Unlock()
				value, err := read()
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				return value, err
			}
		}
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
			if key != memo.fingerprint(ctx, args) {
				continue
			}
			return entry.value, entry.err
		}
		value, err := read()
		if ctx.Err() != nil {
			value, err = "", ctx.Err()
		}
		currentKey := memo.fingerprint(ctx, args)
		if ctx.Err() != nil {
			value, err = "", ctx.Err()
		}
		memo.mu.Lock()
		if ctx.Err() != nil {
			value, err = "", ctx.Err()
		}
		entry.value, entry.err = value, err
		if err != nil || ctx.Err() != nil || key != currentKey {
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
	branch   string
	changed  int
	conflict bool
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
			// Upstream/count semantics remain in the established authoritative reads.
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

func statusResourceCounts(ctx context.Context, path, upstream, base string) (int, int, bool) {
	if upstream != base {
		return 0, 0, false
	}
	raw, err := statusGitContext(ctx, path, "rev-list", "--left-right", "--count", base+"...HEAD")
	fields := strings.Fields(raw)
	if err != nil || len(fields) != 2 {
		return 0, 0, false
	}
	behind, be := strconv.Atoi(fields[0])
	ahead, ae := strconv.Atoi(fields[1])
	return ahead, behind, be == nil && ae == nil && behind >= 0 && ahead >= 0
}

func statusResourceHeads(ctx context.Context, path, base string) (string, string, bool) {
	if base == "" {
		return "", "", false
	}
	raw, err := statusGitContext(ctx, path, "rev-parse", "HEAD", base)
	heads := strings.Fields(raw)
	if err != nil || len(heads) != 2 || !validCheckHead(heads[0]) || !validCheckHead(heads[1]) {
		return "", "", false
	}
	return heads[0], heads[1], true
}

// Discovery asks trusted Git only for its configuration file paths. It never
// evaluates hooks, records values, or relaxes unknown/include/symlink fallback.
func (memo *statusGitMemo) fingerprint(ctx context.Context, args []string) string {
	if !statusResourceKnownGitEnvironment() {
		return ""
	}
	if len(args) < 3 {
		return ""
	}
	command := args[1:]
	if args[0] == "-C" {
		command = args[2:]
	} else if !strings.HasPrefix(args[0], "--git-dir=") {
		return ""
	}
	switch strings.Join(command, "\x00") {
	case "rev-parse\x00--git-common-dir", "rev-parse\x00--show-toplevel", "rev-parse\x00--path-format=absolute\x00--git-common-dir", "remote\x00get-url\x00origin", "remote":
	default:
		return ""
	}
	_, binaryProof := statusResourceGitBinary()
	if binaryProof == "" {
		return ""
	}
	if key := statusResourceGitKeyConfig(args, nil, binaryProof); key != "" || !memo.discoverConfig {
		return key
	}
	paths, proof := memo.configPaths(ctx)
	if paths == nil || ctx.Err() != nil {
		return ""
	}
	return statusResourceGitKeyConfig(args, paths, proof)
}

func (memo *statusGitMemo) configPaths(ctx context.Context) ([]string, string) {
	binary, proof := statusResourceGitBinary()
	if proof == "" {
		return nil, ""
	}
	select {
	case <-ctx.Done():
		return nil, ""
	case <-memo.provenanceGate:
	}
	defer func() { memo.provenanceGate <- struct{}{} }()
	if ctx.Err() != nil {
		return nil, ""
	}
	if memo.provenanceKey == proof {
		return append([]string(nil), memo.provenancePaths...), proof
	}
	paths := []string{}
	for _, name := range []string{"GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL"} {
		if name == "GIT_CONFIG_SYSTEM" && os.Getenv("GIT_CONFIG_NOSYSTEM") == "1" {
			continue
		}
		raw, e := statusRunContext(ctx, 30*time.Second, (*exec.Cmd).Output, binary, "var", name)
		if e != nil || len(raw) > 8192 {
			return nil, ""
		}
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		if len(lines) < 1 || len(lines) > 2 {
			return nil, ""
		}
		for _, path := range lines {
			if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\x00") {
				return nil, ""
			}
			paths = append(paths, path)
		}
	}
	if ctx.Err() != nil {
		return nil, ""
	}
	memo.provenanceKey, memo.provenancePaths = proof, append([]string(nil), paths...)
	return paths, proof
}
func statusResourceKnownGitEnvironment() bool {
	// Unknown Git environment may change discovery or identity independently of files.
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if !strings.HasPrefix(name, "GIT_") {
			continue
		}
		switch name {
		case "GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT", "GIT_TRACE", "GIT_TRACE2", "GIT_TRACE2_EVENT", "GIT_TRACE2_PERF", "GIT_ALLOW_PROTOCOL":
		default:
			return false
		}
	}

	return true
}

func statusResourceGitBinary() (string, string) {
	binary, err := exec.LookPath("git")
	if err != nil {
		return "", ""
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return "", ""
	}
	st, err := os.Stat(binary)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
		return "", ""
	}
	id, ok := st.Sys().(*syscall.Stat_t)
	if !ok || (id.Uid != 0 && id.Uid != uint32(os.Getuid())) {
		return "", ""
	}
	proof := fmt.Sprint(binary, st.Mode(), st.Size(), st.ModTime().UnixNano(), id.Dev, id.Ino, id.Uid, id.Gid)
	for _, name := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"} {
		proof += name + "=" + os.Getenv(name) + "\x00"
	}

	return binary, proof
}
