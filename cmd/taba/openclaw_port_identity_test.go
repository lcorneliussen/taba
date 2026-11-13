package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

func TestGatewayPortOriginSupportedSCP(t *testing.T) {
	for _, host := range []string{"github.com", "bitbucket.org"} {
		for _, user := range []string{"", "git@", "deploy-user@"} {
			for _, suffix := range []string{"", ".git"} {
				remote := user + host + ":example-org/web" + suffix
				t.Run(remote, func(t *testing.T) {
					want := openclaw.ParseOrigin(remote)
					got, ok := gatewayPortOrigin(remote)
					if !ok || got != want || got.Owner != "example-org" || got.Repo != "web" {
						t.Fatalf("origin = %+v, %t; want %+v", got, ok, want)
					}
					if !gatewayPortSameRemote(remote, "https://"+host+"/EXAMPLE-ORG/WEB.git") {
						t.Fatal("SCP and HTTPS identities differ")
					}
				})
			}
		}
	}
}

func TestGatewayPortSCPRejectsIncompleteOrDifferentIdentity(t *testing.T) {
	for _, remote := range []string{
		"github.com:web", "github.com:example-org/web/extra", "github.com:example-org//web",
		"github.com:../web", "github.com:example-org/..", "github.com:example-org/.",
		"github.com:example-org/web?token=example", "github.com:example-org/web#fragment",
		"github.com:example-org/%77eb", "github.com:example-org/web%2fextra",
		"github.com:example-org/web%ZZ", "github.com:example-org/web\\extra",
		"unknown.invalid:example-org/web", "github.com.unknown.invalid:example-org/web",
		"github.com@unknown.invalid:example-org/web", "example-org/web", "/tmp/web",
	} {
		if _, ok := gatewayPortOrigin(remote); ok {
			t.Errorf("invalid remote accepted: %q", remote)
		}
	}
	for _, remote := range []string{
		"github.com:another-org/web", "github.com:example-org/another-repo",
		"bitbucket.org:example-org/web", "github.com@bitbucket.org:example-org/web",
	} {
		if gatewayPortSameRemote(remote, "https://github.com/example-org/web.git") {
			t.Errorf("different repository matched: %q", remote)
		}
	}
}

func TestGatewayProjectPortsSupportedSCPOnStatus(t *testing.T) {
	for _, host := range []string{"github.com", "bitbucket.org"} {
		for _, user := range []string{"", "git@", "deploy-user@"} {
			for _, location := range []string{"configured-project", "recorded-project", "project-registry", "linked-repository"} {
				t.Run(host+"/"+user+"/"+location, func(t *testing.T) {
					state, git := harnessGateway(t)
					configRoot := t.TempDir()
					t.Setenv("WTC_CONFIG_ROOT", configRoot)
					t.Setenv("TABA_CONFIG_ROOT", configRoot)
					project := "https://" + host + "/example-org/platform-harness.git"
					web := "https://" + host + "/example-org/web.git"
					configured, recorded, registryRemote, linkedRemote := project, project, web, web
					scp := func(repo string) string { return user + host + ":example-org/" + repo + ".git" }
					switch location {
					case "configured-project":
						configured = scp("platform-harness")
					case "recorded-project":
						recorded = scp("platform-harness")
					case "project-registry":
						registryRemote = scp("web")
					case "linked-repository":
						linkedRemote = scp("web")
					}
					withGatewayConfig(t, fmt.Sprintf(`{"harnesses":[{"name":"platform","remote":%q,"ref":"main"}]}`, configured))
					harness := filepath.Join(state, "repos", "platform-harness")
					git(harness, "remote", "set-url", "origin", project)
					git(filepath.Join(state, "repos", "web"), "remote", "set-url", "origin", linkedRemote)
					registry := "repos:\n  - name: docs\n    aliases: [old-docs]\n    remote: " + registryRemote + "\n    port_offset: 1\n"
					if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte(registry), 0o644); err != nil {
						t.Fatal(err)
					}
					git(harness, "add", ".")
					git(harness, "commit", "-qm", "synthetic SCP port registry")
					git(harness, "update-ref", "refs/remotes/origin/main", "HEAD")
					folder := openclaw.NewCollectionFolder(state, "ports", "platform")
					if err := wtc.RecordGatewayHarness(folder, "platform", recorded); err != nil {
						t.Fatal(err)
					}
					rows := []openclaw.CheckoutStatus{{Repo: "web", Path: filepath.Join(state, "repos", "web")}, {Repo: "tools", Path: filepath.Join(state, "repos", "tools")}}
					for range 2 {
						if _, _, warning := linkGatewayFolder("ports", rows); warning != "" {
							t.Fatalf("supported SCP project unavailable: %s", warning)
						}
						data, err := os.ReadFile(filepath.Join(folder, ".env.collection"))
						if err != nil {
							t.Fatal(err)
						}
						for _, name := range []string{"WEB", "DOCS", "OLD_DOCS"} {
							if !strings.Contains(string(data), name+"_PORT=42001\n") {
								t.Fatalf("missing inherited %s port: %s", name, data)
							}
						}
						if strings.Contains(string(data), "TOOLS_PORT=") {
							t.Fatalf("unrelated repository inherited a port: %s", data)
						}
					}
				})
			}
		}
	}
}
