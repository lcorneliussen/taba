package wtc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"taba.sh/taba/internal/branding"
)

// runtimeShortTarget resolves a target without its repository, such as web
// for ext.app/web, when exactly one repository has it.
func runtimeShortTarget(m *runtimeManifest, target string) (string, error) {
	dirs := map[string]bool{}
	for _, task := range m.Tasks {
		dir, rest, _ := strings.Cut(task.Path, "/")
		if rest == target || strings.HasPrefix(rest, target+"/") {
			dirs[dir] = true
		}
	}
	switch len(dirs) {
	case 0:
		return "", fmt.Errorf(branding.Text("unknown service %q; {{command}} svc lists them"), target)
	case 1:
		for dir := range dirs {
			return dir + "/" + target, nil
		}
	}
	var names []string
	for dir := range dirs {
		names = append(names, dir+"/"+target)
	}
	sort.Strings(names)
	return "", fmt.Errorf("%q is in several repositories; name one of %s", target, strings.Join(names, ", "))
}

// ServiceOptions describes a service to enlist with wtc svc add.
type ServiceOptions struct {
	Service      string // name or repo/name
	Cmd          []string
	PortEnv      string
	URLEnv       string
	ReadyLog     string
	ReadyTimeout time.Duration
	NoAutostart  bool
	Cwd          string
	Label        string
	Mark         string
	// Shared writes the repo's committed task file instead of this
	// collection's local one.
	Shared  bool
	NoStart bool
	Wait    time.Duration
}

type runtimeRepo struct{ dir, repo, path string }

func (c *Context) runtimeRepos() ([]runtimeRepo, error) {
	_, repos, err := c.CatchUpInventory(CatchUpOptions{DryRun: true})
	if err != nil {
		return nil, err
	}
	var out []runtimeRepo
	for _, r := range repos {
		if filepath.Clean(r.path) == filepath.Clean(c.Harness) {
			continue
		}
		out = append(out, runtimeRepo{dir: filepath.Base(r.path), repo: r.repo, path: r.path})
	}
	return out, nil
}

// runtimeServiceRepo splits repo/name, or finds the repository of a bare
// name: the only one in the collection, or the only one that has it.
func (c *Context) runtimeServiceRepo(service string, existing bool) (runtimeRepo, string, error) {
	repos, err := c.runtimeRepos()
	if err != nil {
		return runtimeRepo{}, "", err
	}
	dir, name, qualified := strings.Cut(service, "/")
	if !qualified {
		name, dir = service, ""
		if existing {
			if m, err := c.runtimeManifest(); err == nil && m != nil {
				if full, err := runtimeShortTarget(m, name); err == nil {
					dir, _, _ = strings.Cut(full, "/")
				}
			}
		}
		if dir == "" && len(repos) == 1 {
			dir = repos[0].dir
		}
		if dir == "" {
			var names []string
			for _, r := range repos {
				names = append(names, r.dir)
			}
			return runtimeRepo{}, "", fmt.Errorf("name the repository: <repo>/%s, one of %s", name, strings.Join(names, ", "))
		}
	}
	if !runtimeResourceName.MatchString(name) {
		return runtimeRepo{}, "", fmt.Errorf("service name %q: use lower-case letters, digits, - or _", name)
	}
	for _, r := range repos {
		if r.dir == dir {
			return r, name, nil
		}
	}
	return runtimeRepo{}, "", fmt.Errorf("no repository %q in this collection", dir)
}

// runtimeEditFragment changes the tasks of a task file, creating it when
// needed, keeping its comments and order.
func runtimeEditFragment(path string, edit func(tasks *yaml.Node) error) error {
	var doc yaml.Node
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err := runtimeRegular(path); err != nil {
			return err
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected a mapping", path)
	}
	var tasks *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "tasks" {
			tasks = root.Content[i+1]
		}
	}
	if tasks == nil {
		tasks = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "tasks"}, tasks)
	}
	if tasks.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: tasks must be a mapping", path)
	}
	tasks.Style = 0
	if err := edit(tasks); err != nil {
		return err
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, out.Bytes(), 0644)
}

func runtimeAddTask(tasks *yaml.Node, name string, value map[string]any) error {
	for i := 0; i+1 < len(tasks.Content); i += 2 {
		if tasks.Content[i].Value == name {
			return fmt.Errorf("task %s already exists", name)
		}
	}
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return err
	}
	tasks.Content = append(tasks.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: name}, &node)
	return nil
}

// runtimeChange edits a task file, checks that the collection's services
// still render, and puts the file back when they do not.
func (c *Context) runtimeChange(path string, edit func(tasks *yaml.Node) error) error {
	if err := c.runtimePathsSafe(); err != nil {
		return err
	}
	before, readErr := os.ReadFile(path)
	if err := runtimeEditFragment(path, edit); err != nil {
		return err
	}
	// The real binary also resolves url_env and port_env, so that a
	// variable that does not resolve is refused now, not on every later up.
	binary, err := c.runtimeBinary()
	if err != nil {
		binary = "dekit"
	}
	if _, _, err := c.runtimePlan(binary); err != nil {
		if readErr == nil {
			_ = os.WriteFile(path, before, 0644)
		} else {
			_ = os.Remove(path)
		}
		return err
	}
	return nil
}

func runtimeFragmentFor(c *Context, repo runtimeRepo, shared bool) string {
	if shared {
		return runtimeSharedFragment(repo.path)
	}
	return c.runtimeLocalFragment(repo.dir)
}

// AddService enlists a service and, unless told otherwise, starts it.
func (c *Context) AddService(opt ServiceOptions) (RuntimeResult, error) {
	if len(opt.Cmd) == 0 {
		return RuntimeResult{}, errors.New(branding.Text("give the command after --, such as {{command}} svc add web -- npm run dev"))
	}
	repo, name, err := c.runtimeServiceRepo(opt.Service, false)
	if err != nil {
		return RuntimeResult{}, err
	}
	if plan, _, err := c.runtimePlan("dekit"); err == nil {
		for _, task := range plan.Tasks {
			if task.Group == repo.dir+"/"+name {
				return RuntimeResult{}, fmt.Errorf("%s/%s is already enlisted (%s)", repo.dir, name, task.Source)
			}
		}
	}
	task := map[string]any{"cmd": opt.Cmd, "autostart": !opt.NoAutostart}
	if opt.Label != "" {
		task["label"] = opt.Label
	}
	if opt.Cwd != "" {
		task["cwd"] = opt.Cwd
	}
	meta := map[string]any{}
	for key, value := range map[string]string{"port_env": opt.PortEnv, "url_env": opt.URLEnv, "mark": opt.Mark} {
		if value != "" {
			meta[key] = value
		}
	}
	if len(meta) > 0 {
		task["x-wtc"] = meta
	}
	timeout := opt.ReadyTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	// With only --port-env, readiness is the port accepting connections,
	// resolved per collection when the services render.
	if opt.ReadyLog != "" {
		task["ready"] = map[string]any{"log": opt.ReadyLog, "timeout": runtimeDekitDuration(timeout)}
	} else if opt.PortEnv != "" && timeout != 60*time.Second {
		task["ready"] = map[string]any{"timeout": runtimeDekitDuration(timeout)}
	}
	path := runtimeFragmentFor(c, repo, opt.Shared)
	if err := c.runtimeChange(path, func(tasks *yaml.Node) error { return runtimeAddTask(tasks, name+"/main", task) }); err != nil {
		return RuntimeResult{}, err
	}
	if opt.NoStart {
		return c.RenderRuntime(false)
	}
	return c.RuntimeAction("up", repo.dir+"/"+name, opt.Wait)
}

// RemoveService stops a service with its tunnels and removes it from its task
// file. A service from the repo's committed file needs shared: that is a repo
// change.
func (c *Context) RemoveService(service string, shared bool) (RuntimeResult, error) {
	defer c.signalServicesChanged()
	if err := c.runtimeConflict(); err != nil {
		return RuntimeResult{}, err
	}
	repo, name, err := c.runtimeServiceRepo(service, true)
	if err != nil {
		return RuntimeResult{}, err
	}
	plan, _, err := c.runtimePlan("dekit")
	if err != nil {
		return RuntimeResult{}, err
	}
	files := map[string][]string{}
	others := 0
	for _, task := range plan.Tasks {
		if task.Group != repo.dir+"/"+name {
			others++
			continue
		}
		if task.Source == runtimeSharedFragment(repo.path) && !shared {
			return RuntimeResult{}, fmt.Errorf("%s comes from %s, which the repository commits; pass --shared to remove it there", task.Path, task.Source)
		}
		files[task.Source] = append(files[task.Source], strings.TrimPrefix(task.Path, repo.dir+"/"))
	}
	if len(files) == 0 {
		return RuntimeResult{}, fmt.Errorf(branding.Text("unknown service %s/%s; {{command}} svc lists them"), repo.dir, name)
	}
	if others == 0 {
		// Nothing would be left to run; stop everything first, if it ever ran.
		if m, err := c.runtimeManifest(); err != nil {
			return RuntimeResult{}, err
		} else if m != nil {
			if _, err := c.RuntimeAction("down", "", 0); err != nil {
				return RuntimeResult{}, err
			}
		}
	}
	for path, names := range files {
		if err := runtimeEditFragment(path, func(tasks *yaml.Node) error {
			var kept []*yaml.Node
			for i := 0; i+1 < len(tasks.Content); i += 2 {
				drop := false
				for _, n := range names {
					drop = drop || tasks.Content[i].Value == n
				}
				if !drop {
					kept = append(kept, tasks.Content[i], tasks.Content[i+1])
				}
			}
			tasks.Content = kept
			return nil
		}); err != nil {
			return RuntimeResult{}, err
		}
	}
	if others == 0 {
		// No task is left to render. Keep the manifest for the resources it
		// records, which retirement must still tear down, without the tasks.
		err := c.withRuntimeLock(func() error {
			m, err := c.runtimeManifest()
			if err != nil || m == nil {
				return err
			}
			m.Tasks = []runtimeTask{}
			for path, intent := range m.TunnelIntents {
				intent.Definition = ""
				m.TunnelIntents[path] = intent
			}
			return runtimeSaveManifest(m)
		})
		return RuntimeResult{Root: c.runtimeRoot(), Action: "rm", Items: c.RuntimeStatus()}, err
	}
	if m, err := c.runtimeManifest(); err != nil || m == nil {
		return RuntimeResult{Root: c.runtimeRoot(), Action: "rm", Items: []RuntimeItem{}}, err
	}
	return c.RenderRuntime(false)
}

// runtimeTunnelTemplate is the client command for a provider, reading the
// service's port from its variable.
func runtimeTunnelTemplate(provider, portEnv string, public bool) (cmd, ready string, err error) {
	port := `"$` + portEnv + `"`
	local := `"http://127.0.0.1:$` + portEnv + `"`
	switch provider {
	case "tailscale":
		if public {
			return "", "", fmt.Errorf("tailscale tunnels are tailnet-only; use cloudflare, ngrok or devtunnel for a public link")
		}
		return "exec tailscale serve --https=" + port + " " + local, "Available within your tailnet", nil
	case "cloudflare":
		return "exec cloudflared tunnel --no-autoupdate --url " + local, "trycloudflare.com", nil
	case "ngrok":
		return "exec ngrok http " + port + " --log stdout", "started tunnel", nil
	case "devtunnel":
		cmd := "exec devtunnel host -p " + port
		if public {
			cmd += " --allow-anonymous"
		}
		return cmd, "Connect via browser", nil
	}
	return "", "", fmt.Errorf("unknown tunnel provider %q", provider)
}

// TunnelOptions opens or closes a tunnel for a service.
type TunnelOptions struct {
	Service  string
	Provider string
	Public   bool
	Stop     bool
	Wait     time.Duration
}

// Tunnel starts a service's tunnel through a provider, adding one to this
// collection's task file when the service has none. A tunnel reachable from
// the internet opens only with Public.
func (c *Context) Tunnel(opt TunnelOptions) (RuntimeResult, error) {
	repo, name, err := c.runtimeServiceRepo(opt.Service, true)
	if err != nil {
		return RuntimeResult{}, err
	}
	group := repo.dir + "/" + name
	binary, err := c.runtimeBinary()
	if err != nil {
		return RuntimeResult{}, err
	}
	plan, _, err := c.runtimePlan(binary)
	if err != nil {
		return RuntimeResult{}, err
	}
	var service *runtimeTask
	var tunnels []runtimeTask
	for i, task := range plan.Tasks {
		if task.Group != group {
			continue
		}
		if task.Kind == "endpoint" {
			if opt.Provider == "" || task.Provider == opt.Provider {
				tunnels = append(tunnels, task)
			}
		} else if service == nil {
			service = &plan.Tasks[i]
		}
	}
	if service == nil {
		return RuntimeResult{}, fmt.Errorf(branding.Text("unknown service %s; {{command}} svc lists them"), group)
	}
	if opt.Stop {
		if len(tunnels) == 0 {
			return RuntimeResult{}, fmt.Errorf("%s has no %s tunnel", group, opt.Provider)
		}
		var result RuntimeResult
		var stopErr error
		for _, t := range tunnels {
			result, err = c.runtimeAction("down", t.Path, 0, &runtimeTunnelIntent{On: false, Definition: t.Definition})
			stopErr = errors.Join(stopErr, err)
		}
		return result, stopErr
	}
	if opt.Provider == "" && len(tunnels) != 1 {
		return RuntimeResult{}, fmt.Errorf("name the provider: tailscale, cloudflare, ngrok or devtunnel")
	}
	provider := opt.Provider
	if provider == "" {
		provider = tunnels[0].Provider
	}
	// A client that is not signed in starts a host that cannot serve.
	if missing := runtimeLoginCheck(provider); missing != "" {
		return RuntimeResult{}, fmt.Errorf("%s; then open the tunnel again", missing)
	}
	if len(tunnels) == 0 {
		if service.PortEnv == "" {
			return RuntimeResult{}, fmt.Errorf("%s has no port variable; enlist it with --port-env or set x-wtc port_env", service.Path)
		}
		cmd, ready, err := runtimeTunnelTemplate(opt.Provider, service.PortEnv, opt.Public)
		if err != nil {
			return RuntimeResult{}, err
		}
		if p, ok := runtimeTunnelProviderNamed(opt.Provider); ok && p.command(cmd) == ExposurePublic && !opt.Public {
			return RuntimeResult{}, fmt.Errorf("a %s tunnel is reachable from the internet; pass --public to open it", opt.Provider)
		}
		task := map[string]any{
			"label": opt.Provider + " tunnel",
			"cmd":   []string{"sh", "-c", cmd},
			"deps":  []string{strings.TrimPrefix(service.Path, repo.dir+"/")},
			"tags":  []string{"endpoint"},
			"ready": map[string]any{"log": ready, "timeout": "60s"},
			"x-wtc": map[string]any{"tunnel": opt.Provider},
		}
		if err := c.runtimeChange(c.runtimeLocalFragment(repo.dir), func(tasks *yaml.Node) error {
			return runtimeAddTask(tasks, name+"/"+opt.Provider, task)
		}); err != nil {
			return RuntimeResult{}, err
		}
		if plan, _, err = c.runtimePlan(binary); err != nil {
			return RuntimeResult{}, err
		}
		for _, t := range plan.Tasks {
			if t.Path == group+"/"+opt.Provider {
				tunnels = []runtimeTask{t}
			}
		}
	}
	tunnel := tunnels[0]
	exposed := true
	switch tunnel.Exposure {
	case ExposureTailnet, ExposureAccount, ExposureProtected:
		exposed = false
	}
	if exposed && !opt.Public {
		return RuntimeResult{}, fmt.Errorf("%s is reachable from the internet (%s); pass --public to open it", tunnel.Path, runtimeExposureWords(tunnel.Exposure))
	}
	return c.runtimeAction("up", tunnel.Path, opt.Wait, &runtimeTunnelIntent{On: true, Definition: tunnel.Definition})
}

func runtimeExposureWords(exposure string) string {
	if exposure == "" || exposure == ExposureUnknown {
		return "exposure unknown"
	}
	return exposure
}

// DekitCommand runs this collection's pinned dekit against its runner, with
// the collection environment, for what wtc svc does not cover.
func (c *Context) DekitCommand(args []string) (*exec.Cmd, error) {
	m, err := c.runtimeManifest()
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New(branding.Text("services are not configured; run {{command}} up"))
	}
	binary, err := c.runtimeBinary()
	if err != nil {
		return nil, err
	}
	collection := filepath.Dir(filepath.Dir(m.Root))
	argv := append([]string{"-c", `set -ea; [ ! -f "$1" ] || . "$1"; [ ! -f "$2" ] || . "$2"; shift 2; exec "$@"`, "wtc-dekit", filepath.Join(collection, ".env.collection"), filepath.Join(collection, ".env.collection.local"), binary, "-C", m.Root}, args...)
	cmd := exec.Command("/bin/sh", argv...)
	cmd.Dir = m.Root
	return cmd, nil
}

// runtimeDekitDuration writes a duration the way dekit reads it: 60s, 500ms.
func runtimeDekitDuration(d time.Duration) string {
	if d%time.Second != 0 {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// statusSignalPath is touched after each wtc svc action so that a running
// status view reads the service states at once instead of on its next poll.
func (c *Context) statusSignalPath() string {
	return filepath.Join(c.runtimeRoot(), "services-changed")
}

func (c *Context) signalServicesChanged() {
	// Deferred callers also reach here after refusal or partial failure.
	// Notify safe partial changes without writing through unsafe state.
	if c.runtimePathsSafe() != nil {
		return
	}
	if _, err := os.Lstat(c.runtimeRoot()); err != nil {
		return
	}
	path := c.statusSignalPath()
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return
		}
	} else if !os.IsNotExist(err) {
		return
	}
	// Replacing the timestamp file does not follow an existing link.
	_ = writeCatchUpFile(path, nil)
}

// ServicesChangedAt is when a wtc svc action last finished, or zero.
func (c *Context) ServicesChangedAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	info, err := os.Stat(c.statusSignalPath())
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
