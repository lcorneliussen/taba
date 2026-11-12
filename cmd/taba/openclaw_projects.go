package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/openclaw"
)

type gatewayProject = openclaw.DiscoveryProject
type gatewayProjectRepo = openclaw.ProjectRepository

// gatewayProjects lists the gateway's wtc projects with their repositories,
// read from the gateway's clone of each harness.
func gatewayProjects() ([]gatewayProject, error) {
	if gw := openclaw.GatewayFromEnv(os.Getenv); gw != nil {
		p, err := gw.Projects()
		return p.Projects, err
	}
	return localGatewayProjects(nil, "")
}

// A nil allowlist is the gateway's local listing. A non-nil list is the
// sandbox projection: filter before reading registries and omit raw errors.
func localGatewayProjects(allowed []string, worktree string) ([]gatewayProject, error) {
	cfg, err := gatewayConfig()
	if err != nil {
		return nil, err
	}
	projects := []gatewayProject{}
	for _, h := range cfg.Harnesses {
		if allowed != nil && !slices.Contains(allowed, h.Name) {
			continue
		}
		p := gatewayProject{Name: h.Name, Harness: h.Remote, Ref: h.Ref, Default: h.Name == cfg.DefaultHarness, Repos: []gatewayProjectRepo{}}
		reg := readHarnessRegistry(h, worktree)
		if reg.err != nil {
			p.Error = reg.err.Error()
		}
		for _, r := range reg.repos {
			p.Repos = append(p.Repos, gatewayProjectRepo{Name: r.Name, Aliases: r.Aliases, Remote: r.Remote, Role: r.Role})
		}
		if allowed != nil {
			p = sandboxProjectMetadata(p, h, reg, worktree)
		}
		projects = append(projects, p)
	}
	return projects, nil
}

// sandboxProjectMetadata projects the registry, current clone origins and
// operator-verified origin aliases without credentials, local paths, arbitrary
// config keys, or command diagnostics. Unsupported remotes
// make the project's registry unavailable rather than exposing filesystem data.
func sandboxProjectMetadata(p gatewayProject, h openclaw.Harness, reg harnessRegistry, worktree string) gatewayProject {
	var err error
	p.Harness, err = projectMetadataRemote(h.Remote)
	if err != nil || p.Harness == "" {
		p.Error = "project harness metadata unavailable on the gateway"
		p.Repos = []gatewayProjectRepo{}
		return p
	}
	if dir := openclaw.GatewayProjectDir(gatewayStateDir(), openclaw.ParseOrigin(p.Harness)); dir != "" {
		p.ProjectID = filepath.Base(dir)
		p.Destination = openclaw.ProjectDestination(dir, worktree)
	}
	p.Repos = []gatewayProjectRepo{}
	p.HasWorktree = reg.worktree != ""
	if reg.err != nil {
		p.Error = "project registry unavailable on the gateway"
		return p
	}
	for _, r := range reg.repos {
		remote, err := projectMetadataRemote(r.Remote)
		if err != nil || remote == "" {
			p.Error = "project registry contains an unsupported remote"
			p.Repos = []gatewayProjectRepo{}
			return p
		}
		repo := gatewayProjectRepo{Name: r.Name, Aliases: append([]string{}, r.Aliases...), RegisteredRemote: remote, Remote: remote, Role: r.Role, DefaultRef: r.DefaultRef}
		if dir := openclaw.GatewayProjectDir(gatewayStateDir(), openclaw.ParseOrigin(remote)); dir != "" {
			repo.ProjectID = filepath.Base(dir)
			repo.Destination = openclaw.ProjectDestination(dir, worktree)
			for _, alias := range openclaw.GatewayProjectOrigins(dir) {
				host := "github.com"
				if alias.Forge == openclaw.ForgeBitbucket {
					host = "bitbucket.org"
				}
				if remote, err := projectMetadataRemote("https://" + host + "/" + alias.Owner + "/" + alias.Repo + ".git"); err == nil {
					repo.OriginAliases = append(repo.OriginAliases, remote)
				}
			}
			previous := openclaw.ParseOrigin(remote)
			current := openclaw.GatewayProjectOrigin(dir)
			if current.Forge == previous.Forge {
				host := "github.com"
				if current.Forge == openclaw.ForgeBitbucket {
					host = "bitbucket.org"
				}
				if canonical, err := projectMetadataRemote("https://" + host + "/" + current.Owner + "/" + current.Repo + ".git"); err == nil {
					repo.Remote = canonical

				}
			}
		}
		p.Repos = append(p.Repos, repo)
	}
	return p
}

var metadataRepoPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func projectMetadataRemote(remote string) (string, error) {
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return "", fmt.Errorf("unsupported remote")
		}
		u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
		remote = u.String()
	}
	o := openclaw.ParseOrigin(remote)
	if !metadataRepoPart.MatchString(o.Owner) || !metadataRepoPart.MatchString(o.Repo) || o.Owner == "." || o.Owner == ".." || o.Repo == "." || o.Repo == ".." {
		return "", fmt.Errorf("unsupported remote")
	}
	_, _, canonical, err := openclaw.ProjectSource(remote)
	return canonical, err
}

func addProjectsCommand(root *cobra.Command, asJSON *bool) {
	cmd := &cobra.Command{
		Use:   "projects",
		Short: branding.Text("Under OpenClaw: list the gateway's {{command}} projects and their repositories"),
		Long: branding.Text(`A wtc project is a named set of repositories: a harness the gateway is
configured with, whose .harness-repos.yml lists them. A collection works in
one wtc project, with worktrees of the repositories a task needs, or in an
ad-hoc one, which takes any repository.

projects lists them, so that what the person asks to work on can be matched to
a project and its repositories before {{command}} add-repo starts a collection.`),

		Args: cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if h := currentHost(); h != openclaw.HostOpenClaw {
			return fmt.Errorf(branding.Text("{{command}} projects lists an OpenClaw gateway's {{command}} projects, and this is a %s session: a harness's .harness-repos.yml lists its repositories"), h)
		}
		projects, err := gatewayProjects()
		if err != nil {
			return err
		}
		return emit(envelope{OK: true, Data: map[string]any{"projects": projects}, Summary: projectsSummary(projects)}, *asJSON)
	}
	root.AddCommand(cmd)
}

func projectsSummary(projects []gatewayProject) string {
	var b strings.Builder
	if len(projects) == 0 {
		b.WriteString(branding.Text("This gateway configures no {{command}} projects: every collection is ad hoc.\n"))
	}
	for _, p := range projects {
		def := ""
		if p.Default {
			def = " (default)"
		}
		fmt.Fprintf(&b, "%s%s: harness %s @ %s\n", p.Name, def, p.Harness, p.Ref)
		if p.Error != "" {
			fmt.Fprintf(&b, "  %s\n", p.Error)
		}
		for _, r := range p.Repos {
			role := ""
			if r.Role != "" {
				role = " — " + r.Role
			}
			fmt.Fprintf(&b, "  %s  %s%s\n", r.Name, r.Remote, role)
		}
	}
	b.WriteString(branding.Text("Any other repository: ad hoc, {{command}} add-repo <url> --project none, or beside a project's with --ad-hoc.\n"))
	b.WriteString(branding.Text("Start a collection: {{command}} add-repo <repo> [...] --worktree <name>; the project whose repositories these are is picked."))
	return b.String()
}
