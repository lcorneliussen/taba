package wtc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"
)

// GatewayPort is the narrow port projection of a verified project entry.
// Remote binds it to the existing local row; Names retain renamed port variables.
type GatewayPort struct {
	Name, Remote string
	Offset       int
	Names        []string
}

// InheritGatewayPorts fills absent port metadata on gateway rows. Explicit null,
// zero, aliases and unrelated fields remain operator-owned. It updates local
// port metadata and the generated env, never hooks, local overrides or services.
func (c *Context) InheritGatewayPorts(ports []GatewayPort) error {
	if !c.AdHoc {
		return fmt.Errorf("gateway ports require a collection-local registry")
	}
	path := c.registryPath()
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("gateway port registry must be a regular file")
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(old, &doc); err != nil {
		return err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("gateway registry requires a mapping")
	}
	field := func(node *yaml.Node, key string) *yaml.Node {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return node.Content[i+1]
			}
		}
		return nil
	}
	rows := field(doc.Content[0], "repos")
	if rows != nil && rows.Tag == "!!null" {
		rows = &yaml.Node{Kind: yaml.SequenceNode}
	}
	if rows == nil || rows.Kind != yaml.SequenceNode {
		return fmt.Errorf("gateway registry requires a repos sequence")
	}
	changed := false
	seen := map[string]bool{}
	for _, p := range ports {
		if !validRepoName.MatchString(p.Name) || seen[p.Name] || p.Offset < 0 || p.Offset >= 100 || len(p.Names) == 0 {
			return fmt.Errorf("invalid or ambiguous project ports for %s", p.Name)
		}
		seen[p.Name] = true
		for _, name := range p.Names {
			if !validRepoName.MatchString(name) || name == "harness" && name != p.Name {
				return fmt.Errorf("invalid project port name %q", name)
			}
		}
		for _, row := range rows.Content {
			name, remote := field(row, "name"), field(row, "remote")
			if name == nil || remote == nil || name.Value != p.Name || remote.Value != p.Remote {
				continue
			}
			add := func(key string, value any) error {
				var node yaml.Node
				if err := node.Encode(value); err != nil {
					return err
				}
				row.Content = append(row.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &node)
				changed = true
				return nil
			}
			if field(row, "port_offset") == nil {
				if err := add("port_offset", p.Offset); err != nil {
					return err
				}
			}
			// Never replace an explicit alias list. It also controls which port
			// names the operator wants exported for this row.
			if field(row, "aliases") == nil {
				var aliases []string
				for _, alias := range p.Names {
					if alias != p.Name && !slices.Contains(aliases, alias) {
						aliases = append(aliases, alias)
					}
				}
				if len(aliases) > 0 {
					if err := add("aliases", aliases); err != nil {
						return err
					}
				}
			}
		}
	}
	data := old
	if changed {
		if data, err = yaml.Marshal(&doc); err != nil {
			return err
		}
	}
	reg, err := ParseRegistry(data)
	if err != nil {
		return err
	}
	// Validate the projected namespace before either generated file is written.
	names, keys := map[string]bool{}, map[string]bool{}
	for _, repo := range reg.Repos {
		for _, name := range repo.Names() {
			if !validRepoName.MatchString(name) || names[name] {
				return fmt.Errorf("project port name collides with repository or alias %q", name)
			}
			names[name] = true
			if repo.PortOffset != nil {
				if keys[portKey(name)] {
					return fmt.Errorf("project port variable collision: %s", portKey(name))
				}
				keys[portKey(name)] = true
			}
		}
	}
	projected := *c
	projected.Registry = reg
	env, err := projected.RenderEnv()
	if err != nil {
		return err
	}
	if changed {
		file, err := os.CreateTemp(filepath.Dir(path), ".gateway-ports-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if _, err = file.Write(data); err == nil {
			err = file.Chmod(info.Mode().Perm())
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(file.Name(), path); err != nil {
			return err
		}
	}
	c.Registry = reg
	current, err := os.ReadFile(filepath.Join(c.Collection, ".env.collection"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !bytes.Equal(current, env) {
		return c.WriteEnv(env)
	}
	return nil
}
