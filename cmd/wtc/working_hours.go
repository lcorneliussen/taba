package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lcorneliussen/wtc-cli/internal/openclaw"
	"github.com/lcorneliussen/wtc-cli/internal/wtc"
	"github.com/spf13/cobra"
)

func localHoursStore(c *wtc.Context) (wtc.WorkingHoursStore, error) {
	if currentHost() == openclaw.HostOpenClaw {
		return wtc.WorkingHoursStore{}, fmt.Errorf("working hours are unconfigured: authenticated-person adapter is not available on this host")
	}
	root, err := wtc.UserConfigRoot()
	if err != nil {
		return wtc.WorkingHoursStore{}, err
	}
	return wtc.NewWorkingHoursStore(root, c.ConfigRoot)
}
func localHoursFacts(c *wtc.Context, now time.Time) (wtc.WorkingHoursFacts, error) {
	if currentHost() == openclaw.HostOpenClaw {
		f, err := wtc.EvaluateWorkingHours(nil, nil, now)
		f.UnavailableReason = "authenticated-person adapter is not available on this host"
		f.Guidance = "Keep working hours unconfigured until an authenticated-person adapter is available; do not use the gateway account's schedule."
		return f, err
	}
	store, err := localHoursStore(c)
	if err != nil {
		return wtc.WorkingHoursFacts{}, err
	}
	return store.Facts(now)
}
func addWorkingHoursCommand(root *cobra.Command, asJSON *bool, cwd func() (*wtc.Context, error)) {
	hours := &cobra.Command{Use: "hours", Short: "Private working-hours setup and fresh schedule facts", Args: cobra.NoArgs}
	facts := &cobra.Command{Use: "facts", Short: "Evaluate current phase and next window without changing state", Args: cobra.NoArgs}
	runFacts := func(cmd *cobra.Command, args []string) error {
		c, err := cwd()
		if err != nil {
			return err
		}
		f, err := localHoursFacts(c, time.Now())
		if err != nil {
			return err
		}
		return emit(envelope{OK: true, Data: f, Summary: f.Summary() + "\n" + f.Guidance}, *asJSON)
	}
	hours.RunE = runFacts
	facts.RunE = runFacts
	hours.AddCommand(facts)
	storeFor := func() (wtc.WorkingHoursStore, error) {
		c, err := cwd()
		if err != nil {
			return wtc.WorkingHoursStore{}, err
		}
		return localHoursStore(c)
	}
	var input string
	var project bool
	setup := &cobra.Command{Use: "setup --file <schedule.json|->", Aliases: []string{"edit"}, Short: "Explicitly replace personal defaults or this project's override", Args: cobra.NoArgs,
		Long: "Set a private schedule from one JSON object with timezone and windows. Each window has days (Sunday=0), start and end (HH:MM). No defaults are guessed. Use --project for a complete project override; edit replaces the selected schedule just like setup. See wtc hours guidance."}
	setup.Flags().StringVar(&input, "file", "", "Schedule JSON file, or - for stdin (required)")
	setup.Flags().BoolVar(&project, "project", false, "Replace this project's override instead of personal defaults")
	setup.RunE = func(cmd *cobra.Command, args []string) error {
		if input == "" {
			return fmt.Errorf("provide --file explicitly; no schedule is active by default")
		}
		store, err := storeFor()
		if err != nil {
			return err
		}
		var reader io.Reader = os.Stdin
		if input != "-" {
			f, err := os.Open(input)
			if err != nil {
				return fmt.Errorf("cannot open schedule input")
			}
			defer f.Close()
			reader = f
		}
		p, err := wtc.DecodeWorkingSchedule(reader)
		if err != nil {
			return err
		}
		if err := store.Setup(p, project); err != nil {
			return err
		}
		return emit(envelope{OK: true, Summary: "Private working hours saved. Collections sharing this project use the same override."}, *asJSON)
	}
	hours.AddCommand(setup)
	view := &cobra.Command{Use: "view", Short: "Show the effective private schedule for this person and project", Args: cobra.NoArgs}
	view.RunE = func(cmd *cobra.Command, args []string) error {
		store, err := storeFor()
		if err != nil {
			return err
		}
		p, source, err := store.View()
		if err != nil {
			return err
		}
		v := map[string]any{"schedule": p, "source": source}
		if *asJSON {
			return emit(envelope{OK: true, Data: v}, true)
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	}
	hours.AddCommand(view)
	var clearProject bool
	clear := &cobra.Command{Use: "clear", Short: "Remove personal defaults, or a project override to inherit defaults", Args: cobra.NoArgs}
	clear.Flags().BoolVar(&clearProject, "project", false, "Remove this project's override")
	clear.RunE = func(cmd *cobra.Command, args []string) error {
		store, err := storeFor()
		if err != nil {
			return err
		}
		if err := store.Clear(clearProject); err != nil {
			return err
		}
		return emit(envelope{OK: true, Summary: "Selected schedule removed; use hours facts to see the effective policy."}, *asJSON)
	}
	hours.AddCommand(clear)
	var duration time.Duration
	extend := &cobra.Command{Use: "extend --for <duration>", Short: "Explicitly continue for a finite period in this project (up to 4h)", Args: cobra.NoArgs}
	extend.Flags().DurationVar(&duration, "for", 0, "Explicit temporary duration, for example 30m (required)")
	extend.RunE = func(cmd *cobra.Command, args []string) error {
		store, err := storeFor()
		if err != nil {
			return err
		}
		if err := store.Extend(time.Now(), duration); err != nil {
			return err
		}
		return runFacts(cmd, args)
	}
	hours.AddCommand(extend)
	end := &cobra.Command{Use: "end-extension", Short: "End this project's temporary extension now", Args: cobra.NoArgs}
	end.RunE = func(cmd *cobra.Command, args []string) error {
		store, err := storeFor()
		if err != nil {
			return err
		}
		if err := store.EndExtension(); err != nil {
			return err
		}
		return runFacts(cmd, args)
	}
	hours.AddCommand(end)
	claim := &cobra.Command{Use: "claim-reminder", Short: "Atomically claim one outside-window nudge across this project's collections", Args: cobra.NoArgs}
	claim.RunE = func(cmd *cobra.Command, args []string) error {
		store, err := storeFor()
		if err != nil {
			return err
		}
		f, err := store.ClaimReminder(time.Now())
		if err != nil {
			return err
		}
		summary := "No reminder claimed."
		if f.ReminderClaimed {
			summary = "Reminder claimed. " + f.Guidance
		}
		return emit(envelope{OK: true, Data: f, Summary: summary}, *asJSON)
	}
	hours.AddCommand(claim)
	guidance := &cobra.Command{Use: "guidance", Short: "Read portable behavior and setup guidance", Args: cobra.NoArgs}
	guidance.RunE = func(cmd *cobra.Command, args []string) error {
		b, err := wtc.ReadDefault("instructions/working-hours.md")
		if err != nil {
			return err
		}
		return emit(envelope{OK: true, Data: string(b), Summary: string(b)}, *asJSON)
	}
	hours.AddCommand(guidance)
	root.AddCommand(hours)
}
