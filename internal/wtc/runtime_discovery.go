package wtc

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// runtimeWithDeclarations adds tasks that have not been rendered yet. Observed
// items win: declarations cannot replace a live URL, failure, resource state or
// tunnel choice, and only fill a missing Declaration. This path never plans or
// applies a runtime.
func (c *Context) runtimeWithDeclarations(observed []RuntimeItem) []RuntimeItem {
	seen := make(map[string]bool, len(observed))
	for _, item := range observed {
		seen[item.Path] = true
	}
	declared := c.runtimeDeclarations
	if c.runtimeDeclarationsCache != nil {
		declared = c.cachedRuntimeDeclarations
	}
	files := map[string]RuntimeItem{}
	for _, item := range declared() {
		if !seen[item.Path] {
			observed = append(observed, item)
			seen[item.Path] = true
		} else if item.Declaration != nil {
			files[item.Path] = item
		}
	}
	// A task rendered before declarations were recorded keeps every observed
	// fact and gains only the declaring file, when it still names that file.
	for i := range observed {
		if file, ok := files[observed[i].Path]; ok && observed[i].Declaration == nil && !observed[i].ManagedTunnel && observed[i].Source != "" && observed[i].Source == file.Source {
			observed[i].Declaration = file.Declaration
		}
	}
	return c.recordedTunnelItems(observed)
}

func (c *Context) runtimeDeclarations() []RuntimeItem {
	items, _ := c.runtimeDeclarationsInputs()
	return items
}

func (c *Context) runtimeDeclarationsInputs() ([]RuntimeItem, []string) {
	_, repos, err := c.CatchUpInventory(CatchUpOptions{DryRun: true})
	if err != nil {
		return []RuntimeItem{c.runtimeDeclarationError("", err)}, nil
	}
	var items []RuntimeItem
	var inputs []string
	identities := c.prRepositories()
	for _, entry := range repos {
		repo := runtimeRepo{dir: filepath.Base(entry.path), repo: entry.repo, path: entry.path}
		identity, identityErr := identities.resolve(repo.dir)
		if identityErr != nil {
			items = append(items, c.runtimeDeclarationError(repo.dir, identityErr))
			continue
		}
		if identity != nil {
			repo.repo = identity.name
		}
		declared, read, err := c.runtimeRepoDeclarationsInputs(repo)
		inputs = append(inputs, read...)
		if err != nil {
			items = append(items, c.runtimeDeclarationError(repo.dir, err))
			continue
		}
		items = append(items, declared...)
	}
	return items, inputs
}

func (c *Context) runtimeDeclarationError(dir string, err error) RuntimeItem {
	if dir == "" {
		dir = "runtime"
	}
	return RuntimeItem{Collection: filepath.Base(c.Collection), Dir: dir, Path: dir + "/@declarations", Group: dir, Kind: "configuration", State: "unknown", Error: err.Error()}
}

// runtimeRepoDeclarationsInputs also returns the files and directories read,
// so a cache can see a changed fragment that dekit.yaml loads.
func (c *Context) runtimeRepoDeclarationsInputs(repo runtimeRepo) ([]RuntimeItem, []string, error) {
	// Repository links are supported collection entries. Their shared files
	// belong to the resolved checkout; local additions belong to the collection.
	sources, err := c.runtimeRepoSources(repo.path, repo.dir, true)
	if err != nil {
		return nil, sources.inputs, err
	}
	tasks := map[string]map[string]any{}
	taskFiles := map[string]string{}
	declarations := map[string]RuntimeDeclaration{}
	for name, task := range sources.tasks {
		if !runtimeTaskPath(name) || task.spec == nil {
			return nil, sources.inputs, fmt.Errorf("invalid declared task path %q", name)
		}
		tasks[name], taskFiles[name], declarations[name] = task.spec, task.source, task.decl
	}
	names := make([]string, 0, len(tasks))
	for name := range tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	var items []RuntimeItem
	for _, name := range names {
		task := tasks[name]
		item := RuntimeItem{Collection: filepath.Base(c.Collection), Repo: repo.repo, Dir: repo.dir, Path: repo.dir + "/" + name, Group: repo.dir + "/" + strings.Split(name, "/")[0], Kind: "service", State: "unconfigured", Source: taskFiles[name]}
		if decl, ok := declarations[name]; ok {
			item.Declaration = &decl
		}
		if task["type"] == "job" {
			item.Kind = "job"
		}
		item.Label, _ = task["label"].(string)
		item.Autostart, _ = task["autostart"].(bool)
		if err := runtimeTags(&item, task); err != nil {
			return nil, sources.inputs, err
		}
		if value, exists := task["x-wtc"]; exists {
			meta, ok := value.(map[string]any)
			if !ok {
				return nil, sources.inputs, fmt.Errorf("%s: x-wtc must be an object", item.Path)
			}
			item.Mark, _ = meta["mark"].(string)
			item.PortEnv, _ = meta["port_env"].(string)
			// Environment-derived URLs and provider state require runtime setup;
			// discovery only shows safe literal metadata, without sourcing anything.
			if url, _ := meta["url"].(string); url != "" {
				if err := runtimeURLSafe(url); err != nil {
					return nil, sources.inputs, fmt.Errorf("%s: %w", item.Path, err)
				}
				item.URL = url
			}
		}
		// Rendering shows a literal ready.http when nothing else names the
		// URL; discovery can tell that only without url_env or port_env.
		meta, _ := task["x-wtc"].(map[string]any)
		if item.URL == "" && meta["url_env"] == nil && meta["port_env"] == nil {
			if ready, ok := task["ready"].(map[string]any); ok {
				if probe, _ := ready["http"].(string); runtimeURLSafe(probe) == nil {
					item.URL = probe
				}
			}
		}
		items = append(items, item)
	}
	return items, sources.inputs, nil
}

func runtimeTags(item *RuntimeItem, task map[string]any) error {
	if task["tags"] == nil {
		return nil
	}
	tags, ok := task["tags"].([]any)
	if !ok {
		return fmt.Errorf("%s: tags must be a list of strings", item.Path)
	}
	for _, value := range tags {
		tag, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s: tags must be a list of strings", item.Path)
		}
		if tag == "endpoint" || tag == "accessory" {
			item.Kind = tag
		}
		item.Tags = append(item.Tags, tag)
	}
	return nil
}
