package main

import (
	"encoding/json"
	"errors"
	"os"
	"slices"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

// This host adapter reads local gateway configuration. The HTTP service
// supplies its operator-owned allowlist; requests cannot choose these flags.
// A sandbox transport invocation must use the authenticated HTTP route.
func sandboxProjectsCommand() *cobra.Command {
	var allowed []string
	cmd := &cobra.Command{Use: "sandbox-projects", Hidden: true, Args: cobra.NoArgs}
	cmd.Flags().StringArrayVar(&allowed, "project", nil, "Operator-allowed project (repeatable)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if openclaw.GatewayFromEnv(os.Getenv) != nil {
			return errors.New("sandbox project discovery must use the authenticated gateway service")
		}
		if len(allowed) == 0 {
			return errors.New("project discovery is not enabled for this agent")
		}
		result := openclaw.SandboxProjects{}
		if cwd, err := os.Getwd(); err == nil {
			result.Collection = openclaw.CurrentWorktree(cwd)
		}
		projects, err := localGatewayProjects(allowed, result.Collection)
		if err != nil {
			return errors.New("gateway project metadata unavailable")
		}
		result.Projects = projects
		if result.Collection != "" {
			if folder := openclaw.CollectionFolder(gatewayStateDir(), result.Collection); folder != "" {
				result.CollectionProject, _, err = wtc.GatewayHarness(folder)
				if err != nil || (result.CollectionProject != "" && result.CollectionProject != openclaw.NoHarness && !slices.Contains(allowed, result.CollectionProject)) {
					return errors.New("gateway collection metadata unavailable")
				}
			}
			// An existing harness worktree establishes the collection's project
			// when no separate record exists; multiple candidates are refused.
			if result.CollectionProject == "" {
				for _, project := range projects {
					if project.HasWorktree {
						if result.CollectionProject != "" {
							return errors.New("gateway collection metadata is ambiguous")
						}
						result.CollectionProject = project.Name
					}
				}
			}
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	return cmd
}

// harnessProjectContext uses one snapshot per add-repo plan. When a gateway
// URL is configured, unavailable or denied discovery never becomes an empty
// local config or an ad-hoc plan.
func harnessProjectContext(worktree string) (openclaw.GatewayConfig, map[string]harnessRegistry, string, error) {
	registries := map[string]harnessRegistry{}
	gw := openclaw.GatewayFromEnv(os.Getenv)
	if gw == nil {
		cfg, err := gatewayConfig()
		return cfg, registries, "", err
	}
	metadata, err := gw.Projects()
	if err != nil {
		return openclaw.GatewayConfig{}, nil, "", err
	}
	if metadata.Collection == "" || metadata.Collection != worktree {
		return openclaw.GatewayConfig{}, nil, "", errors.New("prepare plans for another collection in its gateway collection session: sandbox metadata describes only this session's collection")
	}
	var cfg openclaw.GatewayConfig
	for _, p := range metadata.Projects {
		cfg.Harnesses = append(cfg.Harnesses, openclaw.Harness{Name: p.Name, Remote: p.Harness, Ref: p.Ref})
		if p.Default {
			cfg.DefaultHarness = p.Name
		}
		reg := harnessRegistry{registeredRemotes: map[string]string{}, originAliases: map[string][]string{}, projectID: p.ProjectID, projectIDs: map[string]string{}, destination: p.Destination, destinations: map[string]string{}, hasWorktree: p.HasWorktree}
		if p.Error != "" {
			reg.err = errors.New(p.Error)
		}
		for _, r := range p.Repos {
			reg.repos = append(reg.repos, wtc.Repo{Name: r.Name, Aliases: r.Aliases, Remote: r.Remote, Role: r.Role, DefaultRef: r.DefaultRef})
			reg.registeredRemotes[r.Name] = r.RegisteredRemote
			reg.originAliases[r.Name] = append([]string{}, r.OriginAliases...)
			reg.projectIDs[r.Name] = r.ProjectID
			reg.destinations[r.Name] = r.Destination
		}
		registries[p.Name] = reg
	}
	recorded := ""
	if metadata.Collection == worktree {
		recorded = metadata.CollectionProject
	}
	return cfg, registries, recorded, nil
}
