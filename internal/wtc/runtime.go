package wtc

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RuntimeItem is a repository-owned dekit task. Ready means its startup probe
// passed, not that it has continuous health monitoring.
type RuntimeItem struct {
	DesiredDefinitionID string `json:"desired_definition_id,omitempty"`
	TunnelMode          string `json:"tunnel_mode,omitempty"`
	ManagedTunnel       bool   `json:"managed_tunnel,omitempty"`
	TunnelName          string `json:"tunnel_name,omitempty"`
	Registered          bool   `json:"registered,omitempty"`
	RegistrationURL     string `json:"registration_url,omitempty"`
	OperationID         string `json:"operation_id,omitempty"`
	DefinitionID        string `json:"definition_id,omitempty"`
	// runnerStarting distinguishes runner readiness from an observed task state.
	runnerStarting bool

	Collection string `json:"collection"`
	Repo       string `json:"repo"`
	Dir        string `json:"dir"`
	Path       string `json:"path"`
	Group      string `json:"group"`
	Kind       string `json:"kind"`
	Label      string `json:"label,omitempty"`
	URL        string `json:"url,omitempty"`
	// Provider is the tunnel client of an endpoint, such as tailscale.
	Provider string `json:"provider,omitempty"`
	// Exposure is how far an endpoint reaches (Exposure* constants), and
	// ExposureBasis how wtc knows it.
	Exposure      string `json:"exposure,omitempty"`
	ExposureBasis string `json:"exposure_basis,omitempty"`
	// Source is the task file the task comes from: the repo's committed one or
	// this collection's local one.
	Source string `json:"source,omitempty"`
	// PortEnv names the variable holding a service's local port.
	PortEnv string `json:"port_env,omitempty"`
	// Type is what a resource is, such as postgres.
	Type string `json:"type,omitempty"`
	// Autostart tasks start with wtc up; status shows such a tunnel before it runs.
	Autostart bool `json:"autostart,omitempty"`
	// Mark is the one-character name status shows for the task.
	Mark     string `json:"mark,omitempty"`
	State    string `json:"state"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Signal   *int   `json:"signal,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Error    string `json:"error,omitempty"`
	// Answering is what a live look at a ready task's address found: "yes",
	// or "no" with AnswerDetail saying what came back. Readiness is only a
	// startup probe; this is now.
	Answering    string `json:"answering,omitempty"`
	AnswerDetail string `json:"answer_detail,omitempty"`
	// Login says why the tunnel provider's client cannot serve (logged out,
	// stopped, awaiting approval, an expired key), and how to fix it.
	Login string `json:"login,omitempty"`
	// TunnelIntent is the explicit collection choice: on, off, stale, or empty
	// when no choice exists.
	TunnelIntent string `json:"tunnel_intent,omitempty"`
	// Lifecycle describes a missing intended tunnel or a changed declaration
	// that requires a fresh choice.
	Lifecycle string `json:"lifecycle,omitempty"`
}

type runtimeTask struct {
	RuntimeItem
	Log   string   `json:"log"`
	Ready bool     `json:"ready"`
	Deps  []string `json:"deps,omitempty"`
	// Spec is a digest of the task's generated definition; a running task
	// whose spec changes is restarted when the config is applied live.
	Spec string `json:"spec,omitempty"`
	// Definition binds approval to native configuration and endpoint metadata
	// before explicit intent overrides autostart.
	Definition string `json:"definition,omitempty"`
	// URLLog finds an endpoint URL that is only known after startup.
	URLLog string `json:"url_log,omitempty"`
}

type runtimeResource struct {
	Dir   string `json:"dir"`
	Repo  string `json:"repo"`
	State string `json:"state"`
}

// runtimeDeclaredResource names what a repo's resource hooks manage.
type runtimeDeclaredResource struct {
	Dir   string `json:"dir"`
	Repo  string `json:"repo"`
	Name  string `json:"name"`
	Type  string `json:"type,omitempty"`
	Mark  string `json:"mark,omitempty"`
	Label string `json:"label,omitempty"`
}

type runtimeManifest struct {
	TunnelIntents map[string]runtimeTunnelIntent `json:"tunnel_intents,omitempty"`
	Schema        int                            `json:"schema"`
	Root          string                         `json:"root"`
	Binary        string                         `json:"binary"`
	Digest        string                         `json:"digest"`
	Tasks         []runtimeTask                  `json:"tasks"`
	Resources     []runtimeResource              `json:"resources,omitempty"`
	Declared      []runtimeDeclaredResource      `json:"declared,omitempty"`
}

type RuntimeResult struct {
	Root   string        `json:"root"`
	Action string        `json:"action"`
	Items  []RuntimeItem `json:"items"`
	// Logs holds the recent output of each task that failed or did not get
	// ready, so a build or startup error is in the failure itself.
	Logs []RuntimeLogTail `json:"logs,omitempty"`
}

type RuntimeLogTail struct {
	Path string `json:"path"`
	File string `json:"file"`
	Tail string `json:"tail"`
}

// runtimeLogTail returns the last lines of a task log, with control
// characters other than newlines and tabs removed.
func runtimeLogTail(path string, lines int) string {
	if runtimeRegular(path) != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const window = 64 << 10
	if info, err := f.Stat(); err == nil && info.Size() > window {
		if _, err := f.Seek(-window, io.SeekEnd); err != nil {
			return ""
		}
	}
	data, _ := io.ReadAll(io.LimitReader(f, window))
	text := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' && r != 0x7f && (r < 0x80 || r > 0x9f) {
			return r
		}
		return -1
	}, ansiEscape.ReplaceAllString(string(data), ""))
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	return strings.Join(all[max(0, len(all)-lines):], "\n")
}

var ansiEscape = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// The manifest records this root, so it is canonical: a collection reached
// through a symlinked parent must name the same runtime.
func (c *Context) runtimeRoot() string {
	collection := c.Collection
	if resolved, err := filepath.EvalSymlinks(collection); err == nil {
		collection = resolved
	}
	return filepath.Join(collection, runtimeHome(collection), "runtime")
}

var runtimeNamespaces = []string{".wtc", ".taba"}

// runtimeHome selects one namespace for the manifest, logs and local service
// declarations together, so a collection has one runner. A fresh collection
// uses the legacy name.
func runtimeHome(collection string) string {
	for _, namespace := range runtimeNamespaces {
		if runtimeStateIn(collection, namespace) {
			return namespace
		}
	}
	return runtimeNamespaces[0]
}

// An empty directory holds no state: rolling back a refused first service
// leaves one.
func runtimeStateIn(collection, namespace string) bool {
	for _, name := range []string{"runtime", "svc"} {
		path := filepath.Join(collection, namespace, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() {
			return true
		}
		if entries, err := os.ReadDir(path); err != nil || len(entries) > 0 {
			return true
		}
	}
	return false
}

// runtimeConflict refuses state in both namespaces: the other side may be a
// live runner or services the selected side would silently hide.
func (c *Context) runtimeConflict() error {
	for _, namespace := range runtimeNamespaces {
		if !runtimeStateIn(c.Collection, namespace) {
			return nil
		}
	}
	return fmt.Errorf("runtime state exists under both %s and %s in %s; keep one before continuing", runtimeNamespaces[0], runtimeNamespaces[1], c.Collection)
}

func runtimeRegular(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("expected a regular file: %s", path)
	}
	return nil
}

func (c *Context) runtimePathsSafe() error {
	if err := c.runtimeConflict(); err != nil {
		return err
	}
	for _, path := range []string{filepath.Dir(c.runtimeRoot()), c.runtimeRoot(), filepath.Join(filepath.Dir(c.runtimeRoot()), "svc")} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime directory must not be a symlink: %s", path)
		}
	}
	return nil
}

func (c *Context) runtimeManifest() (*runtimeManifest, error) {
	if err := c.runtimePathsSafe(); err != nil {
		return nil, err
	}
	path := filepath.Join(c.runtimeRoot(), "manifest.json")
	if err := runtimeRegular(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m runtimeManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if (m.Schema != 1 && m.Schema != 2) || m.Root != c.runtimeRoot() || !filepath.IsAbs(m.Binary) {
		return nil, fmt.Errorf("runtime manifest does not describe this collection")
	}
	for _, task := range m.Tasks {
		if task.Collection != filepath.Base(c.Collection) || filepath.Base(task.Dir) != task.Dir || !runtimeTaskPath(task.Dir) || !runtimeTaskPath(task.Path) ||
			!strings.HasPrefix(task.Path, task.Dir+"/") || task.Log != filepath.Join(m.Root, "logs", filepath.FromSlash(task.Path)+".log") {
			return nil, fmt.Errorf("invalid runtime task ownership")
		}
	}
	for _, resource := range m.Resources {
		if !runtimeTaskPath(resource.Dir) || filepath.Base(resource.Dir) != resource.Dir {
			return nil, fmt.Errorf("invalid runtime resource ownership")
		}
	}
	return &m, nil
}

// Refuse parent-directory symlinks for managed output paths.
func runtimeSafeParents(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("runtime path escapes its root")
	}
	parent := filepath.Dir(path)
	for {
		info, err := os.Lstat(parent)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("runtime output directory must not be a symlink: %s", parent)
		}
		if parent == root {
			break
		}
		parent = filepath.Dir(parent)
	}
	return nil
}

func runtimeOutputPathsSafe(m *runtimeManifest) error {
	for _, task := range m.Tasks {
		if err := runtimeSafeParents(m.Root, task.Log); err != nil {
			return err
		}
		if err := runtimeRegular(task.Log); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func runtimeTaskPath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, ":\\*?[]{}@+\n\r") {
		return false
	}
	for _, p := range strings.Split(path, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return true
}

// RuntimeStatus reads recorded tunnel facts and the existing runner. Provider
// credentials, accounts and daemon state are never probed by a read/status.
func (c *Context) RuntimeStatus() []RuntimeItem {
	items := runtimeTaskDiagnostics(c.runtimeStatusOnly())
	// Ready non-tunnel application addresses can be checked for answering.
	runtimeAnswering(items, runtimeAnswerCheck)
	return c.runtimeWithDeclarations(items)
}

// runtimeStatusOnly is the runner's view, without looking at addresses: what
// up's own wait loop polls, with its own reachability check and deadline.
func (c *Context) runtimeStatusOnly() []RuntimeItem {
	m, err := c.runtimeManifest()
	if err != nil {
		return []RuntimeItem{{Collection: filepath.Base(c.Collection), Path: "runtime", State: "unknown", Error: err.Error()}}
	}
	if m == nil {
		return nil
	}
	return c.runtimeStatus(m)
}

// RuntimeEnlisted lists recorded tasks as checking and newly discovered tasks
// as unconfigured, without asking the runner or executing task definitions.
func (c *Context) RuntimeEnlisted() []RuntimeItem {
	m, err := c.runtimeManifest()
	if err != nil || m == nil {
		return c.runtimeWithDeclarations(nil)
	}
	var items []RuntimeItem
	for _, task := range m.Tasks {
		item := task.RuntimeItem
		item.State, item.URL = "checking", ""
		if task.URLLog == "" {
			item.URL = task.URL
		}
		items = append(items, item)
	}
	return c.runtimeWithDeclarations(append(items, c.runtimeResourceItems(m, true)...))
}

// runtimeResourceItems shows what resource hooks manage: one item per
// declared resource, sharing its repo's hook state, or one unnamed item for a
// repo that declares none.
func (c *Context) runtimeResourceItems(m *runtimeManifest, checking bool) []RuntimeItem {
	var items []RuntimeItem
	recorded := map[string]bool{}
	add := func(dir, repo, state string) {
		if checking {
			state = "checking"
		}
		named := false
		for _, d := range m.Declared {
			if d.Dir != dir {
				continue
			}
			named = true
			label := d.Label
			if label == "" {
				label = strings.TrimSpace(d.Type + " resource")
			}
			items = append(items, RuntimeItem{Collection: filepath.Base(c.Collection), Repo: repo, Dir: dir, Path: dir + "/@resources/" + d.Name, Group: dir + "/" + d.Name, Kind: "resources", Type: d.Type, Mark: d.Mark, State: state, Label: label})
		}
		if !named {
			items = append(items, RuntimeItem{Collection: filepath.Base(c.Collection), Repo: repo, Dir: dir, Path: dir + "/@resources", Group: dir + "/resource-hook", Kind: "resources", State: state, Label: "resource hook; declare what it manages under resources: in the task file"})
		}
	}
	for _, resource := range m.Resources {
		recorded[resource.Dir] = true
		add(resource.Dir, resource.Repo, resource.State)
	}
	// Declared but never provisioned: up has not run the hook yet.
	seen := map[string]bool{}
	for _, d := range m.Declared {
		if !recorded[d.Dir] && !seen[d.Dir] {
			seen[d.Dir] = true
			add(d.Dir, d.Repo, "unprovisioned")
		}
	}
	return items
}

// runtimeTaskDiagnostics changes publication only; control decisions retain the
// existing raw runner observation until task discovery succeeds.
func runtimeTaskDiagnostics(items []RuntimeItem) []RuntimeItem {
	for i := range items {
		if items[i].runnerStarting {
			items[i].State = "checking"
			items[i].Reason = "runner reports starting; task state is unavailable"
		}
	}
	return items
}

func (c *Context) runtimeStatus(m *runtimeManifest) []RuntimeItem {
	items := make([]RuntimeItem, len(m.Tasks))
	items = append(items, c.runtimeResourceItems(m, false)...)
	defer func() { runtimeTunnelLifecycle(items) }()
	state, err := runtimeRunnerState(m)
	for i, task := range m.Tasks {
		items[i] = task.RuntimeItem
		if items[i].Kind == "endpoint" && !items[i].ManagedTunnel && items[i].DefinitionID == "" {
			items[i].DefinitionID = task.Definition
		}
		items[i].State = state
		items[i].runnerStarting = err == nil && state == "starting"
		if err != nil {
			items[i].State = "unknown"
			items[i].Error = err.Error()
		}
	}
	if err != nil || state != "running" {
		return items
	}
	defer func() {
		for i := range m.Tasks {
			if !items[i].ManagedTunnel {
				runtimeTunnelExposure(&items[i], nil)
			}
		}
	}()
	data, err := runtimeCommand(m, false, "ls")
	var response struct {
		Tasks []RuntimeItem `json:"tasks"`
	}
	if err == nil {
		err = json.Unmarshal(data, &response)
	}
	states := map[string]RuntimeItem{}
	for _, task := range response.Tasks {
		states[task.Path] = task
	}
	for i := range m.Tasks {
		item := &items[i]
		if err != nil {
			item.State = "unknown"
			item.Error = err.Error()
			continue
		}
		if task, ok := states[item.Path]; ok {
			item.State, item.ExitCode, item.Signal, item.Reason = task.State, task.ExitCode, task.Signal, task.Reason
			// Only a ready task's log is trusted: an append-mode log still holds earlier URLs.
			if pattern := m.Tasks[i].URLLog; pattern != "" && item.State == "ready" {
				if found := runtimeLogURL(m.Tasks[i].Log, pattern); found != "" {
					item.URL = found
				}
			}
		} else {
			item.State = "missing"
		}
	}
	return items
}

// runtimeLogURL returns the newest safe URL matching pattern near the end of a
// task log: the first capture group when there is one, otherwise the match.
func runtimeLogURL(path, pattern string) string {
	re, err := regexp.Compile(pattern)
	if err != nil || runtimeRegular(path) != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const window = 256 << 10
	if info, err := f.Stat(); err == nil && info.Size() > window {
		if _, err := f.Seek(-window, io.SeekEnd); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, window))
	if err != nil {
		return ""
	}
	matches := re.FindAllSubmatch(data, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		found := string(matches[i][len(matches[i])-1])
		if len(matches[i]) > 1 {
			found = string(matches[i][1])
		}
		if runtimeURLSafe(found) == nil {
			return found
		}
	}
	return ""
}

func runtimeSelect(m *runtimeManifest, target string) (string, []runtimeTask, error) {
	if target == "" {
		return "", m.Tasks, nil
	}
	if !runtimeTaskPath(target) {
		return "", nil, fmt.Errorf("expected a repository, task, or group path")
	}
	var tasks []runtimeTask
	exact := false
	for _, task := range m.Tasks {
		if task.Path == target {
			exact = true
			tasks = []runtimeTask{task}
			break
		}
		if task.Path != target && strings.HasPrefix(task.Path, target+"/") {
			tasks = append(tasks, task)
		}
	}
	if len(tasks) == 0 {
		// A name without its repository, such as web.
		full, err := runtimeShortTarget(m, target)
		if err != nil {
			return "", nil, err
		}
		return runtimeSelect(m, full)
	}
	if exact {
		return target, tasks, nil
	}
	return target + "/**", tasks, nil
}

func (c *Context) RuntimeLogPath(target string) (string, error) {
	m, err := c.runtimeManifest()
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", fmt.Errorf("runtime is not configured")
	}
	_, tasks, err := runtimeSelect(m, target)
	if err != nil {
		return "", err
	}
	if len(tasks) > 1 {
		// A service name means its own process, not its tunnels.
		var services []runtimeTask
		for _, task := range tasks {
			if task.Kind != "endpoint" {
				services = append(services, task)
			}
		}
		if len(services) == 1 {
			tasks = services
		}
	}
	if len(tasks) != 1 {
		var paths []string
		for _, task := range tasks {
			paths = append(paths, task.Path)
		}
		return "", fmt.Errorf("name one of %s", strings.Join(paths, ", "))
	}
	path := tasks[0].Log
	if err := runtimeSafeParents(m.Root, path); err != nil {
		return "", err
	}
	if err := runtimeRegular(path); err != nil {
		return "", err
	}
	return path, nil
}
