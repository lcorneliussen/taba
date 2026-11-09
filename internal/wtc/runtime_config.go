package wtc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"taba.sh/taba/internal/branding"
	"time"
)

func runtimeURLSafe(value string) error {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("runtime URL must be HTTP(S) without credentials, query or fragment")
	}
	return nil
}

func runtimeURLFromEnv(collection, name string) (string, error) {
	value, err := runtimeEnvValue(collection, name)
	if err != nil {
		return "", err
	}
	if err := runtimeURLSafe(value); err != nil {
		return "", err
	}
	return value, nil
}

// runtimeEnvValue reads one variable from the collection env files.
func runtimeEnvValue(collection, name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `set -ea; [ ! -f "$1" ] || . "$1"; [ ! -f "$2" ] || . "$2"; printenv "$3"`, "wtc-runtime-url", filepath.Join(collection, ".env.collection"), filepath.Join(collection, ".env.collection.local"), name)
	cmd.Dir = collection
	var out statusLimitedBuffer
	out.limit = 4096
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("cannot read %s from the collection env: %w", name, err)
	}
	return strings.TrimSpace(out.String()), nil
}

func (c *Context) runtimeBinary() (string, error) {
	name := c.Config.Runtime.Binary
	if name == "" {
		name = "dekit"
	}
	if strings.ContainsRune(name, filepath.Separator) && !filepath.IsAbs(name) {
		// Resolve the harness, not the tool: the path feeds the config digest.
		harness := c.Harness
		if resolved, err := filepath.EvalSymlinks(harness); err == nil {
			harness = resolved
		}
		name = filepath.Join(harness, name)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("dekit 0.10.x is required; install the harness's pinned runtime tool: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	out, err := statusCommand(5*time.Second, path, "--version")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || fields[0] != "dekit" {
		return "", fmt.Errorf("expected dekit 0.10.x, got %q", strings.TrimSpace(string(out)))
	}
	v, err := semver.NewVersion(fields[1])
	if err != nil || v.Major() != 0 || v.Minor() != 10 {
		return "", fmt.Errorf("this runtime adapter requires dekit 0.10.x, got %s", fields[1])
	}
	return path, nil
}

// runtimePlan copies native dekit task definitions, rebasing only filesystem
// paths and dependency names. It neither runs hooks nor writes generated files.
func (c *Context) runtimePlan(binary string) (*runtimeManifest, []byte, error) {
	if err := c.runtimePathsSafe(); err != nil {
		return nil, nil, err
	}
	if c.Config.Runtime.Backend != "dekit" {
		return nil, nil, fmt.Errorf("enable [runtime] backend = \"dekit\" in harness/wtc.toml")
	}
	_, repos, err := c.CatchUpInventory(CatchUpOptions{DryRun: true})
	if err != nil {
		return nil, nil, err
	}
	m := &runtimeManifest{Schema: 1, Root: c.runtimeRoot(), Binary: binary, Tasks: []runtimeTask{}}
	native := map[string]any{}
	// The resolved port of each service, by group, for its tasks' env.
	ports := map[string][2]string{}
	for _, repo := range repos {
		dir := filepath.Base(repo.path)
		// The repo's committed tasks, then this collection's own additions.
		tasks := map[string]map[string]any{}
		source := map[string]string{}
		for _, path := range []string{runtimeSharedFragment(repo.path), c.runtimeLocalFragment(dir)} {
			if err := runtimeRegular(path); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return nil, nil, err
			}
			fragment, err := runtimeReadFragment(path)
			if err != nil {
				return nil, nil, err
			}
			for name, task := range fragment.Tasks {
				if _, dup := tasks[name]; dup {
					return nil, nil, fmt.Errorf("%s/%s is defined in both %s and %s", dir, name, source[name], path)
				}
				tasks[name], source[name] = task, path
			}
			for name, resource := range fragment.Resources {
				if !runtimeResourceName.MatchString(name) || resource.Mark != "" && !regexp.MustCompile(`^[A-Za-z0-9]$`).MatchString(resource.Mark) {
					return nil, nil, fmt.Errorf("%s: invalid resource %q", path, name)
				}
				m.Declared = append(m.Declared, runtimeDeclaredResource{Dir: dir, Repo: repo.repo, Name: name, Type: resource.Type, Mark: resource.Mark, Label: resource.Label})
			}
		}
		for local, task := range tasks {
			if !runtimeTaskPath(local) || task == nil {
				return nil, nil, fmt.Errorf("invalid task path %q in %s", local, source[local])
			}
			full := dir + "/" + local
			item := runtimeTask{RuntimeItem: RuntimeItem{Collection: filepath.Base(c.Collection), Repo: repo.repo, Dir: dir, Path: full, Group: dir + "/" + strings.Split(local, "/")[0], Kind: "service", State: "unconfigured"}}
			if kind, _ := task["type"].(string); kind == "job" {
				item.Kind = "job"
			}
			item.Label, _ = task["label"].(string)
			item.Source = source[local]
			if metadata, ok := task["x-wtc"].(map[string]any); ok {
				for key := range metadata {
					if key != "url" && key != "url_env" && key != "url_log" && key != "public" && key != "mark" && key != "tunnel" && key != "port_env" {
						return nil, nil, fmt.Errorf("%s: unsupported x-wtc field %s", full, key)
					}
				}
				item.URL, _ = metadata["url"].(string)
				if value, set := metadata["mark"]; set {
					mark, _ := value.(string)
					if !regexp.MustCompile(`^[A-Za-z0-9]$`).MatchString(mark) {
						return nil, nil, fmt.Errorf("%s: x-wtc mark must be one letter or digit", full)
					}
					item.Mark = mark
				}
				if value, set := metadata["public"]; set {
					public, ok := value.(bool)
					if !ok {
						return nil, nil, fmt.Errorf("%s: x-wtc public must be true or false", full)
					}
					item.Exposure, item.ExposureBasis = ExposureProtected, ExposureDeclared
					if public {
						item.Exposure = ExposurePublic
					}
				}
				if value, set := metadata["tunnel"]; set {
					name, _ := value.(string)
					if !runtimeTunnelNames.MatchString(name) {
						return nil, nil, fmt.Errorf("%s: x-wtc tunnel must be one of %s", full, strings.Trim(strings.ReplaceAll(runtimeTunnelNames.String(), "|", ", "), "^()$"))
					}
					item.Provider = name
				}
				if name, _ := metadata["url_env"].(string); name != "" {
					if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(name) {
						return nil, nil, fmt.Errorf("%s: invalid URL variable", full)
					}
					if filepath.IsAbs(binary) {
						value, err := runtimeURLFromEnv(c.Collection, name)
						if err != nil {
							return nil, nil, err
						}
						item.URL = value
					}
				}
				if name, _ := metadata["port_env"].(string); name != "" {
					if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(name) {
						return nil, nil, fmt.Errorf("%s: invalid port variable", full)
					}
					item.PortEnv = name
					if filepath.IsAbs(binary) {
						if port, err := runtimeEnvValue(c.Collection, name); err == nil && regexp.MustCompile(`^[0-9]{1,5}$`).MatchString(port) {
							if item.URL == "" {
								item.URL = "http://127.0.0.1:" + port
							}
							// Ready once the port accepts connections. The port is
							// resolved here: a runner started before the variable
							// existed would not see it.
							if ready, set := task["ready"].(map[string]any); !set {
								task["ready"] = map[string]any{"tcp": "127.0.0.1:" + port, "timeout": "60s"}
							} else if len(ready) == 1 && ready["timeout"] != nil {
								// wtc svc add --ready-timeout without --ready-log.
								ready["tcp"] = "127.0.0.1:" + port
							}
							ports[dir+"/"+strings.Split(local, "/")[0]] = [2]string{name, port}
						}
					}
					if ready, _ := task["ready"].(map[string]any); len(ready) == 1 && ready["timeout"] != nil {
						// No port to probe: a timeout alone is no readiness check.
						delete(task, "ready")
					}
				}
				if item.URLLog, _ = metadata["url_log"].(string); item.URLLog != "" {
					if re, err := regexp.Compile(item.URLLog); err != nil || re.NumSubexp() > 1 {
						return nil, nil, fmt.Errorf("%s: url_log must be a regular expression with at most one group", full)
					}
				}
				if item.URL != "" {
					if err := runtimeURLSafe(item.URL); err != nil {
						return nil, nil, fmt.Errorf("%s: %w", full, err)
					}
				}
				delete(task, "x-wtc")
			} else if _, exists := task["x-wtc"]; exists {
				return nil, nil, fmt.Errorf("%s: x-wtc must be an object", full)
			}
			if tags, ok := task["tags"].([]any); ok {
				for _, tag := range tags {
					if tag == "endpoint" || tag == "accessory" {
						item.Kind = tag.(string)
					}
				}
			}
			if ready, ok := task["ready"].(map[string]any); ok {
				item.Ready = true
				// A probe may carry credentials or a query; only a plain URL is shown.
				if probe, _ := ready["http"].(string); item.URL == "" && runtimeURLSafe(probe) == nil {
					item.URL = probe
				}
			}
			if (item.Exposure != "" || item.Provider != "") && item.Kind != "endpoint" {
				return nil, nil, fmt.Errorf("%s: only an endpoint can be a tunnel or public", full)
			}
			if item.Kind == "endpoint" {
				// A known tunnel client needs no URL pattern and may say how
				// far it reaches.
				var cmd string
				switch value := task["cmd"].(type) {
				case string:
					cmd = value
				case []any:
					for _, part := range value {
						if text, ok := part.(string); ok {
							cmd += text + " "
						}
					}
				}
				provider, known := runtimeTunnelProviderNamed(item.Provider)
				if !known {
					provider, known = runtimeTunnelProviderOfCommand(cmd)
				}
				if known {
					item.Provider = provider.name
					if item.URLLog == "" && item.URL == "" {
						item.URLLog = provider.urlLog
					}
					if item.Exposure == "" && runtimeWord(cmd, provider.program) {
						if exposure := provider.command(cmd); exposure != "" {
							item.Exposure, item.ExposureBasis = exposure, ExposureCommand
						}
					}
				}
			}
			item.Autostart, _ = task["autostart"].(bool)
			if deps, ok := task["deps"].([]any); ok {
				for _, value := range deps {
					dep, ok := value.(string)
					if !ok {
						return nil, nil, fmt.Errorf("%s: dependency must be a task path", full)
					}
					if strings.HasPrefix(dep, "/") {
						dep = strings.TrimPrefix(dep, "/")
					} else {
						dep = dir + "/" + dep
					}
					if !runtimeTaskPath(dep) {
						return nil, nil, fmt.Errorf("%s: invalid dependency %q", full, dep)
					}
					item.Deps = append(item.Deps, dep)
				}
				task["deps"] = item.Deps
			}
			cwd, _ := task["cwd"].(string)
			if cwd == "" {
				cwd = "."
			}
			if !filepath.IsAbs(cwd) {
				cwd = filepath.Join(repo.path, cwd)
			}
			resolved, err := filepath.EvalSymlinks(cwd)
			if err != nil {
				return nil, nil, fmt.Errorf("%s cwd: %w", full, err)
			}
			base, baseErr := filepath.EvalSymlinks(repo.path)
			if baseErr != nil {
				return nil, nil, baseErr
			}
			rel, err := filepath.Rel(base, resolved)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, nil, fmt.Errorf("%s cwd must stay inside its repository", full)
			}
			task["cwd"] = resolved
			if script, ok := task["script"].(string); ok && !filepath.IsAbs(script) {
				task["script"] = filepath.Join(base, script)
			}
			item.Log = filepath.Join(m.Root, "logs", filepath.FromSlash(full)+".log")
			task["log"] = map[string]any{"enabled": true, "file": item.Log, "mode": "append"}
			native[full] = task
			m.Tasks = append(m.Tasks, item)
		}
	}
	if len(m.Tasks) == 0 {
		return nil, nil, fmt.Errorf("no opted-in repositories (.harness/dekit.tasks.yaml)")
	}
	for _, task := range m.Tasks {
		for _, dep := range task.Deps {
			if _, ok := native[dep]; !ok {
				return nil, nil, fmt.Errorf("%s: dependency %s is not present in this collection", task.Path, dep)
			}
		}
	}
	old, err := c.runtimeManifest()
	if err != nil {
		return nil, nil, err
	}
	if old != nil {
		m.TunnelIntents = maps.Clone(old.TunnelIntents)
	}
	for i := range m.Tasks {
		task := native[m.Tasks[i].Path].(map[string]any)
		// A runner keeps the environment it started with; give a service and
		// its tunnels the port it resolves to now.
		if port, ok := ports[m.Tasks[i].Group]; ok {
			env, _ := task["env"].(map[string]any)
			if env == nil {
				env = map[string]any{}
			}
			if _, set := env[port[0]]; !set {
				env[port[0]] = port[1]
			}
			task["env"] = env
		}
		spec, err := yaml.Marshal(task)
		if err != nil {
			return nil, nil, err
		}
		item := &m.Tasks[i]
		definition, err := json.Marshal([]any{string(spec), item.Provider, item.Exposure, item.URL, item.URLLog, item.Source})
		if err != nil {
			return nil, nil, err
		}
		item.Definition = fmt.Sprintf("%x", sha256.Sum256(definition))
		if intent, ok := m.TunnelIntents[item.Path]; ok && item.Kind == "endpoint" {
			item.TunnelIntent = "off"
			if intent.On {
				item.TunnelIntent = "stale"
				if intent.Definition == item.Definition {
					item.TunnelIntent = "on"
				}
			}
			// Explicit choices are started by WTC after provider checks, never
			// implicitly by a runner launch or configuration reload.
			task["autostart"] = false
			spec, err = yaml.Marshal(task)
			if err != nil {
				return nil, nil, err
			}
		}
		item.Spec = fmt.Sprintf("%x", sha256.Sum256(spec))
	}
	sort.Slice(m.Tasks, func(i, j int) bool { return m.Tasks[i].Path < m.Tasks[j].Path })
	sort.Slice(m.Declared, func(i, j int) bool {
		return m.Declared[i].Dir+"/"+m.Declared[i].Name < m.Declared[j].Dir+"/"+m.Declared[j].Name
	})
	data, err := yaml.Marshal(map[string]any{"kernel": map[string]string{"path": binary}, "tasks": native})
	if err != nil {
		return nil, nil, err
	}
	data = append([]byte("# Generated by wtc runtime render. Do not edit.\n"), data...)
	m.Digest = fmt.Sprintf("%x", sha256.Sum256(data))
	return m, data, nil
}

func (c *Context) renderRuntime() (*runtimeManifest, error) {
	if err := c.runtimePathsSafe(); err != nil {
		return nil, err
	}
	binary, err := c.runtimeBinary()
	if err != nil {
		return nil, err
	}
	m, data, err := c.runtimePlan(binary)
	if err != nil {
		return nil, err
	}
	old, err := c.runtimeManifest()
	if err != nil {
		return nil, err
	}
	// Forget approval for a removed declaration, even if that path is reused.
	for path, intent := range m.TunnelIntents {
		if !slices.ContainsFunc(m.Tasks, func(t runtimeTask) bool { return t.Path == path && t.Kind == "endpoint" }) {
			intent.Definition = ""
			m.TunnelIntents[path] = intent
		}
	}
	var reload func() error
	if old != nil {
		m.Resources = old.Resources
		if err := runtimeRegular(filepath.Join(m.Root, "dekit.yaml")); err != nil {
			return nil, err
		}
		if err := runtimeOutputPathsSafe(old); err != nil {
			return nil, err
		}
		existing, err := os.ReadFile(filepath.Join(m.Root, "dekit.yaml"))
		if err != nil {
			return nil, err
		}
		if fmt.Sprintf("%x", sha256.Sum256(existing)) != old.Digest {
			return nil, fmt.Errorf("generated runtime config was edited; preserve changes before rendering")
		}
		if err := runtimeRegular(filepath.Join(m.Root, "dekit.yaml")); err != nil {
			return nil, err
		}
		// Metadata-only policy/provider changes can leave the native digest
		// unchanged. Stop unapproved live endpoints before either save path.
		if slices.ContainsFunc(m.Tasks, func(t runtimeTask) bool { return t.TunnelIntent == "off" || t.TunnelIntent == "stale" }) {
			stopped := map[string]bool{}
			for _, item := range c.runtimeStatus(old) {
				if item.State == "unknown" && slices.ContainsFunc(m.Tasks, func(t runtimeTask) bool {
					return t.Path == item.Path && (t.TunnelIntent == "off" || t.TunnelIntent == "stale")
				}) {
					return nil, fmt.Errorf("%s: shutdown not confirmed: unknown", item.Path)
				}
				if item.State != "ready" && item.State != "running" && item.State != "starting" {
					continue
				}
				for _, task := range m.Tasks {
					if task.Path == item.Path && (task.TunnelIntent == "off" || task.TunnelIntent == "stale") {
						if _, err := runtimeCommand(old, false, "veto", task.Path); err != nil {
							return nil, err
						}
						stopped[task.Path] = true
					}
				}
			}
			if len(stopped) > 0 {
				if err := c.runtimeWaitStopped(old, stopped); err != nil {
					return nil, err
				}
			}
		}
		if old.Digest == m.Digest {
			// Endpoint metadata and declared resources are outside the
			// generated config and its digest.
			if reflect.DeepEqual(old.Tasks, m.Tasks) && reflect.DeepEqual(old.Declared, m.Declared) && reflect.DeepEqual(old.TunnelIntents, m.TunnelIntents) {
				return old, nil
			}
			if err := runtimeSaveManifest(m); err != nil {
				return nil, err
			}
			return m, nil
		}
		state, err := runtimeRunnerState(old)
		if err != nil {
			return nil, err
		}
		if state == "running" {
			// Apply the change live: a reload adds and redefines tasks but
			// keeps removed ones, so remove those first, and restart a
			// running task whose definition changed.
			kept := map[string]string{}
			for _, task := range m.Tasks {
				kept[task.Path] = task.Spec
			}
			live := map[string]bool{}
			for _, item := range c.runtimeStatus(old) {
				switch item.State {
				case "ready", "running", "starting":
					live[item.Path] = true
				}
			}
			for _, task := range old.Tasks {
				if _, ok := kept[task.Path]; !ok {
					if _, err := runtimeCommand(old, false, "rm", task.Path); err != nil {
						return nil, err
					}
				}
			}
			reload = func() error {
				if err := runtimeReloadConfig(m); err != nil {
					return err
				}
				for _, task := range old.Tasks {
					if spec, ok := kept[task.Path]; ok && spec != task.Spec && live[task.Path] {
						if _, err := runtimeCommand(m, false, "restart", task.Path); err != nil {
							return err
						}
					}
				}
				return nil
			}
		} else if state != "absent" {
			return nil, fmt.Errorf(branding.Text("runtime config changed while the runner is %s; run {{command}} down before rendering"), state)
		} else if _, err := runtimeCommand(old, false, "runner", "stop"); err != nil && !strings.Contains(err.Error(), "Runner is not running.") {
			// Clear down's saved task definitions before applying changed config.
			return nil, err
		}
	} else if _, err := os.Lstat(m.Root); err == nil {
		return nil, fmt.Errorf("unmanaged runtime directory already exists")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	complete := false
	defer func() {
		if old == nil && !complete {
			_ = os.RemoveAll(m.Root)
		}
	}()
	if err := os.MkdirAll(m.Root, 0700); err != nil {
		return nil, err
	}
	for _, task := range m.Tasks {
		if err := runtimeSafeParents(m.Root, task.Log); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(task.Log), 0700); err != nil {
			return nil, err
		}
	}
	if err := runtimeWrite(filepath.Join(m.Root, "dekit.yaml"), data); err != nil {
		return nil, err
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := runtimeWrite(filepath.Join(m.Root, "manifest.json"), manifest); err != nil {
		return nil, err
	}
	complete = true
	if reload != nil {
		if err := reload(); err != nil {
			return nil, fmt.Errorf("apply changed runtime config: %w", err)
		}
	}
	return m, nil
}

func runtimeReloadConfig(m *runtimeManifest) error {
	_, err := runtimeCommand(m, false, "reload", "--yes")
	if err != nil && strings.Contains(err.Error(), "unknown command or unsupported file: `reload`") {
		// Dekit 0.10.0 imports configuration by restarting its runner.
		_, err = runtimeCommand(m, false, "runner", "restart")
	}
	return err
}

func runtimeSaveManifest(m *runtimeManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return runtimeWrite(filepath.Join(m.Root, "manifest.json"), data)
}

func runtimeWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".runtime-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (c *Context) RenderRuntime(dryRun bool) (RuntimeResult, error) {
	if !dryRun {
		defer c.signalServicesChanged()
	}
	result := RuntimeResult{Root: c.runtimeRoot(), Action: "render", Items: []RuntimeItem{}}
	var m *runtimeManifest
	var err error
	if dryRun {
		m, _, err = c.runtimePlan("dekit")
	} else {
		err = c.withRuntimeLock(func() error { m, err = c.renderRuntime(); return err })
	}
	if err == nil {
		for _, task := range m.Tasks {
			result.Items = append(result.Items, task.RuntimeItem)
		}
	}
	return result, err
}

type runtimeFragment struct {
	Tasks     map[string]map[string]any `yaml:"tasks"`
	Resources map[string]struct {
		Type  string `yaml:"type"`
		Mark  string `yaml:"mark"`
		Label string `yaml:"label"`
	} `yaml:"resources"`
}

var runtimeResourceName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// runtimeSharedFragment holds a repo's committed services.
func runtimeSharedFragment(repo string) string {
	return filepath.Join(repo, ".harness", "dekit.tasks.yaml")
}

// runtimeLocalFragment holds services this collection added for a repo; it is
// never committed and goes with the collection.
func (c *Context) runtimeLocalFragment(dir string) string {
	return filepath.Join(filepath.Dir(c.runtimeRoot()), "svc", dir+".yaml")
}

func runtimeReadFragment(path string) (runtimeFragment, error) {
	var fragment runtimeFragment
	data, err := os.ReadFile(path)
	if err != nil {
		return fragment, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&fragment); err != nil && err != io.EOF {
		return fragment, fmt.Errorf("%s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fragment, fmt.Errorf("%s: expected one YAML document", path)
	}
	return fragment, nil
}
