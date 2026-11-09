package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

func runtimeContext() (*wtc.Context, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return wtc.Discover(cwd)
}

// startContext is runtimeContext for what starts services or tunnels, or
// provisions their resources (add, up, restart, provision, opening a
// tunnel). A worker's services are its task's: the worker merges into the
// task, whose collection runs them. Stopping, listing and logs still work in
// a worker's collection, for what ran there before.
func startContext() (*wtc.Context, error) {
	c, err := runtimeContext()
	if err != nil {
		return nil, err
	}
	if err := refuseWorkerServices(c.Collection, os.Getenv); err != nil {
		return nil, err
	}
	return c, nil
}

// refuseWorkerServices refuses a worker's collection under OpenClaw: one
// named <task>--<worker> beside its task's collection.
func refuseWorkerServices(collection string, getenv func(string) string) error {
	if openclaw.DetectHost(getenv) != openclaw.HostOpenClaw {
		return nil
	}
	name := filepath.Base(collection)
	task := openclaw.TaskOf(name)
	if task == "" {
		return nil
	}
	if info, err := os.Stat(filepath.Join(filepath.Dir(collection), task)); err != nil || !info.IsDir() {
		return nil
	}
	return fmt.Errorf("%s is a worker's collection: services run in its task's collection, %s", name, task)
}

func addRuntimeCommands(root *cobra.Command, asJSON *bool) {
	runtime := &cobra.Command{Use: "svc", Aliases: []string{"services", "runtime"}, Short: "List, enlist, start and stop this collection's services and tunnels",
		Long: branding.Text(`Services are a repository's long-running processes, such as a web server, with
their tunnels and resources, run by this collection's dekit runner.

A repository shares services in its committed .harness/dekit.tasks.yaml. {{command}} svc
add and {{command}} svc tunnel write svc/<repo>.yaml in this collection's runtime home unless
--shared is given; that file is never committed and goes with the collection.

  {{command}} svc                          list services, tunnels and resources
  {{command}} svc add web --port-env PORT -- npm run dev
  {{command}} svc up|down|restart [web]    all, or one service
  {{command}} svc logs web                 recent output; -f follows
  {{command}} svc tunnel web tailscale     open a tunnel; --public for one on the internet
  {{command}} svc tunnel web --stop        close it
  {{command}} svc rm web                   stop and remove
  {{command}} svc dekit -- why web/main    anything else, with dekit itself

A name without its repository works when only one repository has it.`),
	}
	runtime.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := runtimeContext()
		if err != nil {
			return err
		}
		items := c.RuntimeStatus()
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(items)
		}
		if len(items) == 0 {
			fmt.Println(branding.Text("No services. Enlist one with {{command}} svc add <name> -- <command>; {{command}} svc --help explains."))
			return nil
		}
		fmt.Print(wtc.RuntimeText(items))
		return nil
	}
	var dryRun bool
	render := &cobra.Command{Use: "render", Short: "Render repository dekit tasks without starting services", Args: cobra.NoArgs}
	render.Flags().BoolVar(&dryRun, "dry-run", false, "Preview task ownership without writing or requiring dekit")
	render.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := runtimeContext()
		if err != nil {
			return err
		}
		result, err := c.RenderRuntime(dryRun)
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: result, Summary: fmt.Sprintf("runtime: %d task(s)", len(result.Items))}, true)
		}
		fmt.Print(wtc.RuntimeText(result.Items))
		return nil
	}
	status := &cobra.Command{Use: "status", Aliases: []string{"ls", "list"}, Short: "List services and their state without starting them", Args: cobra.NoArgs}
	status.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := runtimeContext()
		if err != nil {
			return err
		}
		items := c.RuntimeStatus()
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(items)
		}
		fmt.Print(wtc.RuntimeText(items))
		return nil
	}
	teardown := &cobra.Command{Use: "teardown", Short: "Stop the runtime and strictly tear down collection-owned resources", Args: cobra.NoArgs}
	teardown.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := runtimeContext()
		if err != nil {
			return err
		}
		if err := c.RetireRuntime(); err != nil {
			return err
		}
		return emit(envelope{OK: true, Summary: "runtime stopped and resource teardown completed"}, *asJSON)
	}
	provision := &cobra.Command{Use: "provision [repo/task-or-group]", Short: "Run idempotent resource hooks without starting services", Args: cobra.MaximumNArgs(1)}
	provision.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := startContext()
		if err != nil {
			return err
		}
		target := ""
		if len(args) > 0 {
			target = args[0]
		}
		result, err := c.RuntimeAction("provision", target, 0)
		if err != nil {
			return err
		}
		return emit(envelope{OK: true, Data: result, Summary: "runtime resources provisioned"}, *asJSON)
	}
	runtime.AddCommand(render, status, provision, teardown)
	addServiceCommands(runtime, asJSON)
	root.AddCommand(runtime)
	for _, action := range []string{"up", "down", "restart"} {
		runtime.AddCommand(runtimeActionCommand(action, asJSON))
		root.AddCommand(runtimeActionCommand(action, asJSON))
	}
	runtime.AddCommand(runtimeLogsCommand(asJSON))
	root.AddCommand(runtimeLogsCommand(asJSON))
}

var runtimeActionShort = map[string]string{
	"up":      "Start this collection's services (all, or one)",
	"down":    "Stop this collection's services (all, or one)",
	"restart": "Restart this collection's services (all, or one)",
}

func runtimeActionCommand(action string, asJSON *bool) *cobra.Command {
	var wait time.Duration
	cmd := &cobra.Command{Use: action + " [service]", Short: runtimeActionShort[action], Args: cobra.MaximumNArgs(1)}
	if action != "down" {
		cmd.Flags().DurationVar(&wait, "wait", 30*time.Second, "Wait for selected startup readiness (0 returns immediately)")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if wait < 0 {
			return fmt.Errorf("--wait must be nonnegative")
		}
		load := startContext
		if action == "down" {
			load = runtimeContext
		}
		c, err := load()
		if err != nil {
			return err
		}
		target := ""
		if len(args) != 0 {
			target = args[0]
		}
		result, err := c.RuntimeAction(action, target, wait)
		if err != nil && !*asJSON {
			// The task's own output is usually the actual error.
			for _, tail := range result.Logs {
				fmt.Fprintf(os.Stderr, "--- %s (last lines of %s)\n%s\n", tail.Path, tail.File, tail.Tail)
			}
		}
		if *asJSON {
			if emitErr := emit(envelope{OK: err == nil, Data: result, Summary: action + " runtime"}, true); emitErr != nil {
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
	return cmd
}

func runtimeLogsCommand(asJSON *bool) *cobra.Command {
	var follow bool
	var tail int
	logs := &cobra.Command{Use: "logs <service>", Short: "Read a service's or tunnel's output without starting it", Args: cobra.ExactArgs(1)}
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "Follow appended log output")
	logs.Flags().IntVar(&tail, "tail", 100, "Number of recent lines")
	logs.RunE = func(cmd *cobra.Command, args []string) error {
		if *asJSON {
			return errors.New(branding.Text("logs emits plain text; use {{command}} svc --json for state"))
		}
		if tail < 0 {
			return fmt.Errorf("--tail must be nonnegative")
		}
		c, err := runtimeContext()
		if err != nil {
			return err
		}
		path, err := c.RuntimeLogPath(args[0])
		if err != nil {
			return err
		}
		argv := []string{"-n", fmt.Sprint(tail)}
		if follow {
			argv = append(argv, "-f")
		}
		argv = append(argv, path)
		reader := exec.CommandContext(cmd.Context(), "tail", argv...)
		reader.Stdout, reader.Stderr = os.Stdout, os.Stderr
		return reader.Run()
	}
	return logs
}
