package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
	openclawplugin "taba.sh/taba/openclaw-plugin"
)

// The setting that lets the gateway load a plugin's Control UI.
const customPluginsKey = "gateway.controlUi.experimental.customPlugins"

func addOpenClawCommands(root *cobra.Command) {
	oc := &cobra.Command{Use: "openclaw", Short: "Set up an OpenClaw gateway for wtc", Args: cobra.NoArgs}
	var dir string
	var noConfig bool
	install := &cobra.Command{
		Use:   "install",
		Short: branding.Text("Install this wtc's OpenClaw plugin on the gateway: the collection card, the Collection panel and the agent's {{command}} skills"),
		Long: `Run on the gateway host, where wtc and openclaw are on the PATH. The plugin is
embedded in this binary, so it is always the one that matches this wtc.

install turns on custom Control UI plugins (` + customPluginsKey + `,
skip with --no-config) and runs "openclaw plugins install <plugin> --force
--accept-capabilities", which also replaces an older wtc plugin, then
"openclaw plugins reload wtc" so that the running gateway uses it. Running
it is consent to the capabilities the plugin declares: a Control UI panel
and session actions. That is also how to upgrade: install the new wtc, then
run this again. With --dir it only writes the plugin there, for a gateway on
another host or in an image build.`,
		Args: cobra.NoArgs,
	}
	install.Flags().StringVar(&dir, "dir", "", "Write the plugin into this directory and install nothing")
	install.Flags().BoolVar(&noConfig, "no-config", false, "Leave "+customPluginsKey+" as it is")
	install.RunE = func(cmd *cobra.Command, args []string) error {
		if dir != "" {
			if err := writeOpenClawPlugin(dir, version); err != nil {
				return err
			}
			fmt.Printf("wrote the wtc %s OpenClaw plugin to %s\ninstall it on the gateway with: openclaw plugins install %s --force --accept-capabilities\n", versionLabel(), dir, dir)
			return nil
		}
		bin, err := exec.LookPath("openclaw")
		if err != nil {
			return errors.New("openclaw is not on the PATH: run this on the gateway host, or write the plugin with --dir and install it there")
		}
		tmp, err := os.MkdirTemp("", "wtc-openclaw-plugin-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		target := filepath.Join(tmp, "wtc")
		if err := writeOpenClawPlugin(target, version); err != nil {
			return err
		}
		run := func(args ...string) error {
			c := exec.Command(bin, args...)
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return fmt.Errorf("openclaw %s: %w", strings.Join(args, " "), err)
			}
			return nil
		}
		if !noConfig {
			if err := run("config", "set", customPluginsKey, "true"); err != nil {
				return err
			}
		}
		if err := run("plugins", "install", target, "--force", "--accept-capabilities"); err != nil {
			return err
		}
		// The running gateway keeps the old plugin until it reloads it. A
		// gateway that is not running loads the new one when it starts.
		if err := run("plugins", "reload", "wtc"); err != nil {
			fmt.Printf("installed the wtc %s OpenClaw plugin, but the gateway did not reload it (%v): restart the gateway\n", versionLabel(), err)
		} else {
			fmt.Printf("installed and reloaded the wtc %s OpenClaw plugin. Reload open Control UI tabs to get the new card\n", versionLabel())
		}
		fmt.Println("see https://github.com/lcorneliussen/taba/blob/main/docs/openclaw.md#install for what each person connects")
		return nil
	}
	oc.AddCommand(install)
	addSandboxGitCommands(oc)
	root.AddCommand(oc)
}

var manifestVersion = regexp.MustCompile(`("version":\s*)"[^"]*"`)

// writeOpenClawPlugin writes the embedded plugin into dir, stamped with this
// binary's version so that the gateway lists which wtc it came from. A build
// without a release version stamps what it describes itself as (a -dev
// version, which the plugin does not compare with anything), or 0.0.0-dev.
func writeOpenClawPlugin(dir, version string) error {
	stamp := strings.TrimPrefix(version, "v")
	if stamp == "dev" {
		stamp = describeBuild(debug.ReadBuildInfo)
	}
	if stamp == "dev" || stamp == "" {
		stamp = "0.0.0-dev"
	}
	err := fs.WalkDir(openclawplugin.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := openclawplugin.Files.ReadFile(path)
		if err != nil {
			return err
		}
		if path == "package.json" || path == "openclaw.plugin.json" {
			if at := manifestVersion.FindSubmatchIndex(data); at != nil {
				data = append(append(append([]byte{}, data[:at[3]]...), `"`+stamp+`"`...), data[at[1]:]...)
			}
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return err
	}
	return writeOpenClawSkills(filepath.Join(dir, "skills"))
}

// writeOpenClawSkills adds the wtc skills that have an OpenClaw variant,
// skills/<name>/SKILL.openclaw.md among the defaults, to the plugin's skills
// as <name>/SKILL.md: the gateway's agents get the procedures for their host,
// from the same binary.
func writeOpenClawSkills(dir string) error {
	paths, err := wtc.DefaultPaths()
	if err != nil {
		return err
	}
	for _, path := range paths {
		parts := strings.Split(path, "/")
		if len(parts) != 3 || parts[0] != "skills" || parts[2] != "SKILL.openclaw.md" {
			continue
		}
		data, err := wtc.ReadDefault(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, parts[1]), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, parts[1], "SKILL.md"), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
