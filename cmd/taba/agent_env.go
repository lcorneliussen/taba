package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

func addAgentEnvCommand(root *cobra.Command, asJSON *bool) {
	var collection string
	var write, printPath, wrap, eval bool
	cmd := &cobra.Command{Use: "agent-env", Short: "Apply sibling toolchain bins to agent shells", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&collection, "collection", "", "Collection directory (default: current)")
	cmd.Flags().BoolVar(&write, "write", false, "Refresh the machine-local cache without printing")
	cmd.Flags().BoolVar(&printPath, "print-path", false, "Print only WTC_TOOLCHAIN_PATH")
	cmd.Flags().BoolVar(&wrap, "wrap", false, "Wrap a PreToolUse event from stdin")
	cmd.Flags().BoolVar(&eval, "eval", false, "Print shell exports (default)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		modes := 0
		for _, flag := range []bool{write, printPath, wrap, eval} {
			if flag {
				modes++
			}
		}
		if modes > 1 {
			return fmt.Errorf("choose one agent-env output mode")
		}
		var c *wtc.Context
		var err error
		if collection == "" {
			var cwd string
			cwd, err = os.Getwd()
			if err == nil {
				c, err = wtc.Discover(cwd)
			}
		} else {
			c, err = wtc.OpenCollection(collection)
		}
		if err != nil {
			return err
		}
		path, err := c.AgentToolchainPath(write)
		if err != nil {
			return err
		}
		if wrap {
			raw, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			if output := wtc.AgentEnvWrap(raw, path); len(output) > 0 {
				fmt.Println(string(output))
			}
			return nil
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: map[string]string{"path": path, "collection": c.Collection}}, true)
		}
		if write {
			return nil
		}
		if printPath {
			if path != "" {
				fmt.Println(path)
			}
			return nil
		}
		fmt.Print(wtc.AgentEnvEval(path))
		return nil
	}
	root.AddCommand(cmd)
}
