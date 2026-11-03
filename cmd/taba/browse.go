package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

type browseHerdrResult struct {
	Result struct {
		Agents []struct {
			PaneID string `json:"pane_id"`
		} `json:"agents"`
	} `json:"result"`
}

func addBrowseCommand(root *cobra.Command, asJSON *bool) {
	var here bool
	cmd := &cobra.Command{Use: "browse [collection]", Short: "Open the collection's Neovim browser in this terminal", Args: cobra.MaximumNArgs(1)}
	cmd.Flags().BoolVar(&here, "here", false, "Open Neovim in this terminal, including an agent pane")
	cmd.Flags().Bool("no-focus", false, "Compatibility flag; browse no longer routes to a herdr pane")
	_ = cmd.Flags().MarkHidden("no-focus")
	cmd.Flags().String("session", "", "Compatibility flag; browse no longer routes to a herdr pane")
	_ = cmd.Flags().MarkHidden("session")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if *asJSON {
			return errors.New("browse is interactive and does not support --json")
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		c, err := wtc.Discover(cwd)
		if err != nil {
			return err
		}
		if len(args) == 1 {
			name := args[0]
			if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
				return fmt.Errorf("expected a collection name")
			}
			if err := c.RequireWorkspace("browse <collection>"); err != nil {
				return err
			}
			c, err = wtc.OpenCollection(filepath.Join(c.Workspace, name))
			if err != nil {
				return err
			}
		}
		if _, err := exec.LookPath("nvim"); err != nil {
			return errors.New("nvim is not on PATH")
		}
		if !here {
			callerAgent, err := browseCallerIsAgent()
			if err != nil {
				return err
			}
			if callerAgent {
				// Neovim here would take over the agent's terminal.
				open := branding.Text("{{command}} open ") + filepath.Base(c.Collection)
				if session := os.Getenv("HERDR_SESSION"); session != "" {
					open += " --session " + session
				}
				return fmt.Errorf("this is an agent pane; run %s --add browse for a browse pane, or pass --here", open)
			}
		}
		return browseHere(c)
	}
	root.AddCommand(cmd)
}

func browseHerdr(session string, args ...string) (browseHerdrResult, error) {
	var result browseHerdrResult
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	argv := append([]string{"--session", session}, args...)
	out, err := exec.CommandContext(ctx, "herdr", argv...).CombinedOutput()
	if err != nil {
		return result, fmt.Errorf("herdr %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	if len(out) == 0 {
		return result, nil
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return result, fmt.Errorf("herdr %s returned invalid JSON: %w", strings.Join(args, " "), err)
	}
	return result, nil
}

func browseCallerIsAgent() (bool, error) {
	callerSession := os.Getenv("HERDR_SESSION")
	if os.Getenv("HERDR_ENV") != "1" || os.Getenv("HERDR_PANE_ID") == "" || callerSession == "" {
		return false, nil
	}
	result, err := browseHerdr(callerSession, "agent", "list")
	if err != nil {
		return false, fmt.Errorf("cannot determine whether this herdr pane belongs to an agent: %w", err)
	}
	for _, agent := range result.Result.Agents {
		if agent.PaneID == os.Getenv("HERDR_PANE_ID") {
			return true, nil
		}
	}
	return false, nil
}

func browseHere(c *wtc.Context) error {
	body, err := browseView(c)
	if err != nil {
		return err
	}
	lua, err := os.CreateTemp("", "wtc-browse-*.lua")
	if err != nil {
		return err
	}
	defer os.Remove(lua.Name())
	if _, err := lua.Write(body); err != nil {
		lua.Close()
		return err
	}
	if err := lua.Close(); err != nil {
		return err
	}
	socket := browseSocket(c.Workspace, filepath.Base(c.Collection))
	argv := []string{}
	probeContext, stopProbe := context.WithTimeout(context.Background(), 3*time.Second)
	probe := exec.CommandContext(probeContext, "nvim", "--server", socket, "--remote-expr", "1")
	probeErr := probe.Run()
	probeTimedOut := probeContext.Err() != nil
	stopProbe()
	if probeTimedOut {
		return fmt.Errorf("browse Neovim at %s did not answer within three seconds", socket)
	}
	if probeErr != nil {
		if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
			return err
		}
		argv = append(argv, "--listen", socket)
	} else {
		fmt.Fprintf(os.Stderr, "==> %s: browse Neovim already listens on %s; opening another window\n", filepath.Base(c.Collection), socket)
	}
	argv = append(argv,
		"-c", "lua vim.g.wtc_browse_root = "+strconv.Quote(c.Collection),
		"-c", "lua dofile("+strconv.Quote(lua.Name())+")")
	process := exec.Command("nvim", argv...)
	process.Dir = c.Collection
	process.Stdin, process.Stdout, process.Stderr = os.Stdin, os.Stdout, os.Stderr
	return process.Run()
}

func browseView(c *wtc.Context) ([]byte, error) {
	override := filepath.Join(c.Harness, "overlays", "browse", "wtc-browse.lua")
	if info, err := os.Stat(override); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("browse overlay is not a regular file: %s", override)
		}
		return os.ReadFile(override)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return wtc.ReadDefault("browse/wtc-browse.lua")
}

// POSIX cksum is also used by the shell compatibility shim and status clicks.
func browseChecksum(data string) uint32 {
	const polynomial uint32 = 0x04c11db7
	crc := uint32(0)
	step := func(value byte) {
		crc ^= uint32(value) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ polynomial
			} else {
				crc <<= 1
			}
		}
	}
	for i := 0; i < len(data); i++ {
		step(data[i])
	}
	for n := len(data); n > 0; n >>= 8 {
		step(byte(n))
	}
	return ^crc
}

func browseSocket(workspace, collection string) string {
	path := "/tmp/wtc-browse-" + filepath.Base(workspace) + "-" + collection + ".nvim"
	if len(path) <= 100 {
		return path
	}
	stem := filepath.Base(workspace) + "-" + collection
	if len(stem) > 60 {
		stem = stem[:60]
	}
	return fmt.Sprintf("/tmp/wtc-browse-%s-%d.nvim", stem, browseChecksum(workspace+"/"+collection))
}
