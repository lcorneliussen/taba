package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

func addServiceCommands(svc *cobra.Command, asJSON *bool) {
	report := func(result wtc.RuntimeResult, err error, summary string) error {
		if err != nil && !*asJSON {
			for _, tail := range result.Logs {
				fmt.Fprintf(os.Stderr, "--- %s (last lines of %s)\n%s\n", tail.Path, tail.File, tail.Tail)
			}
		}
		if *asJSON {
			if emitErr := emit(envelope{OK: err == nil, Data: result, Summary: summary}, true); emitErr != nil {
				return emitErr
			}
		}
		if err != nil {
			return err
		}
		if !*asJSON {
			fmt.Print(wtc.RuntimeText(result.Items))
		}
		return nil
	}

	var opt wtc.ServiceOptions
	add := &cobra.Command{Use: "add <service> -- <command>...", Short: "Enlist a service and start it",
		Long: branding.Text(`Enlist a long-running command as a service of a repository and start it.

The service goes into svc/<repo>.yaml in this collection's runtime home, which is
never committed; --shared writes the repository's .harness/dekit.tasks.yaml instead,
for everyone who works on it. The command runs in the repository with the
collection environment. Give the variable holding its port with --port-env:
status then shows its local URL, it counts as ready once the port answers,
and {{command}} svc tunnel can expose it.`),

		Example: branding.Text(`  {{command}} svc add web --port-env PORT -- npm run dev
  {{command}} svc add ext.app/api --port-env API_PORT --shared -- ./bin/dev
  {{command}} svc add worker --ready-log "worker started" -- mise run worker`),

		Args: cobra.MinimumNArgs(2)}
	add.Flags().StringVar(&opt.PortEnv, "port-env", "", "Variable holding the service's local port")
	add.Flags().StringVar(&opt.URLEnv, "url-env", "", "Variable holding the service's local URL")
	add.Flags().StringVar(&opt.ReadyLog, "ready-log", "", "Output line that means the service is ready")
	add.Flags().DurationVar(&opt.ReadyTimeout, "ready-timeout", 60*time.Second, "Time allowed to get ready")
	add.Flags().BoolVar(&opt.NoAutostart, "no-autostart", false, branding.Text("Do not start it with {{command}} up"))
	add.Flags().StringVar(&opt.Cwd, "cwd", "", "Working directory inside the repository")
	add.Flags().StringVar(&opt.Label, "label", "", "Description shown on hover")
	add.Flags().StringVar(&opt.Mark, "mark", "", "One letter for status")
	add.Flags().BoolVar(&opt.Shared, "shared", false, "Write the repository's committed task file")
	add.Flags().BoolVar(&opt.NoStart, "no-start", false, "Enlist without starting")
	add.Flags().DurationVar(&opt.Wait, "wait", 60*time.Second, "Wait for readiness (0 returns immediately)")
	add.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.ArgsLenAtDash() != 1 {
			return errors.New(branding.Text("put the command after --, such as {{command}} svc add web -- npm run dev"))
		}
		c, err := startContext()
		if err != nil {
			return err
		}
		opt.Service, opt.Cmd = args[0], args[1:]
		result, err := c.AddService(opt)
		return report(result, err, "service enlisted")
	}

	var shared bool
	rm := &cobra.Command{Use: "rm <service>", Aliases: []string{"remove"}, Short: "Stop a service with its tunnels and remove it",
		Long: `Stop a service and its tunnels and remove them from their task file. The other
services keep running. A service from the repository's committed task file
needs --shared, since removing it there is a change to the repository.`,
		Args: cobra.ExactArgs(1)}
	rm.Flags().BoolVar(&shared, "shared", false, "Also remove it from the repository's committed task file")
	rm.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := runtimeContext()
		if err != nil {
			return err
		}
		result, err := c.RemoveService(args[0], shared)
		return report(result, err, "service removed")
	}

	var topt wtc.TunnelOptions
	tunnel := &cobra.Command{Use: "tunnel <service> [tailscale|cloudflare|ngrok|devtunnel]", Short: "Open or close a tunnel to a service",
		Long: `Open a tunnel to a service through a provider, or close it with --stop.

A service without a tunnel for the provider gets one in this collection's task
file, built from its --port-env. A tailscale tunnel stays on your tailnet and
a devtunnel admits your account only; a cloudflare quick tunnel, ngrok and an
anonymous devtunnel are on the internet and open only with --public.`,
		Example: branding.Text(`  {{command}} svc tunnel web tailscale
  {{command}} svc tunnel web cloudflare --public
  {{command}} svc tunnel web --stop`),

		Args: cobra.RangeArgs(1, 2)}
	tunnel.Flags().BoolVar(&topt.Public, "public", false, "Allow a tunnel reachable from the internet")
	tunnel.Flags().BoolVar(&topt.Stop, "stop", false, "Close the tunnel")
	tunnel.Flags().DurationVar(&topt.Wait, "wait", 60*time.Second, "Wait until the tunnel answers (0 returns immediately)")
	tunnel.RunE = func(cmd *cobra.Command, args []string) error {
		load := startContext
		if topt.Stop {
			load = runtimeContext
		}
		c, err := load()
		if err != nil {
			return err
		}
		topt.Service, topt.Provider = args[0], ""
		if len(args) == 2 {
			topt.Provider = args[1]
		}
		result, err := c.Tunnel(topt)
		return report(result, err, "tunnel")
	}

	dekit := &cobra.Command{Use: "dekit -- <args>...", Short: "Run this collection's dekit against its runner",
		Long: branding.Text(`Run the collection's pinned dekit with its runner and environment selected, for
what {{command}} svc does not cover: why a task is not running, its live screen,
attaching to it, or a one-off task. Task paths are <repo>/<service>/<task>.
Tasks added here with dekit spawn are not part of the task files and do not
show in {{command}} status.`),

		Example: branding.Text(`  {{command}} svc dekit -- why ext.app/web/main
  {{command}} svc dekit -- screen ext.app/web/main
  {{command}} svc dekit -- ls`),

		DisableFlagParsing: true}
	dekit.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
			return cmd.Help()
		}
		c, err := runtimeContext()
		if err != nil {
			return err
		}
		run, err := c.DekitCommand(args)
		if err != nil {
			return err
		}
		run.Stdin, run.Stdout, run.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := run.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				os.Exit(exit.ExitCode())
			}
			return err
		}
		return nil
	}
	svc.AddCommand(add, rm, tunnel, dekit)
}
