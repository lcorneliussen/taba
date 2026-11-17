package wtc

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type RequirementsOptions struct {
	Examples           []string
	Repo               string
	ProcessEnvironment bool
	Lint               bool
}
type RequirementsDiagnostic struct {
	Repository  string `json:"repository"`
	Declaration string `json:"declaration,omitempty"`
	Code        string `json:"code"`
	Line        int    `json:"line,omitempty"`
}
type RequirementsInventory struct {
	Items          []RequirementItem        `json:"items"`
	Diagnostics    []RequirementsDiagnostic `json:"diagnostics"`
	LintOK         bool                     `json:"lint_ok"`
	ReadinessOK    bool                     `json:"readiness_ok"`
	LocalReadiness string                   `json:"local_readiness"`
}
type requirementRepository struct{ name, dir string }
type selectedRequirements struct {
	repo    requirementRepository
	example string
	schema  RequirementSchema
}

// ListConfigRequirements is the single collection inventory producer. All
// discovery is bounded to two tracked root files or explicitly selected paths.
func (c *Context) ListConfigRequirements(opt RequirementsOptions) (RequirementsInventory, error) {
	repos := []requirementRepository{}
	harnessName, _ := c.HarnessRepoName()
	for _, repo := range c.Registry.Repos {
		if !validRepoName.MatchString(repo.Name) {
			return RequirementsInventory{}, fmt.Errorf("invalid requirements repository identity")
		}
		if opt.Repo != "" && !repo.Answers(opt.Repo) && !(opt.Repo == "harness" && repo.Name == harnessName) {
			continue
		}
		dir := c.WorktreeDir(repo.Name)
		if repo.Name == harnessName {
			dir = c.Harness
		}
		if _, err := os.Lstat(dir); os.IsNotExist(err) {
			continue
		}
		root, err := canonicalPath(dir)
		collection, e := canonicalPath(c.Collection)
		if err != nil || e != nil || !withinPath(collection, root) || root == collection || !validInventoryWorktree(dir) {
			explicit := opt.Repo != ""
			for _, selection := range opt.Examples {
				if strings.HasPrefix(selection, repo.Name+":") {
					explicit = true
				}
			}
			if explicit {
				return RequirementsInventory{}, fmt.Errorf("cannot verify requirements repository ownership")
			}
			// Without an explicit selection, a non-worktree directory cannot
			// opt in through Git-tracked examples. Do not inspect its files.
			continue
		}
		repos = append(repos, requirementRepository{repo.Name, root})
	}
	if opt.Repo != "" && len(repos) == 0 {
		return RequirementsInventory{}, fmt.Errorf("selected requirements repository is not present")
	}
	return inspectConfigRequirements(c, repos, opt)
}

// LintRepositoryRequirements supports credential-free static CI in an ordinary
// checkout. Project sources have no receiving authorization here and are not
// opened. Selected collection sources may be checked when provided.
func LintRepositoryRequirements(root string, examples []string) (RequirementsInventory, error) {
	resolved, err := canonicalPath(root)
	if err != nil || !validInventoryWorktree(resolved) {
		return RequirementsInventory{}, fmt.Errorf("config lint requires a repository root")
	}
	selections := make([]string, 0, len(examples))
	for _, example := range examples {
		if strings.Contains(example, ":") {
			return RequirementsInventory{}, fmt.Errorf("standalone lint examples must be repository-relative paths")
		}
		selections = append(selections, "repository:"+example)
	}
	return inspectConfigRequirements(nil, []requirementRepository{{"repository", resolved}}, RequirementsOptions{Examples: selections, Lint: true})
}

func inspectConfigRequirements(c *Context, repos []requirementRepository, opt RequirementsOptions) (RequirementsInventory, error) {
	result := RequirementsInventory{Items: []RequirementItem{}, Diagnostics: []RequirementsDiagnostic{}, LintOK: true, ReadinessOK: true, LocalReadiness: "unverified"}
	diag := func(repo, example, code string, line int) {
		result.Diagnostics = append(result.Diagnostics, RequirementsDiagnostic{repo, example, code, line})
		result.LintOK = false
		result.ReadinessOK = false
	}
	selected := []selectedRequirements{}
	explicit := map[string][]string{}
	for _, selection := range opt.Examples {
		repo, p, ok := strings.Cut(selection, ":")
		if !ok || !validRepoName.MatchString(repo) || !requirementPath(p) {
			return result, fmt.Errorf("--example requires a present repository and relative example path")
		}
		found := false
		for _, r := range repos {
			if r.name == repo {
				found = true
			}
		}
		if !found {
			return result, fmt.Errorf("example repository is not selected or present")
		}
		explicit[repo] = append(explicit[repo], p)
	}
	for _, repo := range repos {
		paths := explicit[repo.name]
		if len(opt.Examples) == 0 {
			for _, p := range []string{".env.example", ".env.local.example"} {
				if _, err := gitOutput("-C", repo.dir, "ls-files", "--error-unmatch", "--", p); err == nil {
					paths = append(paths, p)
				}
			}
		}
		seen := map[string]bool{}
		for _, p := range paths {
			if seen[p] {
				diag(repo.name, p, "duplicate-example-selection", 0)
				continue
			}
			seen[p] = true
			data, state := readRequirementFile(repo.dir, p, "", true)
			if state != "inspected" {
				diag(repo.name, p, "unverified-example", 0)
				continue
			}
			if !hasRequirementsAnnotation(data) {
				// Legacy examples remain hints. Explicit selection makes that lack of a
				// schema visible without asserting that every legacy key is mandatory.
				if len(opt.Examples) > 0 {
					result.Diagnostics = append(result.Diagnostics, RequirementsDiagnostic{Repository: repo.name, Declaration: p, Code: "legacy-example-hint"})
				}
				continue
			}
			schema, err := ParseConfigRequirements(p, bytes.NewReader(data))
			if err != nil {
				e := err.(*RequirementError)
				diag(repo.name, p, e.Code, e.Line)
				continue
			}
			selected = append(selected, selectedRequirements{repo, p, schema})
		}
	}
	// Reject conflicting canonical ownership before inspecting actual inputs.
	owners := map[string]bool{}
	envOwners := []struct {
		repo, name, group string
		prefix            bool
	}{}
	for _, s := range selected {
		for _, source := range s.schema.Sources.Files {
			key := s.repo.name + ":" + source.Scope + ":" + source.Path
			if owners[key] {
				diag(s.repo.name, s.example, "duplicate-source-ownership", 0)
			}
			owners[key] = true
		}
		for _, req := range s.schema.Requirements {
			target := "key:"
			if req.File != "" {
				target = "file:"
			}
			key := s.repo.name + ":" + target + req.Name
			if owners[key] {
				diag(s.repo.name, s.example, "duplicate-requirement-ownership", 0)
			}
			owners[key] = true
		}
		for _, name := range s.schema.Sources.EnvNames {
			envOwners = append(envOwners, struct {
				repo, name, group string
				prefix            bool
			}{s.repo.name, name, s.example, false})
		}
		for _, name := range s.schema.Sources.EnvPrefixes {
			envOwners = append(envOwners, struct {
				repo, name, group string
				prefix            bool
			}{s.repo.name, name, s.example, true})
		}
	}
	for i, a := range envOwners {
		for _, b := range envOwners[:i] {
			if a.repo == b.repo && a.group == b.group {
				continue
			}
			overlap := a.name == b.name || a.prefix && strings.HasPrefix(b.name, a.name) || b.prefix && strings.HasPrefix(a.name, b.name)
			if overlap {
				diag(a.repo, "", "ambiguous-process-ownership", 0)
			}
		}
	}
	if !result.LintOK {
		return result, nil
	}
	projectRoot := ""
	if c != nil && len(selected) > 0 {
		scope, err := c.ValidateControlRoot()
		if err != nil {
			diag("", "", "unverified-receiving-scope", 0)
			return result, nil
		}
		projectRoot = scope.Root
	}
	actualOwners := map[string]string{}
	for _, s := range selected {
		group := s.repo.name + ":" + s.example
		for _, source := range s.schema.Sources.Files {
			base, allowed := requirementsSourceRoot(s.repo, source.Scope, projectRoot)
			if !allowed {
				continue
			}
			actual, err := canonicalPath(filepath.Join(base, source.Path))
			if err != nil {
				continue
			}
			if previous, exists := actualOwners[actual]; exists && previous != group {
				diag(s.repo.name, s.example, "duplicate-actual-source-ownership", 0)
			}
			actualOwners[actual] = group
		}
	}
	if !result.LintOK {
		return result, nil
	}
	for _, s := range selected {
		inputs := []RequirementInput{}
		for _, source := range s.schema.Sources.Files {
			base, allowed := requirementsSourceRoot(s.repo, source.Scope, projectRoot)
			input := RequirementInput{Source: source, State: "missing"}
			if !allowed {
				if !opt.Lint {
					input.State = "unverified"
				}
				inputs = append(inputs, input)
				continue
			}
			data, state := readRequirementFile(base, source.Path, requirementsProjectNamespace(s.repo, projectRoot), false)
			input.State = state
			if state == "inspected" {
				resolved, _ := canonicalPath(filepath.Join(base, source.Path))
				if projectRoot != "" && withinPath(requirementsProjectNamespace(s.repo, projectRoot), resolved) {
					input.Provenance = "receiving-project-file"
					input.EffectiveScope = "project"
				} else {
					input.Provenance = "collection-repository-file"
					input.EffectiveScope = "collection"
				}
			}
			if state == "inspected" {
				values, err := ParseRequirementInput(bytes.NewReader(data))
				if err != nil {
					e := err.(*RequirementError)
					diag(s.repo.name, s.example, e.Code, e.Line)
					input.State = "unverified"
				} else {
					input.Values = values
				}
			} else if state != "missing" {
				diag(s.repo.name, s.example, "unverified-owned-input", 0)
			}
			inputs = append(inputs, input)
		}
		var env map[string]string
		if opt.ProcessEnvironment {
			env = map[string]string{}
			// Enumerating is only enabled by the invocation, and only the schema's
			// exact names/prefixes are retained. Unrelated process values never enter
			// the effective configuration or diagnostics.
			for _, entry := range os.Environ() {
				k, v, ok := strings.Cut(entry, "=")
				if !ok {
					continue
				}
				owned := false
				for _, name := range s.schema.Sources.EnvNames {
					if k == name {
						owned = true
					}
				}
				for _, prefix := range s.schema.Sources.EnvPrefixes {
					if strings.HasPrefix(k, prefix) {
						owned = true
					}
				}
				if owned {
					env[k] = v
				}
			}
		}
		fileStates := map[string]string{}
		fileScopes := map[string]string{}
		fileProvenance := map[string]string{}
		for _, req := range s.schema.Requirements {
			if req.File == "" {
				continue
			}
			base, allowed := requirementsSourceRoot(s.repo, req.Scope, projectRoot)
			if !allowed {
				fileStates[req.File] = "unverified"
				continue
			}
			resolved, state := requirementFilePath(base, req.File, requirementsProjectNamespace(s.repo, projectRoot), false)
			if state == "inspected" {
				state = "present"
				fileScopes[req.File] = "collection"
				fileProvenance[req.File] = "collection-repository-file"
				if projectRoot != "" && withinPath(requirementsProjectNamespace(s.repo, projectRoot), resolved) {
					fileScopes[req.File] = "project"
					fileProvenance[req.File] = "receiving-project-file"
				}
			}
			fileStates[req.File] = state
		}
		items := ConfigRequirementsInventory(s.schema, inputs, env, fileStates)
		for i := range items {
			item := &items[i]
			item.Repository = s.repo.name
			item.Declaration = s.example
			if item.Target == "file" && fileScopes[item.Name] != "" {
				item.Scope = fileScopes[item.Name]
				item.Provenance = fileProvenance[item.Name]
			}
			if item.State == "undocumented" {
				result.LintOK = false
				result.ReadinessOK = false
			}
			if item.Required != "optional" && item.Applicability != "inapplicable" && item.State != "present" {
				result.ReadinessOK = false
			}
		}
		result.Items = append(result.Items, items...)
	}
	sort.Slice(result.Items, func(i, j int) bool {
		a, b := result.Items[i], result.Items[j]
		if a.Repository != b.Repository {
			return a.Repository < b.Repository
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Target < b.Target
	})
	return result, nil
}

func hasRequirementsAnnotation(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if strings.HasPrefix(strings.TrimSpace(string(line)), "# taba:requirements") {
			return true
		}
	}
	return false
}
func requirementsProjectNamespace(repo requirementRepository, project string) string {
	if project == "" {
		return ""
	}
	return filepath.Join(project, repo.name)
}
func requirementsSourceRoot(repo requirementRepository, scope, project string) (string, bool) {
	if scope == "collection" {
		return repo.dir, true
	}
	if project == "" {
		return "", false
	}
	namespace := requirementsProjectNamespace(repo, project)
	resolved, err := canonicalPath(namespace)
	return namespace, err == nil && resolved == namespace
}

// requirementFilePath authorizes the exact regular file before opening it.
// A worktree secret link is permitted only into this repository's validated
// receiving namespace; another repository's control namespace is never enough.
func requirementFilePath(root, rel, projectNamespace string, example bool) (string, string) {
	if !requirementPath(rel) {
		return "", "unverified"
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	resolved, err := canonicalPath(p)
	if err != nil {
		return "", "unverified"
	}
	allowed := withinPath(root, resolved)
	if !example && projectNamespace != "" {
		namespace, e := canonicalPath(projectNamespace)
		if e == nil && namespace == projectNamespace && withinPath(namespace, resolved) {
			allowed = true
		}
	}
	if !allowed {
		return "", "unverified"
	}
	info, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return "", "missing"
	}
	if err != nil {
		return "", "unverified"
	}
	if example && info.Mode()&os.ModeSymlink != 0 {
		return "", "unverified"
	}
	info, err = os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Mode().Perm()&0022 != 0 {
		return "", "unverified"
	}
	if info.Size() > requirementMaxBytes {
		return "", "unverified"
	}
	return resolved, "inspected"
}
func readRequirementFile(root, rel, projectNamespace string, example bool) ([]byte, string) {
	p, state := requirementFilePath(root, rel, projectNamespace, example)
	if state != "inspected" {
		return nil, state
	}
	openRoot := root
	if !withinPath(root, p) {
		openRoot = projectNamespace
	}
	relPath, e := filepath.Rel(openRoot, p)
	if e != nil {
		return nil, "unverified"
	}
	f, err := os.OpenInRoot(openRoot, relPath)
	if err != nil {
		return nil, "unverified"
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Mode().Perm()&0022 != 0 || info.Size() > requirementMaxBytes {
		return nil, "unverified"
	}
	data, err := io.ReadAll(io.LimitReader(f, requirementMaxBytes+1))
	if err != nil || len(data) > requirementMaxBytes {
		return nil, "unverified"
	}
	return data, "inspected"
}
