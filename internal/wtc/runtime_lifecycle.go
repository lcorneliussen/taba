package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"taba.sh/taba/internal/branding"
	"time"
)

func runtimeDependencies(m *runtimeManifest, selected []runtimeTask) []runtimeTask {
	byPath := map[string]runtimeTask{}
	seen := map[string]bool{}
	result := []runtimeTask{}
	for _, task := range m.Tasks {
		byPath[task.Path] = task
	}
	var visit func(runtimeTask)
	visit = func(task runtimeTask) {
		if seen[task.Path] {
			return
		}
		seen[task.Path] = true
		for _, dep := range task.Deps {
			visit(byPath[dep])
		}
		result = append(result, task)
	}
	for _, task := range selected {
		visit(task)
	}
	return result
}

// Every client takes the legacy lock, whichever namespace holds the runtime.
func (c *Context) withRuntimeLock(fn func() error) error {
	path := filepath.Join(c.Collection, ".wtc-runtime.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("runtime lock must be a regular file")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK || time.Now().After(deadline) {
			return fmt.Errorf("runtime is busy: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return fn()
}

// runtimeDiagnostics keeps the start of stderr and discards the rest, so a
// noisy but successful call is never reported as failed.
type runtimeDiagnostics struct {
	data  []byte
	limit int
}

func (d *runtimeDiagnostics) Write(p []byte) (int, error) {
	if room := d.limit - len(d.data); room > 0 {
		d.data = append(d.data, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (d *runtimeDiagnostics) String() string { return string(d.data) }

func runtimeCommand(m *runtimeManifest, sourceEnv bool, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	argv := append([]string{"-C", m.Root, "--json"}, args...)
	cmd := exec.CommandContext(ctx, m.Binary, argv...)
	if sourceEnv {
		collection := filepath.Dir(filepath.Dir(m.Root))
		args := append([]string{"-c", `set -ea; [ ! -f "$1" ] || . "$1"; [ ! -f "$2" ] || . "$2"; shift 2; exec "$@"`, "wtc-runtime", filepath.Join(collection, ".env.collection"), filepath.Join(collection, ".env.collection.local"), m.Binary}, argv...)
		cmd = exec.CommandContext(ctx, "/bin/sh", args...)
	}
	cmd.Dir = m.Root
	cmd.WaitDelay = 200 * time.Millisecond
	var stdout statusLimitedBuffer
	stdout.limit = 1 << 20
	stderr := runtimeDiagnostics{limit: 4096}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dekit %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func runtimeRunnerState(m *runtimeManifest) (string, error) {
	data, err := runtimeCommand(m, false, "runner", "status")
	if err != nil {
		return "", err
	}
	var state struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return "", err
	}
	if state.Status == "" {
		return "", fmt.Errorf("dekit returned no runner state")
	}
	return state.Status, nil
}

func (c *Context) runtimeHooks(m *runtimeManifest, tasks []runtimeTask, hook string) error {
	seen := map[string]bool{}
	for _, task := range tasks {
		if task.Dir == "" {
			return fmt.Errorf("invalid runtime hook owner")
		}
		if seen[task.Dir] {
			continue
		}
		seen[task.Dir] = true
		worktree := filepath.Join(c.Collection, task.Dir)
		path := filepath.Join(worktree, ".harness", "runtime-"+hook+".sh")
		if err := runtimeRegular(path); os.IsNotExist(err) {
			if hook == "teardown" {
				for _, resource := range m.Resources {
					if resource.Dir == task.Dir {
						return fmt.Errorf("%s: resource teardown hook is missing", task.Dir)
					}
				}
			}
			continue
		} else if err != nil {
			return err
		}
		// Record ownership before a provision attempt, including partial failure.
		resourceIndex := -1
		for i := range m.Resources {
			if m.Resources[i].Dir == task.Dir {
				resourceIndex = i
				break
			}
		}
		if hook == "teardown" && resourceIndex < 0 {
			// Nothing provisioned here, so nothing of this collection's to destroy.
			continue
		}
		if hook == "provision" {
			teardown := filepath.Join(worktree, ".harness", "runtime-teardown.sh")
			if err := runtimeRegular(teardown); err != nil {
				return fmt.Errorf("%s: provision requires a teardown hook: %w", task.Dir, err)
			}
			info, err := os.Stat(teardown)
			if err != nil {
				return err
			}
			if info.Mode()&0111 == 0 {
				return fmt.Errorf("runtime teardown hook must be executable: %s", teardown)
			}
			if resourceIndex < 0 {
				m.Resources = append(m.Resources, runtimeResource{Dir: task.Dir, Repo: task.Repo})
				resourceIndex = len(m.Resources) - 1
			}
			m.Resources[resourceIndex].State = "provision-attempted"
			if err := runtimeSaveManifest(m); err != nil {
				return err
			}
		}
		info, _ := os.Stat(path)
		if info.Mode()&0111 == 0 {
			return fmt.Errorf("runtime hook must be executable: %s", path)
		}
		if hook == "teardown" {
			// Say what is about to be destroyed, when the repo declares it.
			var names []string
			for _, d := range m.Declared {
				if d.Dir == task.Dir {
					name := d.Name
					if d.Type != "" {
						name += " (" + d.Type + ")"
					}
					names = append(names, name)
				}
			}
			if len(names) > 0 {
				fmt.Fprintf(os.Stderr, "%s: tearing down %s\n", task.Dir, strings.Join(names, ", "))
			}
		}
		cmd := exec.Command("/bin/sh", "-c", `set -ea; [ ! -f "$1" ] || . "$1"; [ ! -f "$2" ] || . "$2"; shift 2; exec "$@"`, "wtc-runtime-hook", filepath.Join(c.Collection, ".env.collection"), filepath.Join(c.Collection, ".env.collection.local"), path)
		cmd.Dir = worktree
		logPath := filepath.Join(m.Root, "hook-logs", task.Dir, hook+".log")
		if err := runtimeSafeParents(m.Root, logPath); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
			return err
		}
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		cmd.Stdout, cmd.Stderr = io.MultiWriter(os.Stderr, log), io.MultiWriter(os.Stderr, log)
		runErr := cmd.Run()
		log.Close()
		if resourceIndex >= 0 {
			if hook == "provision" {
				if runErr == nil {
					m.Resources[resourceIndex].State = "provision-hook-completed"
				}
			} else if runErr == nil {
				m.Resources = append(m.Resources[:resourceIndex], m.Resources[resourceIndex+1:]...)
			} else {
				m.Resources[resourceIndex].State = "teardown-failed"
			}
			if err := runtimeSaveManifest(m); err != nil {
				return err
			}
		}
		if runErr != nil {
			err := runErr
			return fmt.Errorf("%s runtime %s failed: %w", task.Dir, hook, err)
		}
	}
	return nil
}

// runtimeGroupTasks picks a group's processes and private autostart defaults.
// Explicit choices are restored separately after provider checks.
func runtimeGroupTasks(selector string, tasks []runtimeTask, starting bool) ([]runtimeTask, error) {
	var picked []runtimeTask
	for _, task := range tasks {
		if task.Kind != "endpoint" {
			picked = append(picked, task)
			continue
		}
		if task.TunnelIntent != "" {
			continue
		}
		switch task.Exposure {
		case ExposureTailnet, ExposureAccount, ExposureProtected:
			if starting && task.Autostart {
				picked = append(picked, task)
			}
		}
	}
	if len(picked) == 0 {
		group := strings.TrimSuffix(selector, "/**")
		return nil, fmt.Errorf(branding.Text("%s has only tunnels; open one with {{command}} svc tunnel"), group)
	}
	return picked, nil
}

func (c *Context) RuntimeAction(action, target string, wait time.Duration) (RuntimeResult, error) {
	return c.runtimeAction(action, target, wait, nil)
}

func (c *Context) runtimeAction(action, target string, wait time.Duration, choice *runtimeTunnelIntent) (RuntimeResult, error) {
	defer c.signalServicesChanged()
	result := RuntimeResult{Root: c.runtimeRoot(), Action: action, Items: []RuntimeItem{}}
	err := c.withRuntimeLock(func() error {
		m, err := c.runtimeManifest()
		if err != nil {
			return err
		}
		if action == "up" || action == "provision" || action == "restart" || m == nil && choice != nil {
			m, err = c.renderRuntime()
			if err != nil {
				return err
			}
		}
		if m == nil {
			return errors.New(branding.Text("runtime is not configured; run {{command}} up"))
		}
		if action != "down" {
			if err := runtimeOutputPathsSafe(m); err != nil {
				return err
			}
		}
		if choice != nil && !choice.On && !slices.ContainsFunc(m.Tasks, func(t runtimeTask) bool { return t.Path == target }) {
			// A declaration added since the last render has no running task
			// to stop. Persist off before rendering so it cannot autostart.
			if m.TunnelIntents == nil {
				m.TunnelIntents = map[string]runtimeTunnelIntent{}
			}
			m.TunnelIntents[target] = *choice
			if err := runtimeSaveManifest(m); err != nil {
				return err
			}
			m, err = c.renderRuntime()
			if err != nil {
				return err
			}
		}
		selector, tasks, err := runtimeSelect(m, target)
		if err != nil {
			return err
		}
		if choice != nil {
			if len(tasks) != 1 || tasks[0].Kind != "endpoint" || choice.On && tasks[0].Definition != choice.Definition {
				return fmt.Errorf("tunnel declaration changed; choose the tunnel again")
			}
			if m.TunnelIntents == nil {
				m.TunnelIntents = map[string]runtimeTunnelIntent{}
			}
			m.TunnelIntents[tasks[0].Path] = *choice
			for i := range m.Tasks {
				if m.Tasks[i].Path == tasks[0].Path {
					m.Tasks[i].TunnelIntent = "off"
					if choice.On {
						m.Tasks[i].TunnelIntent = "on"
					}
				}
			}
			if err := runtimeSaveManifest(m); err != nil {
				return err
			}
			if choice.On {
				m, err = c.renderRuntime()
				if err != nil {
					return err
				}
				selector, tasks, err = runtimeSelect(m, target)
				if err != nil {
					return err
				}
			}
		}
		if choice == nil && (action == "up" || action == "restart") && len(tasks) == 1 && (tasks[0].TunnelIntent == "off" || tasks[0].TunnelIntent == "stale") {
			return fmt.Errorf(branding.Text("%s: choose the tunnel again with {{command}} svc tunnel"), tasks[0].Path)
		}
		if action == "up" || action == "restart" {
			for _, task := range tasks {
				if selector != "" && task.TunnelIntent == "on" {
					if missing := runtimeLoginCheck(task.Provider); missing != "" {
						return fmt.Errorf("%s: %s", task.Path, missing)
					}
				}
			}
		}
		grouped := strings.HasSuffix(selector, "/**")
		// Restore choices for the services selected by this action. Untargeted
		// restart retains its existing rule of restarting only live services.
		live := map[string]bool{}
		if action == "up" && target == "" {
			for _, item := range c.runtimeStatus(m) {
				if item.State == "ready" || item.State == "running" || item.State == "starting" {
					live[item.Path] = true
				}
			}
		}
		selectedGroups := map[string]bool{}
		for _, task := range tasks {
			if task.Kind != "endpoint" && (target != "" || action != "up" || task.Autostart || live[task.Path]) {
				selectedGroups[task.Group] = true
			}
		}
		var restored []runtimeTask
		restore := func() error {
			for _, task := range m.Tasks {
				if task.Kind != "endpoint" || task.TunnelIntent != "on" || !selectedGroups[task.Group] {
					continue
				}
				if action == "restart" && target == "" {
					active := false
					for _, service := range m.Tasks {
						if service.Group == task.Group && service.Kind != "endpoint" && live[service.Path] {
							active = true
						}
					}
					if !active {
						continue
					}
				}
				if missing := runtimeLoginCheck(task.Provider); missing != "" {
					return fmt.Errorf("%s: %s", task.Path, missing)
				}
				if _, err := runtimeCommand(m, true, "start", task.Path); err != nil {
					return err
				}
				restored = append(restored, task)
			}
			return nil
		}
		switch action {
		case "up", "provision":
			if err := c.runtimeHooks(m, runtimeDependencies(m, tasks), "provision"); err != nil {
				return err
			}
			if action == "provision" {
				return nil
			}
			if grouped {
				if tasks, err = runtimeGroupTasks(selector, tasks, true); err != nil {
					return err
				}
				for _, task := range tasks {
					if _, err := runtimeCommand(m, true, "start", task.Path); err != nil {
						return err
					}
				}
				break
			}
			args := []string{"up"}
			if selector != "" {
				args = []string{"start", selector}
			}
			if _, err := runtimeCommand(m, true, args...); err != nil {
				return err
			}
		case "down":
			state, err := runtimeRunnerState(m)
			if err != nil {
				return err
			}
			if state == "absent" {
				if choice != nil {
					_, err := c.renderRuntime()
					return err
				}
				return nil
			}
			args := []string{"down"}
			if selector != "" {
				args = []string{"veto", selector}
			}
			if _, err := runtimeCommand(m, false, args...); err != nil {
				return err
			}
		case "restart":
			state, err := runtimeRunnerState(m)
			if err != nil {
				return err
			}
			if state != "running" {
				return fmt.Errorf(branding.Text("runner is %s; run {{command}} up first"), state)
			}
			for _, item := range c.runtimeStatus(m) {
				if item.State == "ready" || item.State == "running" || item.State == "starting" {
					live[item.Path] = true
				}
			}
			if selector == "" {
				// Restart what runs, never a stopped tunnel or a service left
				// off. A running dependent restarts with its dependency.
				var running []runtimeTask
				for _, task := range tasks {
					if !live[task.Path] || slices.ContainsFunc(task.Deps, func(dep string) bool { return live[dep] }) {
						continue
					}
					running = append(running, task)
				}
				if len(running) == 0 {
					return errors.New(branding.Text("nothing is running; start services with {{command}} up"))
				}
				tasks = running
				for _, task := range tasks {
					if _, err := runtimeCommand(m, false, "restart", task.Path); err != nil {
						return err
					}
				}
				break
			}
			if grouped {
				if tasks, err = runtimeGroupTasks(selector, tasks, false); err != nil {
					return err
				}
				for _, task := range tasks {
					if _, err := runtimeCommand(m, false, "restart", task.Path); err != nil {
						return err
					}
				}
				break
			}
			if _, err := runtimeCommand(m, false, "restart", selector); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown runtime action %q", action)
		}
		if action == "up" || action == "restart" {
			if err := restore(); err != nil {
				return err
			}
			tasks = append(tasks, restored...)
		}
		if action == "down" && target == "" {
			deadline := time.Now().Add(30 * time.Second)
			for {
				state, err := runtimeRunnerState(m)
				if err != nil {
					return err
				}
				if state == "absent" {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("runtime shutdown not confirmed: %s", state)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		if action == "down" && target != "" {
			stopped := map[string]bool{}
			for _, task := range tasks {
				stopped[task.Path] = true
			}
			for changed := true; changed; {
				changed = false
				for _, task := range m.Tasks {
					for _, dep := range task.Deps {
						if stopped[dep] && !stopped[task.Path] {
							stopped[task.Path] = true
							changed = true
						}
					}
				}
			}
			if err := c.runtimeWaitStopped(m, stopped); err != nil {
				return err
			}
		}
		if choice != nil && !choice.On {
			// Confirm this endpoint stopped before applying configuration. A
			// different provider's failed stop must not prevent this one.
			if _, err := c.renderRuntime(); err != nil {
				return err
			}
		}
		if wait > 0 && (action == "up" || action == "restart") {
			wanted := map[string]bool{}
			for _, task := range tasks {
				if target == "" && task.TunnelIntent != "" && !slices.ContainsFunc(restored, func(t runtimeTask) bool { return t.Path == task.Path }) {
					continue
				}
				if target != "" || task.Autostart || task.TunnelIntent == "on" || action == "restart" {
					wanted[task.Path] = true
				}
			}
			// dekit restarts running dependents with their dependency, such as a
			// tunnel with its service; wait for those too.
			for changed := action == "restart"; changed; {
				changed = false
				for _, task := range m.Tasks {
					for _, dep := range task.Deps {
						if wanted[dep] && live[task.Path] && !wanted[task.Path] {
							wanted[task.Path], changed = true, true
						}
					}
				}
			}
			deadline := time.Now().Add(wait)
			reachable := map[string]bool{}
			for {
				items := c.runtimeStatusOnly()
				ready, pending := true, ""
				observed := map[string]bool{}
				for _, item := range items {
					if !wanted[item.Path] {
						continue
					}
					observed[item.Path] = true
					switch item.State {
					case "ready", "done":
						// A tunnel can announce its URL before it serves through it.
						for _, task := range m.Tasks {
							if task.Path == item.Path && task.URLLog != "" && !reachable[item.Path] {
								if reachable[item.Path] = item.URL != "" && runtimeURLReachable(item.URL, time.Until(deadline)); !reachable[item.Path] {
									ready, pending = false, item.Path+": endpoint URL not reachable yet"
								}
							}
						}
					case "running":
						for _, task := range m.Tasks {
							if task.Path == item.Path && (task.Ready || task.Kind == "job") {
								ready = false
							}
						}
					case "exited", "unknown", "missing", "failed", "absent":
						return fmt.Errorf("%s: %s %s %s", item.Path, item.State, item.Reason, item.Error)
					default:
						ready = false
					}
				}
				for path := range wanted {
					if !observed[path] {
						return fmt.Errorf("%s: runtime state unavailable", path)
					}
				}
				if ready {
					break
				}
				if time.Now().After(deadline) {
					if pending != "" {
						return fmt.Errorf(branding.Text("%s after %s; inspect {{command}} status and {{command}} logs"), pending, wait)
					}
					return fmt.Errorf(branding.Text("runtime readiness timed out after %s; inspect {{command}} status and {{command}} logs"), wait)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		return nil
	})
	result.Items = c.RuntimeStatus()
	if err != nil && (action == "up" || action == "restart") {
		if m, mErr := c.runtimeManifest(); mErr == nil && m != nil {
			for _, item := range result.Items {
				named := strings.Contains(err.Error(), item.Path+":")
				named = named || item.runnerStarting && strings.Contains(err.Error(), "timed out")
				switch item.State {
				case "exited", "failed":
					named = true
				case "starting", "running":
					// Still compiling or waiting for its probe when time ran out.
					named = named || strings.Contains(err.Error(), "timed out")
				}
				if !named {
					continue
				}
				for _, task := range m.Tasks {
					if task.Path == item.Path && runtimeSafeParents(m.Root, task.Log) == nil {
						if tail := runtimeLogTail(task.Log, 40); tail != "" {
							result.Logs = append(result.Logs, RuntimeLogTail{Path: task.Path, File: task.Log, Tail: tail})
						}
					}
				}
			}
		}
	}
	return result, err
}

// runtimeURLReachable reports whether an endpoint answers through its URL. Any
// response short of a gateway error counts; the page itself is the project's.
// One attempt never outlasts the caller's remaining wait by much.
func runtimeURLReachable(url string, budget time.Duration) bool {
	client := http.Client{Timeout: max(100*time.Millisecond, min(2*time.Second, budget)), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(url)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode < 500
}

// RetireRuntime uses the target's manifest even if runtime opt-in was removed.
// An unconfirmed shutdown or failed teardown keeps the worktrees and record.
func (c *Context) RetireRuntime() error {
	defer c.signalServicesChanged()
	if err := c.runtimeConflict(); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(c.runtimeRoot(), "manifest.json")); os.IsNotExist(err) {
		return nil
	}
	return c.withRuntimeLock(c.retireRuntime)
}

func (c *Context) retireRuntime() error {
	m, err := c.runtimeManifest()
	if err != nil {
		return err
	}
	if m == nil {
		return nil
	}
	state, err := runtimeRunnerState(m)
	if err != nil {
		return err
	}
	_, stopErr := runtimeCommand(m, false, "runner", "stop")
	if stopErr != nil && !(state == "absent" && strings.Contains(stopErr.Error(), "Runner is not running.")) {
		return stopErr
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		state, err = runtimeRunnerState(m)
		if err != nil {
			return err
		}
		if state == "absent" {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("runtime shutdown not confirmed: %s", state)
		}
		time.Sleep(100 * time.Millisecond)
	}
	tasks := append([]runtimeTask(nil), m.Tasks...)
	for _, resource := range m.Resources {
		tasks = append(tasks, runtimeTask{RuntimeItem: RuntimeItem{Dir: resource.Dir, Repo: resource.Repo}})
	}
	return c.runtimeHooks(m, tasks, "teardown")
}

// runtimeWaitStopped confirms veto completion before config changes can
// replace a tunnel command or restart one of its dependencies.
func (c *Context) runtimeWaitStopped(m *runtimeManifest, stopped map[string]bool) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		complete := true
		for _, item := range c.runtimeStatus(m) {
			if !stopped[item.Path] {
				continue
			}
			switch item.State {
			case "idle", "exited", "done", "absent":
			case "unknown", "missing":
				return fmt.Errorf("%s: shutdown not confirmed: %s", item.Path, item.State)
			default:
				complete = false
			}
		}
		if complete {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("target runtime shutdown not confirmed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}
