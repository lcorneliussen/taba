package wtc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// runtimeWithDeclarations adds tasks that have not been rendered yet. Observed
// items win in their entirety: declarations cannot replace a live URL, failure,
// resource state or tunnel choice. This path never plans or applies a runtime.
func (c *Context) runtimeWithDeclarations(observed []RuntimeItem) []RuntimeItem {
	seen := make(map[string]bool, len(observed))
	for _, item := range observed {
		seen[item.Path] = true
	}
	for _, item := range c.runtimeDeclarations() {
		if !seen[item.Path] {
			observed = append(observed, item)
			seen[item.Path] = true
		}
	}
	return c.recordedTunnelItems(observed)
}

func (c *Context) runtimeDeclarations() []RuntimeItem {
	_, repos, err := c.CatchUpInventory(CatchUpOptions{DryRun: true})
	if err != nil {
		return []RuntimeItem{c.runtimeDeclarationError("", err)}
	}
	var items []RuntimeItem
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
		declared, err := c.runtimeRepoDeclarations(repo)
		if err != nil {
			items = append(items, c.runtimeDeclarationError(repo.dir, err))
			continue
		}
		items = append(items, declared...)
	}
	return items
}

func (c *Context) runtimeDeclarationError(dir string, err error) RuntimeItem {
	if dir == "" {
		dir = "runtime"
	}
	return RuntimeItem{Collection: filepath.Base(c.Collection), Dir: dir, Path: dir + "/@declarations", Group: dir, Kind: "configuration", State: "unknown", Error: err.Error()}
}

func (c *Context) runtimeRepoDeclarations(repo runtimeRepo) ([]RuntimeItem, error) {
	collectionRoot, err := filepath.EvalSymlinks(c.Collection)
	if err != nil {
		return nil, err
	}
	// Repository links are supported collection entries. Their shared files
	// belong to the resolved checkout; local additions belong to the collection.
	repoRoot, err := filepath.EvalSymlinks(repo.path)
	if err != nil {
		return nil, err
	}
	shared := runtimeSharedFragment(repo.path)
	local := c.runtimeLocalFragment(repo.dir)
	tasks := map[string]map[string]any{}
	sources := map[string]string{}
	for _, path := range []string{shared, local} {
		root, safe := collectionRoot, path
		if path == shared {
			root, safe = repoRoot, runtimeSharedFragment(repoRoot)
		}
		if err := runtimeSafeParents(root, safe); err != nil {
			return nil, err
		}
		if err := runtimeRegular(safe); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		fragment, err := runtimeReadFragment(safe)
		if err != nil {
			return nil, err
		}
		for name, task := range fragment.Tasks {
			if !runtimeTaskPath(name) || task == nil {
				return nil, fmt.Errorf("invalid declared task path %q", name)
			}
			if _, exists := tasks[name]; exists {
				return nil, fmt.Errorf("%s/%s is defined in both shared and local task files", repo.dir, name)
			}
			tasks[name], sources[name] = task, path
		}
	}
	names := make([]string, 0, len(tasks))
	for name := range tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	var items []RuntimeItem
	for _, name := range names {
		task := tasks[name]
		item := RuntimeItem{Collection: filepath.Base(c.Collection), Repo: repo.repo, Dir: repo.dir, Path: repo.dir + "/" + name, Group: repo.dir + "/" + strings.Split(name, "/")[0], Kind: "service", State: "unconfigured", Source: sources[name]}
		if task["type"] == "job" {
			item.Kind = "job"
		}
		item.Label, _ = task["label"].(string)
		item.Autostart, _ = task["autostart"].(bool)
		if tags, ok := task["tags"].([]any); ok {
			for _, tag := range tags {
				if tag == "endpoint" || tag == "accessory" {
					item.Kind = tag.(string)
				}
			}
		}
		if value, exists := task["x-wtc"]; exists {
			meta, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s: x-wtc must be an object", item.Path)
			}
			item.Mark, _ = meta["mark"].(string)
			item.PortEnv, _ = meta["port_env"].(string)
			// Environment-derived URLs and provider state require runtime setup;
			// discovery only shows safe literal metadata, without sourcing anything.
			if url, _ := meta["url"].(string); url != "" {
				if err := runtimeURLSafe(url); err != nil {
					return nil, fmt.Errorf("%s: %w", item.Path, err)
				}
				item.URL = url
			}
		}
		items = append(items, item)
	}
	return items, nil
}
