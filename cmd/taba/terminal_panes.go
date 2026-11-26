package main

import (
	"context"
	"errors"
	"path/filepath"

	"taba.sh/taba/internal/wtc"
)

// catchUpPanes is nil for herdr, which catch-up reaches itself.
func catchUpPanes(c *wtc.Context) (wtc.CatchUpPanes, error) {
	kind, err := openMultiplexer(c)
	if err != nil || kind == "herdr" {
		return nil, err
	}
	return rexCatchUpPanes{session: openSession(c, "")}, nil
}

// collectionAgentBusy checks the collection's own agent in whichever
// multiplexer holds it, without catch-up's initiating-agent exemption.
func collectionAgentBusy(c *wtc.Context) (string, error) {
	panes, err := catchUpPanes(c)
	if err != nil {
		return "", err
	}
	if panes == nil {
		return c.AgentBusy()
	}
	return panes.AgentBusy(filepath.Base(c.Collection))
}

func statusProcesses(c *wtc.Context) ([]wtc.StatusProcess, error) {
	kind, err := openMultiplexer(c)
	if err != nil {
		return nil, err
	}
	if kind == "herdr" {
		return c.StatusProcesses()
	}
	shells, err := (&rexHost{session: openSession(c, "")}).paneShells()
	if err != nil {
		return nil, err
	}
	return c.StatusProcessesIn(shells)
}

// paneShells are the processes Rex started in the collections' panes; none
// without a server.
func (h *rexHost) paneShells() (map[int]bool, error) {
	shells := map[int]bool{}
	workspaces, err := h.Workspaces()
	if rexUnreachable(err) {
		return shells, nil
	}
	if err != nil {
		return nil, err
	}
	for _, workspace := range workspaces {
		err := h.eachLiveBlock(workspace.ID, func(_ rexView, _ rexWindow, block rexBlock) (bool, error) {
			process, err := h.process(block.ID)
			if err == nil && process.Child != nil {
				shells[process.Child.PID] = true
			}
			return false, err
		})
		if err != nil {
			return nil, err
		}
	}
	return shells, nil
}

func (h *rexHost) workspace(collection string) (string, error) {
	workspaces, err := h.Workspaces()
	if err != nil {
		return "", err
	}
	for _, workspace := range workspaces {
		if workspace.Label == collection {
			return workspace.ID, nil
		}
	}
	return "", nil
}

type rexCatchUpPanes struct{ session string }

func (p rexCatchUpPanes) host(ctx context.Context) *rexHost {
	return &rexHost{session: p.session, ctx: ctx}
}

// AgentBusy leaves an agent that reports no state to its owner: only the
// owner can tell whether it is between turns.
func (p rexCatchUpPanes) AgentBusy(collection string) (string, error) {
	h := p.host(context.Background())
	if h.Available() != nil {
		return "", nil
	}
	workspace, err := h.workspace(collection)
	if rexUnreachable(err) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("rex session lookup failed")
	}
	if workspace == "" {
		return "", nil
	}
	busy := ""
	err = h.eachLiveBlock(workspace, func(_ rexView, _ rexWindow, block rexBlock) (bool, error) {
		kind, state, err := h.agentState(block.ID)
		if kind != "" && (state == "working" || state == "blocked" || state == "unknown") {
			busy = state
		}
		return busy != "", err
	})
	if err != nil {
		return "", errors.New("rex agent lookup failed")
	}
	return busy, nil
}

func (p rexCatchUpPanes) StatusPane(ctx context.Context, collection string) (string, bool, error) {
	h := p.host(ctx)
	workspace, err := h.workspace(collection)
	if err != nil {
		return "", false, errors.New("rex session lookup failed; no pane control attempted")
	}
	if workspace == "" {
		return "", false, nil
	}
	pane, agent := "", false
	err = h.eachLiveBlock(workspace, func(_ rexView, _ rexWindow, block rexBlock) (bool, error) {
		if block.Label != "status" {
			return false, nil
		}
		process, err := h.process(block.ID)
		if err != nil {
			return false, err
		}
		pane, agent = block.ID, rexAgentKind(process.Foreground) != ""
		return true, nil
	})
	if err != nil {
		return "", false, errors.New("rex pane lookup failed; no pane control attempted")
	}
	return pane, agent, nil
}

func (p rexCatchUpPanes) Foreground(ctx context.Context, pane string) ([]string, error) {
	return p.host(ctx).Foreground(pane)
}

func (p rexCatchUpPanes) Interrupt(ctx context.Context, pane string) error {
	_, err := p.host(ctx).rex("send-key", "-b", pane, "ctrl+c")
	return err
}

func (p rexCatchUpPanes) Run(ctx context.Context, pane, command string) error {
	return p.host(ctx).Run(pane, command)
}
