package wtc

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"taba.sh/taba/internal/envcompat"
)

// A public review runs its concern and lead processes in a neutral execution
// context instead of beneath the authoring collection: a fresh temporary
// directory with a copy of the bundle, a read-only snapshot of the reviewed
// head, and an allowlisted environment. Bundle filtering decides what the
// reviewer is given; this context decides what else the process can pick up.
//
// The guarantees are deliberately narrow. The directory and environment are
// established by taba. Instruction, skill, hook and MCP exclusion is a client
// capability, and file-tool confinement is a client restriction, not an OS
// sandbox. Unexpected writes are detected after each phase, not prevented.

const (
	reviewContextPrefix = "taba-review-ctx-"
	reviewContextMarker = ".taba-review-context"
	reviewContextRecord = "execution-context.json"
	reviewContextMaxAge = 24 * time.Hour
)

// Files and directories through which agent clients load instructions,
// settings, hooks, skills or MCP servers from a working directory's ancestors.
var reviewContextMarkers = []string{"AGENTS.md", "CLAUDE.md", "CLAUDE.local.md", "GEMINI.md", ".agents", ".claude", ".codex", ".cursor", ".grok", ".mcp.json"}

// Environment passed to every isolated review process when set. Everything
// else in the parent environment is dropped.
var reviewContextBaseEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "TZ", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"}

// Client authentication and provider selection, passed only to that client.
var reviewContextAuthEnv = map[string][]string{
	"claude": {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "ANTHROPIC_VERTEX_PROJECT_ID", "CLOUD_ML_REGION", "AWS_PROFILE", "AWS_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS"},
}

// Client options the public launch depends on: the context exclusions plus
// non-interactive permission handling. The capability check refuses a client
// whose help does not offer every one of them.
var reviewClaudeContextFlags = []string{"--safe-mode", "--restricted", "--strict-mcp-config", "--no-session-persistence", "--permission-prompts"}

type reviewExecContext struct {
	root, bundle, runner, source, tmp string
	collection                        string
	sourceSHA, sourceTree             string
	baseline, initial                 map[string]string
	custom                            bool
	extraEnv                          []string
	untrusted                         bool

	mu      sync.Mutex
	clients map[string]string
	record  reviewContextRecordFile
}

type reviewContextRecordFile struct {
	Version     int                 `json:"version"`
	Mode        string              `json:"mode"`
	WorkingDir  string              `json:"working_directory"`
	Environment reviewContextEnvRec `json:"environment"`
	Clients     []reviewClientRec   `json:"clients"`
	Source      reviewSourceRec     `json:"source"`
	Audit       reviewAuditRec      `json:"bundle_audit"`
	Cleanup     string              `json:"cleanup"`
	Limits      []string            `json:"limits"`
}

type reviewContextEnvRec struct {
	Policy string   `json:"policy"`
	Names  []string `json:"names"`
}

type reviewClientRec struct {
	Agent     string `json:"agent"`
	Isolation string `json:"isolation"`
}

type reviewSourceRec struct {
	Commit     string `json:"commit"`
	Snapshot   string `json:"snapshot"`
	TreeBefore string `json:"tree_sha256_before"`
	TreeAfter  string `json:"tree_sha256_after,omitempty"`
	Unchanged  bool   `json:"unchanged"`
}

type reviewAuditRec struct {
	Checked    []string `json:"checked_phases"`
	Unexpected []string `json:"unexpected_writes"`
}

// reviewPublicClient refuses, before anything runs, a client that cannot
// establish the public execution context.
func reviewPublicClient(agent string) error {
	if envcompat.Getenv("WTC_REVIEW_AGENT_CMD") != "" {
		return nil
	}
	switch agent {
	case "claude":
		return reviewClaudeCapability()
	case "grok":
		return fmt.Errorf("public review needs an isolated execution context, and grok loads user-level instructions and skills with no option to exclude them; use a claude:<model> spec for public review (for example --strong claude:opus), or set WTC_REVIEW_AGENT_CMD to a launcher that establishes that isolation itself")
	case "codex":
		return fmt.Errorf("public review needs an isolated execution context, and taba cannot establish one for codex: its supported options do not exclude user-level instructions and skills; use a claude:<model> spec for public review (for example --strong claude:opus), or set WTC_REVIEW_AGENT_CMD to a launcher that establishes that isolation itself")
	}
	return fmt.Errorf("unsupported review agent %q", agent)
}

var reviewClaudeHelp struct {
	once sync.Once
	err  error
}

func reviewClaudeCapability() error {
	reviewClaudeHelp.once.Do(func() {
		out, err := exec.Command("claude", "--help").CombinedOutput()
		if err != nil {
			reviewClaudeHelp.err = fmt.Errorf("public review cannot check the claude client: %v", err)
			return
		}
		var missing []string
		for _, flag := range reviewClaudeContextFlags {
			if !bytes.Contains(out, []byte(flag)) {
				missing = append(missing, flag)
			}
		}
		if len(missing) > 0 {
			reviewClaudeHelp.err = fmt.Errorf("public review needs claude options %s to exclude automatically loaded context; update the claude client", strings.Join(missing, ", "))
		}
	})
	return reviewClaudeHelp.err
}

// reviewSpecAgents lists the agents named by agent:model chains.
func reviewSpecAgents(specs ...string) []string {
	seen := map[string]bool{}
	var agents []string
	for _, spec := range specs {
		for _, entry := range strings.Fields(strings.ReplaceAll(spec, ",", " ")) {
			agent := strings.SplitN(entry, ":", 2)[0]
			if !seen[agent] {
				seen[agent] = true
				agents = append(agents, agent)
			}
		}
	}
	return agents
}

func newReviewExecContext(collection, bundle string, manifest ReviewManifest) (*reviewExecContext, error) {
	sweepReviewContexts()
	root, err := os.MkdirTemp("", reviewContextPrefix)
	if err != nil {
		return nil, fmt.Errorf("public review context: %w", err)
	}
	x := &reviewExecContext{root: root, collection: collection, clients: map[string]string{}, custom: envcompat.Getenv("WTC_REVIEW_AGENT_CMD") != ""}
	if err := x.prepare(bundle, manifest); err != nil {
		x.remove()
		return nil, err
	}
	return x, nil
}

func (x *reviewExecContext) prepare(bundle string, manifest ReviewManifest) error {
	real, err := filepath.EvalSymlinks(x.root)
	if err != nil {
		return err
	}
	x.root = real
	if err := checkReviewContextLocation(x.root, x.collection); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(x.root, reviewContextMarker), nil, 0644); err != nil {
		return err
	}
	x.bundle, x.runner, x.source, x.tmp = filepath.Join(x.root, "bundle"), filepath.Join(x.root, "runner"), filepath.Join(x.root, "source"), filepath.Join(x.root, "tmp")
	for _, dir := range []string{x.runner, x.tmp} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	if err := copyReviewTree(bundle, x.bundle, reviewPosterFiles); err != nil {
		return fmt.Errorf("public review context: %w", err)
	}
	// Runner-owned material lives outside every directory an agent can use.
	for _, dir := range reviewRunnerDirs {
		if err := os.Rename(filepath.Join(x.bundle, dir), filepath.Join(x.runner, dir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	commit, err := reviewGit(manifest.RepoDir, "rev-parse", "--verify", manifest.HeadSHA+"^{commit}")
	if err != nil {
		return fmt.Errorf("public review context: reviewed head %s is not available: %w", manifest.HeadSHA, err)
	}
	x.sourceSHA = strings.TrimSpace(string(commit))
	if !strings.HasPrefix(x.sourceSHA, manifest.HeadSHA) {
		return fmt.Errorf("public review context: head %s resolved to %s", manifest.HeadSHA, x.sourceSHA)
	}
	if err := snapshotReviewSource(manifest.RepoDir, x.sourceSHA, x.source); err != nil {
		return fmt.Errorf("public review context: source snapshot: %w", err)
	}
	if x.sourceTree, err = hashReviewTree(x.source); err != nil {
		return err
	}
	x.extraEnv = strings.Fields(strings.ReplaceAll(envcompat.Getenv("WTC_REVIEW_ENV"), ",", " "))
	mode := "isolated-public"
	if x.custom {
		mode = "public-operator-launcher"
	}
	x.record = reviewContextRecordFile{
		Version: 1, Mode: mode,
		WorkingDir:  "fresh temporary directory outside the collection; no agent configuration found in its ancestors",
		Environment: reviewContextEnvRec{Policy: "allowlist"},
		Source:      reviewSourceRec{Commit: x.sourceSHA, Snapshot: "read-only files of the reviewed head written from raw blob contents (no export attributes or filters); symlinks with empty, absolute or parent-step targets omitted; submodule contents not materialized", TreeBefore: x.sourceTree},
		Limits: []string{
			"client restrictions and file-tool confinement are not an OS sandbox",
			"unexpected writes are detected after each phase, not prevented",
		},
	}
	if x.custom {
		x.record.Limits = append(x.record.Limits, "an operator launcher ran the reviewers; taba did not establish client-level exclusion of instructions, skills, hooks or MCP servers")
	} else {
		x.record.Limits = append(x.record.Limits,
			"reviewers have file tools only: they read code but run no commands, builds or tests; check results are separate evidence",
			"client safe mode excludes project, local and user instructions, skills, hooks, plugins and MCP servers; administrator-managed settings and the hooks of managed plugins still apply (per the client's own help)")
	}
	if err := x.snapshotBundle(); err != nil {
		return err
	}
	x.initial = x.baseline
	return nil
}

// Posting state belongs to the poster, not to reviewers: it is neither copied
// into the context nor touched when results return.
var reviewPosterFiles = []string{"comment.id", "comment.create-pending", "inline-comments.json"}

// checkReviewContextLocation refuses a temporary directory beneath the
// collection or beneath a directory carrying agent configuration. The home
// directory's own configuration is user-level; for claude, the client options
// taba passes exclude it. An operator launcher is responsible for its own.
func checkReviewContextLocation(root, collection string) error {
	if collection != "" {
		if real, err := filepath.EvalSymlinks(collection); err == nil {
			collection = real
		}
		if root == collection || strings.HasPrefix(root, collection+string(filepath.Separator)) {
			return fmt.Errorf("public review context: the temporary directory is inside the collection; set TMPDIR to a directory outside it")
		}
	}
	home, _ := os.UserHomeDir()
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		if dir != home {
			for _, marker := range reviewContextMarkers {
				if _, err := os.Lstat(filepath.Join(dir, marker)); err == nil {
					return fmt.Errorf("public review context: %s above the temporary directory carries agent configuration (%s); set TMPDIR to a neutral directory", dir, marker)
				}
			}
		}
		if filepath.Dir(dir) == dir {
			return nil
		}
	}
}

// Directories the runner writes and agents never need.
var reviewRunnerDirs = []string{".prompts", ".logs", "stats"}

func (x *reviewExecContext) env(agent, statsPath string) []string {
	names := append([]string{}, reviewContextBaseEnv...)
	if !x.custom {
		names = append(names, reviewContextAuthEnv[agent]...)
	}
	names = append(names, x.extraEnv...)
	seen := map[string]bool{}
	var env, present []string
	for _, name := range names {
		if seen[name] || name == "TMPDIR" {
			continue
		}
		seen[name] = true
		value, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		if name == "PATH" {
			value = x.neutralPath(value)
		}
		env = append(env, name+"="+value)
		present = append(present, name)
	}
	env = append(env, "TMPDIR="+x.tmp, "WTC_REVIEW_STATS_FILE="+statsPath, "TABA_REVIEW_STATS_FILE="+statsPath)
	present = append(present, "TMPDIR", "WTC_REVIEW_STATS_FILE", "TABA_REVIEW_STATS_FILE")
	x.mu.Lock()
	for _, name := range present {
		if !containsString(x.record.Environment.Names, name) {
			x.record.Environment.Names = append(x.record.Environment.Names, name)
		}
	}
	x.mu.Unlock()
	return env
}

// neutralPath drops PATH entries inside the collection and relative entries,
// which would resolve against the neutral working directory.
func (x *reviewExecContext) neutralPath(path string) string {
	var keep []string
	for _, entry := range filepath.SplitList(path) {
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		if x.collection != "" && (entry == x.collection || strings.HasPrefix(entry, x.collection+string(filepath.Separator))) {
			continue
		}
		keep = append(keep, entry)
	}
	return strings.Join(keep, string(filepath.ListSeparator))
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// claudeArgs starts claude without automatically loaded instructions, skills,
// hooks, plugins, MCP servers or session persistence, with file tools confined
// to the bundle copy and the source snapshot, and edits of the snapshot denied.
func (x *reviewExecContext) claudeArgs(model string) []string {
	args := []string{"-p", "--output-format", "json"}
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, "--safe-mode", "--restricted", "--strict-mcp-config", "--no-session-persistence", "--permission-prompts", "none", "--permission-mode", "acceptEdits",
		"--add-dir", x.source, "--tools", "Read,Grep,Glob,Write,Edit",
		"--disallowedTools", claudeDenyEdits(x.source))
}

// claudeDenyEdits denies claude edits below dir. A single leading slash is
// project-relative in claude permission rules; "//" anchors the rule at the
// filesystem root. Edit rules cover every file-editing tool, Write included.
func claudeDenyEdits(dir string) string {
	return "Edit(/" + dir + "/**)"
}

func (x *reviewExecContext) noteClient(agent string) {
	isolation := "claude --safe-mode --restricted --strict-mcp-config --no-session-persistence; file tools only, confined by the client to the bundle copy and source snapshot; administrator-managed policy still applies"
	if x.custom {
		agent, isolation = "custom", "operator launcher; client-level isolation not established by taba (taba set the directory, environment and source snapshot only)"
	}
	x.mu.Lock()
	x.clients[agent] = isolation
	x.mu.Unlock()
}

// snapshotBundle records the agent-visible bundle before a phase.
func (x *reviewExecContext) snapshotBundle() error {
	files, err := reviewTreeFiles(x.bundle)
	x.baseline = files
	return err
}

// checkPhase compares the agent-visible bundle and the source snapshot with
// their state before the phase. Only allowed paths may change.
func (x *reviewExecContext) checkPhase(phase string, allowed func(rel string, existed, removed bool) bool) error {
	x.record.Audit.Checked = append(x.record.Audit.Checked, phase)
	// Fail closed: the copy is untrusted until this audit completes cleanly,
	// so an audit that cannot read a tree never lets results return.
	x.untrusted = true
	incomplete := func(err error) error {
		x.record.Audit.Unexpected = append(x.record.Audit.Unexpected, phase+" phase audit incomplete")
		return fmt.Errorf("public review context: %s phase audit could not complete: %w", phase, err)
	}
	tree, err := hashReviewTree(x.source)
	if err != nil {
		return incomplete(err)
	}
	x.record.Source.TreeAfter = tree
	x.record.Source.Unchanged = tree == x.sourceTree
	var unexpected []string
	if tree != x.sourceTree {
		unexpected = append(unexpected, "source snapshot")
	}
	now, err := reviewTreeFiles(x.bundle)
	if err != nil {
		return incomplete(err)
	}
	for rel, sum := range now {
		before, existed := x.baseline[rel]
		if (!existed || before != sum) && !allowed(rel, existed, false) {
			unexpected = append(unexpected, "bundle/"+rel)
		}
	}
	for rel := range x.baseline {
		if _, ok := now[rel]; !ok && !allowed(rel, true, true) {
			unexpected = append(unexpected, "bundle/"+rel+" (removed)")
		}
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		x.record.Audit.Unexpected = append(x.record.Audit.Unexpected, unexpected...)
		return fmt.Errorf("public review context: %s phase changed files outside its outputs: %s", phase, strings.Join(unexpected, ", "))
	}
	if err := x.snapshotBundle(); err != nil {
		return incomplete(err)
	}
	x.untrusted = false
	return nil
}

// finish copies the run back into the bundle and removes the context. After
// an audit failure the agent-written bundle copy is not trusted: only runner
// evidence returns, and any earlier summary and verdict are removed so they
// cannot be posted for this run.
func (x *reviewExecContext) finish(bundle string) error {
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if !x.untrusted {
		keep(x.returnResults(bundle))
	} else {
		for _, name := range []string{"summary.md", "verdict"} {
			if err := os.Remove(filepath.Join(bundle, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				keep(err)
			}
		}
	}
	for _, dir := range reviewRunnerDirs {
		keep(os.RemoveAll(filepath.Join(bundle, dir)))
		if _, err := os.Stat(filepath.Join(x.runner, dir)); err == nil {
			keep(copyReviewTree(filepath.Join(x.runner, dir), filepath.Join(bundle, dir), nil))
		}
	}
	x.record.Cleanup = "removed"
	if err := x.remove(); err != nil {
		x.record.Cleanup = "pending: the next public review run retries removal"
	}
	keep(x.writeRecord(bundle))
	return firstErr
}

func (x *reviewExecContext) writeRecord(bundle string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	var agents []string
	for agent := range x.clients {
		agents = append(agents, agent)
	}
	sort.Strings(agents)
	x.record.Clients = nil
	for _, agent := range agents {
		x.record.Clients = append(x.record.Clients, reviewClientRec{Agent: agent, Isolation: x.clients[agent]})
	}
	sort.Strings(x.record.Environment.Names)
	data, err := json.MarshalIndent(x.record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(bundle, reviewContextRecord), append(data, '\n'), 0644)
}

// report is the public, path-free provenance line of a summary.
func (x *reviewExecContext) report() string {
	if x.custom {
		return fmt.Sprintf("Execution context: public review through an operator launcher. taba started it in a fresh directory outside the authoring collection with an allowlisted environment and a read-only snapshot of `%s`, and audited its writes (source unchanged: %t); client-level isolation was not established by taba.", shortSHA(x.sourceSHA), x.record.Source.Unchanged)
	}
	return fmt.Sprintf("Execution context: isolated public review. Concern and lead processes ran in a fresh directory outside the authoring collection with an allowlisted environment and without project or user agent configuration, read a read-only snapshot of `%s` with file tools only (no commands or tests were run), and their writes were audited (source unchanged: %t). Client restrictions are not an OS sandbox; administrator-managed client policy still applies.", shortSHA(x.sourceSHA), x.record.Source.Unchanged)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func (x *reviewExecContext) remove() error {
	return removeReviewContext(x.root)
}

func removeReviewContext(root string) error {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0700)
		}
		return nil
	})
	return os.RemoveAll(root)
}

// sweepReviewContexts retries removal of stale contexts that an interrupted
// or failed cleanup left behind, bounded in count and to marked directories.
func sweepReviewContexts() {
	paths, _ := filepath.Glob(filepath.Join(os.TempDir(), reviewContextPrefix+"*"))
	if len(paths) > 32 {
		paths = paths[:32]
	}
	for _, path := range paths {
		info, err := os.Stat(filepath.Join(path, reviewContextMarker))
		if err == nil && time.Since(info.ModTime()) > reviewContextMaxAge {
			_ = removeReviewContext(path)
		}
	}
}

// snapshotReviewSource writes the reviewed head's files read-only from raw
// blob contents in the object database. git archive is not used because it
// applies export-ignore and export-subst, and checkout would run repository
// filters. It is not the complete tree: symlinks with empty, absolute or
// parent-step targets are omitted, and submodules become empty directories without their
// contents.
func snapshotReviewSource(repo, commit, dest string) error {
	list, err := reviewGit(repo, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return err
	}
	type entry struct{ mode, sha, path string }
	var entries []entry
	for _, record := range strings.Split(string(list), "\x00") {
		meta, path, ok := strings.Cut(record, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return fmt.Errorf("unexpected tree entry %q", record)
		}
		clean := filepath.Clean(filepath.FromSlash(path))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe tree path %q", path)
		}
		if fields[1] == "commit" {
			// A submodule is recorded by commit; its content is not part of this tree.
			if err := os.MkdirAll(filepath.Join(dest, clean), 0755); err != nil {
				return err
			}
			continue
		}
		entries = append(entries, entry{fields[0], fields[2], clean})
	}
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	cmd := exec.Command("git", "-C", repo, "cat-file", "--batch")
	var input bytes.Buffer
	for _, e := range entries {
		input.WriteString(e.sha + "\n")
	}
	cmd.Stdin = &input
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	writeErr := func() error {
		r := bufio.NewReader(out)
		for _, e := range entries {
			header, err := r.ReadString('\n')
			if err != nil {
				return err
			}
			fields := strings.Fields(header)
			if len(fields) != 3 || fields[0] != e.sha || fields[1] != "blob" {
				return fmt.Errorf("unexpected object for %s: %q", e.path, strings.TrimSpace(header))
			}
			size, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				return err
			}
			body := make([]byte, size)
			if _, err := io.ReadFull(r, body); err != nil {
				return err
			}
			if _, err := r.ReadByte(); err != nil {
				return err
			}
			path := filepath.Join(dest, e.path)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			switch e.mode {
			case "120000":
				if reviewLinkEscapes(string(body)) {
					continue
				}
				if err := os.Symlink(string(body), path); err != nil {
					return err
				}
			default:
				if err := os.WriteFile(path, body, 0644); err != nil {
					return err
				}
			}
		}
		return nil
	}()
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git cat-file: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	if writeErr != nil {
		return writeErr
	}
	// Read-only after writing: directories last, so files can be written.
	var dirs []string
	err = filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		return os.Chmod(path, 0444)
	})
	for i := len(dirs) - 1; i >= 0 && err == nil; i-- {
		err = os.Chmod(dirs[i], 0555)
	}
	return err
}

// reviewLinkEscapes reports a link the snapshot omits: an empty or absolute
// target, or one with a parent step. The links kept cannot form a chain that
// resolves outside the snapshot.
func reviewLinkEscapes(target string) bool {
	if target == "" || filepath.IsAbs(target) {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(target), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// reviewTreeFiles hashes every regular file below root by relative path.
// Symlinks are recorded by target; other file types are recorded as
// "special", which no phase allows.
func reviewTreeFiles(root string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			files[rel] = "link:" + target
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			files[rel] = fmt.Sprintf("%o:%s", info.Mode().Perm(), hex.EncodeToString(sum[:]))
		default:
			files[rel] = "special"
		}
		return nil
	})
	return files, err
}

func hashReviewTree(root string) (string, error) {
	files, err := reviewTreeFiles(root)
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		fmt.Fprintf(h, "%s\x00%s\n", key, files[key])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyReviewTree copies regular files and directories. A public bundle has
// no reason to carry links, so a link is refused rather than followed.
func copyReviewTree(src, dest string, skip []string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if containsString(skip, filepath.ToSlash(rel)) {
			return nil
		}
		target := filepath.Join(dest, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0755)
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0644)
		}
		return fmt.Errorf("%s is not a regular file or directory", rel)
	})
}

// returnResults applies this run's own changes to the caller's bundle: files
// that changed in the context since it was copied. A bundle file that changed
// meanwhile outside the run is never overwritten or removed; that conflict
// fails the run instead. Runner directories return separately; poster state
// is never touched.
func (x *reviewExecContext) returnResults(bundle string) error {
	now, err := reviewTreeFiles(x.bundle)
	if err != nil {
		return err
	}
	have, err := reviewTreeFiles(bundle)
	if err != nil {
		return err
	}
	protected := func(rel string) bool {
		top := strings.SplitN(rel, "/", 2)[0]
		return containsString(reviewRunnerDirs, top) || containsString(reviewPosterFiles, rel)
	}
	var conflicts []string
	changed := map[string]bool{}
	for rel, sum := range now {
		if x.initial[rel] != sum {
			changed[rel] = true
		}
	}
	for rel := range x.initial {
		if _, ok := now[rel]; !ok {
			changed[rel] = true
		}
	}
	for rel := range changed {
		if protected(rel) {
			continue
		}
		if have[rel] != x.initial[rel] {
			conflicts = append(conflicts, rel)
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return fmt.Errorf("public review context: %s changed in the bundle during the run; results were not copied over it", strings.Join(conflicts, ", "))
	}
	for rel := range changed {
		if protected(rel) {
			continue
		}
		path := filepath.Join(bundle, filepath.FromSlash(rel))
		if _, ok := now[rel]; !ok {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		data, err := os.ReadFile(filepath.Join(x.bundle, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			return err
		}
	}
	return nil
}
