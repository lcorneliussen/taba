package wtc

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// A repository declares its tasks in the files [runtime] declarations names
// (runtime_paths.go): by default natively in a root dekit.yaml, which a bare
// `dekit up` runs, and for taba in .harness/dekit.tasks.yaml. A collection adds
// its own in svc/<repo>.yaml. One loader reads them all for discovery, the
// declaration cache, named tunnels and rendering. It parses files only: no hook, kernel, !js
// value or task command runs, and no environment file is sourced.

// Bounds for the native tree a repository's dekit.yaml loads.
const (
	runtimeDekitMaxDepth   = 16
	runtimeDekitMaxFiles   = 256
	runtimeDekitMaxBytes   = 1 << 20
	runtimeDekitMaxEntries = 10000
)

// RuntimeDeclaration is the file that declares a task, relative to its scope:
// the repository (dekit.yaml, a fragment it loads, .harness/dekit.tasks.yaml)
// or the collection (its local svc/<repo>.yaml). Mount is the `at` path a
// native load puts the file's tasks under.
type RuntimeDeclaration struct {
	Scope string `json:"scope"`
	File  string `json:"file"`
	Mount string `json:"mount,omitempty"`
}

type runtimeDeclaredTask struct {
	spec map[string]any
	// source keeps RuntimeItem.Source semantics: the absolute task file.
	source string
	decl   RuntimeDeclaration
	// native marks the dekit.yaml tree, even a harness file it loads.
	native bool
}

type runtimeResourceFile struct {
	path     string
	fragment runtimeFragment
}

type runtimeRepoSources struct {
	tasks     map[string]runtimeDeclaredTask
	resources []runtimeResourceFile
	// inputs are the files and directories read, for cache invalidation.
	inputs []string
}

// runtimeNativeDefaults are the task settings dekit.yaml's defaults may set.
var runtimeNativeDefaults = map[string]bool{"cwd": true, "env": true, "add_path": true, "autostart": true, "autorestart": true, "stop": true, "log": true, "scrollback_len": true, "mouse_scroll_speed": true}

type runtimeDekitProject struct {
	Tasks    map[string]any `yaml:"tasks"`
	Defaults map[string]any `yaml:"defaults"`
	Vars     any            `yaml:"vars"`
	Load     []any          `yaml:"load"`
	// The runner's own settings belong to the collection runner: parsed so the
	// file stays strict, never applied or run.
	Kernel any `yaml:"kernel"`
	Log    any `yaml:"log"`
	OnInit any `yaml:"on_init"`
	OnIdle any `yaml:"on_idle"`
}

type runtimeDekitFragment struct {
	Tasks map[string]any `yaml:"tasks"`
	Load  []any          `yaml:"load"`
}

// runtimeRepoSources reads a repository's declarations: the configured
// declaration files (runtime_paths.go) in order, then this collection's own
// svc/<repo>.yaml. checkParents refuses symlinked directories above the default
// harness file and the local file, as discovery always has; native trees and
// any other configured file refuse symlinks in every caller.
func (c *Context) runtimeRepoSources(repoPath, dir string, checkParents bool) (runtimeRepoSources, error) {
	out := runtimeRepoSources{tasks: map[string]runtimeDeclaredTask{}}
	files, err := c.runtimeDeclarationFiles()
	if err != nil {
		return out, err
	}
	repoRoot, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		return out, err
	}
	sharedRel := filepath.ToSlash(filepath.Join(".harness", "dekit.tasks.yaml"))
	// Native projects first: a taba task file one of them loads is read there.
	sets := make([]map[string]runtimeDeclaredTask, len(files))
	mounts := map[string][]string{}
	for i, file := range files {
		abs := filepath.Join(repoRoot, filepath.FromSlash(file.rel))
		out.inputs = append(out.inputs, abs)
		if !file.native {
			continue
		}
		if _, err := os.Lstat(abs); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return out, fmt.Errorf("%s/%s: %w", dir, file.rel, err)
		}
		native := &runtimeDekitLoader{root: repoRoot, project: filepath.Dir(abs), display: repoPath, label: dir, tasks: map[string]runtimeDeclaredTask{}, folders: map[string]string{}, mounts: mounts}
		err := native.load(abs, "", true, 0)
		out.inputs = append(out.inputs, native.inputs...)
		if err != nil {
			return out, err
		}
		if err := native.checkTree(); err != nil {
			return out, err
		}
		sets[i] = native.tasks
	}
	collectionRoot := c.Collection
	if resolved, err := filepath.EvalSymlinks(c.Collection); err == nil {
		collectionRoot = resolved
	}
	// strict holds a file to the native tree's checks; fenced also reads it
	// the native way, refusing a swapped link or FIFO.
	read := func(path, root, safe string, strict, fenced bool, decl RuntimeDeclaration) (map[string]runtimeDeclaredTask, error) {
		if strict {
			if err := runtimeSafeParents(root, safe); err != nil {
				return nil, err
			}
		}
		if err := runtimeRegular(safe); os.IsNotExist(err) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		var fragment runtimeFragment
		if fenced {
			data, err := runtimeDekitRead(safe)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if fragment, err = runtimeDecodeFragment(path, data); err != nil {
				return nil, err
			}
		} else if fragment, err = runtimeReadFragment(safe); err != nil {
			return nil, err
		}
		out.resources = append(out.resources, runtimeResourceFile{path: path, fragment: fragment})
		tasks := map[string]runtimeDeclaredTask{}
		for name, task := range fragment.Tasks {
			tasks[name] = runtimeDeclaredTask{spec: task, source: path, decl: decl}
		}
		return tasks, nil
	}
	for i, file := range files {
		if file.native {
			continue
		}
		if loaded := mounts[file.rel]; len(loaded) > 0 {
			// A native dekit.yaml loads this file without `at`, so its tasks
			// were read through that tree at the repository root; reading it
			// again would declare each of them twice.
			if len(loaded) == 1 && loaded[0] == "" {
				continue
			}
			return out, fmt.Errorf("%s: dekit.yaml loads %s at %q; taba also reads it at the repository root, so load it without `at` or not at all", dir, file.rel, strings.Join(loaded, `", "`))
		}
		path := filepath.Join(repoPath, filepath.FromSlash(file.rel))
		safe := filepath.Join(repoRoot, filepath.FromSlash(file.rel))
		// The default harness file keeps the checks it always had; any other
		// configured file is held to the native tree's containment.
		configured := file.rel != sharedRel
		tasks, err := read(path, repoRoot, safe, checkParents || configured, configured, RuntimeDeclaration{Scope: "repository", File: file.rel})
		if err != nil {
			return out, err
		}
		sets[i] = tasks
	}
	local := c.runtimeLocalFragment(dir)
	out.inputs = append(out.inputs, local)
	decl := RuntimeDeclaration{Scope: "collection", File: local}
	if rel, err := filepath.Rel(collectionRoot, local); err == nil {
		decl.File = filepath.ToSlash(rel)
	}
	tasks, err := read(local, collectionRoot, local, checkParents, false, decl)
	if err != nil {
		return out, err
	}
	sets = append(sets, tasks)
	for _, tasks := range sets {
		names := make([]string, 0, len(tasks))
		for name := range tasks {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			task := tasks[name]
			if old, dup := out.tasks[name]; dup {
				return out, fmt.Errorf("%s/%s is defined in both %s and %s", dir, name, runtimeDeclarationName(old.decl), runtimeDeclarationName(task.decl))
			}
			out.tasks[name] = task
		}
	}
	return out, nil
}

func runtimeDeclarationName(d RuntimeDeclaration) string {
	if d.Scope == "collection" {
		return "the collection's " + d.File
	}
	return d.File
}

type runtimeDekitLoader struct {
	root    string // resolved repository root
	project string // directory of the dekit.yaml: loads stay inside it
	display string // repository path as the caller names it
	label   string // repository directory name, for messages
	tasks   map[string]runtimeDeclaredTask
	folders map[string]string
	mounts  map[string][]string // file -> mounts it was loaded at
	stack   []string
	files   int
	entries int
	inputs  []string
	// defaults from dekit.yaml, with relative paths already resolved.
	defaults map[string]any
}

func (l *runtimeDekitLoader) name(abs string) string {
	rel, err := filepath.Rel(l.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

// projectFile is the repository-relative dekit.yaml of this tree.
func (l *runtimeDekitLoader) projectFile() string {
	return l.name(filepath.Join(l.project, "dekit.yaml"))
}

func (l *runtimeDekitLoader) errorf(abs, format string, args ...any) error {
	return fmt.Errorf("%s/%s: %s", l.label, l.name(abs), fmt.Sprintf(format, args...))
}

// contained refuses a path outside the repository, inside its .git, or
// reached through a symlink. dekit itself follows links that stay inside the
// project; taba does not read symlinked declaration files or directories.
func (l *runtimeDekitLoader) contained(abs string) error {
	rel, err := filepath.Rel(l.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s: a dekit.yaml loads %s, outside the repository", l.label, l.name(abs))
	}
	if inProject, err := filepath.Rel(l.project, abs); err != nil || inProject == ".." || strings.HasPrefix(inProject, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s/%s loads %s, outside its project directory", l.label, l.projectFile(), l.name(abs))
	}
	current := l.root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if runtimeGitDir(part) {
			return fmt.Errorf("%s/%s: declarations are not read from .git", l.label, filepath.ToSlash(rel))
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("%s/%s: %w", l.label, l.name(current), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s/%s is a symlink; taba does not read symlinked declaration files or directories, so replace it with the file or directory itself", l.label, l.name(current))
		}
	}
	return nil
}

func (l *runtimeDekitLoader) load(abs, mount string, root bool, depth int) error {
	abs = filepath.Clean(abs)
	if err := l.contained(abs); err != nil {
		return err
	}
	if !root && filepath.Base(abs) == "dekit.yaml" {
		return l.errorf(abs, "is a project declaration, not a fragment; dekit refuses to load it too")
	}
	for i, open := range l.stack {
		if open == abs {
			chain := make([]string, 0, len(l.stack)-i+1)
			for _, file := range append(l.stack[i:], abs) {
				chain = append(chain, l.name(file))
			}
			return fmt.Errorf("%s/%s: load cycle: %s", l.label, l.projectFile(), strings.Join(chain, " -> "))
		}
	}
	if depth > runtimeDekitMaxDepth {
		return l.errorf(abs, "loads are nested deeper than %d files", runtimeDekitMaxDepth)
	}
	if l.files++; l.files > runtimeDekitMaxFiles {
		return fmt.Errorf("%s/%s loads more than %d files", l.label, l.projectFile(), runtimeDekitMaxFiles)
	}
	l.inputs = append(l.inputs, abs)
	l.mounts[l.name(abs)] = append(l.mounts[l.name(abs)], mount)
	data, err := runtimeDekitRead(abs)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return l.errorf(abs, "%v", err)
	}
	if err := runtimeDekitTags(data); err != nil {
		return l.errorf(abs, "%v", err)
	}
	var tasks map[string]any
	var loads []any
	if root {
		var project runtimeDekitProject
		if err := runtimeDekitDecode(data, &project); err != nil {
			return l.errorf(abs, "%v", err)
		}
		tasks, loads = project.Tasks, project.Load
		if err := l.setDefaults(abs, project.Defaults); err != nil {
			return err
		}
	} else {
		var fragment runtimeDekitFragment
		if err := runtimeDekitDecode(data, &fragment); err != nil {
			return l.errorf(abs, "%v (a fragment holds only tasks and load)", err)
		}
		tasks, loads = fragment.Tasks, fragment.Load
	}
	if err := l.addTasks(abs, mount, tasks); err != nil {
		return err
	}
	l.stack = append(l.stack, abs)
	defer func() { l.stack = l.stack[:len(l.stack)-1] }()
	for i, entry := range loads {
		pattern, at := "", ""
		switch value := entry.(type) {
		case string:
			pattern = value
		case map[string]any:
			for key := range value {
				if key != "file" && key != "at" {
					return l.errorf(abs, "load entry %d: unknown field %s", i, key)
				}
			}
			pattern, _ = value["file"].(string)
			if raw, set := value["at"]; set {
				at, _ = raw.(string)
				if !runtimeTaskPath(at) {
					return l.errorf(abs, "load entry %d: invalid at %q", i, raw)
				}
			}
		}
		if pattern == "" {
			return l.errorf(abs, "load entry %d must be a pattern or {file, at}", i)
		}
		matches, err := l.glob(filepath.Join(filepath.Dir(abs), filepath.FromSlash(pattern)))
		if err != nil {
			return l.errorf(abs, "load %q: %v", pattern, err)
		}
		if len(matches) == 0 {
			return l.errorf(abs, "load pattern %q matched no files", pattern)
		}
		next := mount
		if at != "" {
			next = runtimeJoinTaskPath(mount, at)
		}
		for _, match := range matches {
			if err := l.load(match, next, false, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func runtimeJoinTaskPath(mount, name string) string {
	if mount == "" {
		return name
	}
	return mount + "/" + name
}

func runtimeDekitRead(abs string) ([]byte, error) {
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular file")
	}
	if info.Size() > runtimeDekitMaxBytes {
		return nil, fmt.Errorf("larger than %d bytes", runtimeDekitMaxBytes)
	}
	// Open without following a final link or blocking on a FIFO, and read only
	// the file Lstat saw: a swap between the two is refused, not read.
	f, err := os.OpenFile(abs, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("changed while being read")
	}
	data, err := io.ReadAll(io.LimitReader(f, runtimeDekitMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > runtimeDekitMaxBytes {
		return nil, fmt.Errorf("larger than %d bytes", runtimeDekitMaxBytes)
	}
	return data, nil
}

// runtimeDekitTags refuses values taba cannot read as written. dekit evaluates
// !js when its runner loads the config; taba neither evaluates nor drops them.
func runtimeDekitTags(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	var walk func(*yaml.Node) error
	walk = func(n *yaml.Node) error {
		if n.Tag == "!js" {
			return fmt.Errorf("line %d: !js values are not supported: taba does not evaluate them; write the value out", n.Line)
		}
		if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
			return fmt.Errorf("line %d: unsupported tag %s", n.Line, n.Tag)
		}
		for _, child := range n.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(&doc)
}

func runtimeDekitDecode(data []byte, into any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(into); err != nil && err != io.EOF {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("expected one YAML document")
	}
	return nil
}

// setDefaults keeps dekit.yaml's task defaults, resolving their paths from it.
func (l *runtimeDekitLoader) setDefaults(abs string, defaults map[string]any) error {
	for key := range defaults {
		if !runtimeNativeDefaults[key] {
			return l.errorf(abs, "defaults.%s is not supported; defaults take cwd, env, add_path, autostart, autorestart, stop, log, scrollback_len and mouse_scroll_speed", key)
		}
	}
	l.defaults = runtimeRebasePaths(defaults, filepath.Dir(abs))
	return nil
}

// runtimeRebasePaths resolves a task's relative paths from the file that
// declares it, as dekit does.
func runtimeRebasePaths(spec map[string]any, dir string) map[string]any {
	if spec == nil {
		return nil
	}
	out := make(map[string]any, len(spec))
	for key, value := range spec {
		out[key] = value
	}
	for _, key := range []string{"cwd", "script"} {
		if value, ok := out[key].(string); ok && value != "" && !filepath.IsAbs(value) {
			out[key] = filepath.Join(dir, value)
		}
	}
	if paths, ok := out["add_path"].([]any); ok {
		rebased := make([]any, len(paths))
		for i, value := range paths {
			rebased[i] = value
			if text, ok := value.(string); ok && text != "" && !filepath.IsAbs(text) {
				rebased[i] = filepath.Join(dir, text)
			}
		}
		out["add_path"] = rebased
	}
	return out
}

func (l *runtimeDekitLoader) addTasks(abs, mount string, tasks map[string]any) error {
	names := make([]string, 0, len(tasks))
	for name := range tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		full := runtimeJoinTaskPath(mount, name)
		if folder, ok := strings.CutSuffix(full, "/"); ok {
			if !runtimeTaskPath(folder) {
				return l.errorf(abs, "invalid folder %q", name)
			}
			if _, ok := tasks[name].(map[string]any); !ok && tasks[name] != nil {
				return l.errorf(abs, "folder %q must be an object", name)
			}
			l.folders[folder] = l.name(abs)
			continue
		}
		spec, ok := tasks[name].(map[string]any)
		if !ok || !runtimeTaskPath(full) {
			return l.errorf(abs, "invalid task %q", name)
		}
		if _, set := spec["x-wtc"]; set {
			return l.errorf(abs, "task %s: x-wtc is not a dekit task field and dekit refuses it in dekit.yaml and every file it loads; a .harness/dekit.tasks.yaml that carries x-wtc must not be loaded from dekit.yaml", name)
		}
		if old, dup := l.tasks[full]; dup {
			return l.errorf(abs, "task %s is also declared in %s", full, old.decl.File)
		}
		task := runtimeRebasePaths(spec, filepath.Dir(abs))
		runtimeApplyDefaults(task, l.defaults)
		if _, set := task["cwd"]; !set && l.project != l.root {
			// dekit runs a task in its project directory by default.
			task["cwd"] = l.project
		}
		if deps, ok := task["deps"].([]any); ok {
			moved := make([]any, len(deps))
			for i, value := range deps {
				moved[i] = value
				if dep, ok := value.(string); ok {
					// `/x` is the repository's own x; others move with the mount.
					if rooted, ok := strings.CutPrefix(dep, "/"); ok {
						moved[i] = rooted
					} else {
						moved[i] = runtimeJoinTaskPath(mount, dep)
					}
				}
			}
			task["deps"] = moved
		}
		l.tasks[full] = runtimeDeclaredTask{spec: task, source: filepath.Join(l.display, filepath.FromSlash(l.name(abs))), decl: RuntimeDeclaration{Scope: "repository", File: l.name(abs), Mount: mount}, native: true}
	}
	return nil
}

// runtimeApplyDefaults gives a task dekit's defaults: its own value wins whole,
// but log combines key by key and stop in two parts (how, and timeout).
func runtimeApplyDefaults(task, defaults map[string]any) {
	for key, value := range defaults {
		own, set := task[key]
		switch {
		case !set:
			// A copy: rendering adds to a task's env in place.
			task[key] = runtimeCopyValue(value)
		case key == "log":
			base, ok1 := value.(map[string]any)
			mine, ok2 := own.(map[string]any)
			if ok1 && ok2 {
				merged := map[string]any{}
				for k, v := range base {
					merged[k] = runtimeCopyValue(v)
				}
				for k, v := range mine {
					merged[k] = v
				}
				task[key] = merged
			}
		case key == "stop":
			task[key] = runtimeMergeStop(value, own)
		}
	}
}

// runtimeCopyValue copies decoded YAML maps and lists, so tasks that take
// the same default never share one.
func runtimeCopyValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = runtimeCopyValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = runtimeCopyValue(item)
		}
		return out
	}
	return value
}

func runtimeStopParts(value any) map[string]any {
	switch stop := value.(type) {
	case string:
		return map[string]any{"signal": stop}
	case map[string]any:
		return stop
	}
	return nil
}

func runtimeMergeStop(defaults, own any) any {
	base, mine := runtimeStopParts(defaults), runtimeStopParts(own)
	if base == nil || mine == nil {
		return own
	}
	merged := map[string]any{}
	how := false
	for _, key := range []string{"signal", "group", "keys", "cmd"} {
		if _, set := mine[key]; set {
			how = true
		}
	}
	for key, value := range base {
		if key == "timeout" || !how {
			merged[key] = runtimeCopyValue(value)
		}
	}
	for key, value := range mine {
		merged[key] = runtimeCopyValue(value)
	}
	return merged
}

// checkTree applies dekit's path rules across the whole native tree.
func (l *runtimeDekitLoader) checkTree() error {
	for full := range l.tasks {
		if file, ok := l.folders[full]; ok {
			return fmt.Errorf("%s/%s: %s is a task, so it can't be a folder too", l.label, file, full)
		}
		parts := strings.Split(full, "/")
		for i := 1; i < len(parts); i++ {
			if parent := strings.Join(parts[:i], "/"); l.tasks[parent].spec != nil {
				return fmt.Errorf("%s/%s: a task can't have tasks under it: %s is under %s", l.label, l.tasks[full].decl.File, full, parent)
			}
		}
	}
	for folder, file := range l.folders {
		parts := strings.Split(folder, "/")
		for i := 1; i < len(parts); i++ {
			if parent := strings.Join(parts[:i], "/"); l.tasks[parent].spec != nil {
				return fmt.Errorf("%s/%s: folder %s is under task %s", l.label, file, folder, parent)
			}
		}
	}
	return nil
}

// glob matches a load pattern inside the repository: *, ?, [...] within a
// name and ** across directories. It never traverses a symlink.
func (l *runtimeDekitLoader) glob(pattern string) ([]string, error) {
	pattern = filepath.Clean(pattern)
	rel, err := filepath.Rel(l.root, pattern)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("the pattern leaves the repository")
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		if _, err := path.Match(part, ""); err != nil {
			return nil, err
		}
	}
	found := map[string]bool{}
	if err := l.match(l.root, parts, found); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(found))
	for match := range found {
		out = append(out, match)
	}
	sort.Strings(out)
	return out, nil
}

// linkOnTheWay refuses a symlink that a pattern names as a directory to
// descend through. Its target is never looked at, not even its metadata: it
// may be outside the repository or on a slow or remote mount.
func (l *runtimeDekitLoader) linkOnTheWay(link string) error {
	return fmt.Errorf("%s is a symlink; a load pattern does not descend through symlinks, and taba does not read symlinked declaration files or directories", l.name(link))
}

// runtimeGitDir matches .git in any case: macOS volumes ignore case.
func runtimeGitDir(name string) bool { return strings.EqualFold(name, ".git") }

func runtimeGlobMeta(part string) bool { return strings.ContainsAny(part, `*?[\`) }

func (l *runtimeDekitLoader) match(dir string, parts []string, found map[string]bool) error {
	if len(parts) == 0 {
		info, err := os.Lstat(dir)
		if err == nil && !info.IsDir() {
			found[dir] = true
		}
		return nil
	}
	part := parts[0]
	if !runtimeGlobMeta(part) {
		next := filepath.Join(dir, part)
		info, err := os.Lstat(next)
		if os.IsNotExist(err) {
			// Creating it later changes what the load matches.
			l.inputs = append(l.inputs, next)
			return nil
		} else if err != nil {
			return err
		}
		if len(parts) > 1 && info.Mode()&os.ModeSymlink != 0 {
			return l.linkOnTheWay(next)
		}
		return l.match(next, parts[1:], found)
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		l.inputs = append(l.inputs, dir)
		return nil
	} else if err != nil {
		return err
	}
	l.inputs = append(l.inputs, dir)
	if l.entries += len(entries); l.entries > runtimeDekitMaxEntries {
		return fmt.Errorf("the pattern visits more than %d directory entries", runtimeDekitMaxEntries)
	}
	if part == "**" {
		if err := l.match(dir, parts[1:], found); err != nil {
			return err
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		next := filepath.Join(dir, name)
		symlink := entry.Type()&os.ModeSymlink != 0
		if part == "**" {
			// ** does not descend into symlinks: their targets are never
			// looked at. A link matching the last name was found above and
			// is refused when loaded.
			if entry.IsDir() && !symlink && !runtimeGitDir(name) {
				if err := l.match(next, parts, found); err != nil {
					return err
				}
			}
			continue
		}
		if ok, _ := path.Match(part, name); !ok || runtimeGitDir(name) {
			continue
		}
		if len(parts) == 1 {
			if !entry.IsDir() {
				// A matched symlink is refused when it is loaded.
				found[next] = true
			}
			continue
		}
		if symlink {
			return l.linkOnTheWay(next)
		}
		if entry.IsDir() {
			if err := l.match(next, parts[1:], found); err != nil {
				return err
			}
		}
	}
	return nil
}

// runtimeInputsSignature fingerprints the files and directories a declaration
// read by metadata alone: path, mode, size, modification time and file
// identity. It opens nothing, so a swapped link or FIFO cannot be read or
// block, and it stays cheap however many fragments a tree loads.
func runtimeInputsSignature(paths []string) string {
	hash := sha256.New()
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			fmt.Fprintln(hash, path, err)
			continue
		}
		identity := ""
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			identity = fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
		}
		fmt.Fprintln(hash, path, info.Mode(), info.Size(), info.ModTime().UnixNano(), identity)
	}
	return fmt.Sprintf(":%x", hash.Sum(nil))
}
