package wtc

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

func (c *Context) statusRuntime(snapshot *StatusSnapshot, all bool) {
	if c.statusSweep {
		return
	}
	if !all {
		snapshot.Runtime = c.RuntimeStatus()
	} else {
		// A sweep uses this collection's trusted executable, never a binary
		// selected by a foreign collection's manifest or hook.
		binary, binaryErr := c.runtimeBinary()
		dirs, err := WorkspaceCollections(c.Workspace)
		if err != nil {
			return
		}
		for _, dir := range dirs {
			target, err := OpenCollection(dir)
			if err != nil {
				continue
			}
			add := func(items []RuntimeItem) {
				snapshot.Runtime = append(snapshot.Runtime, target.runtimeWithDeclarations(items)...)
			}
			m, err := target.runtimeManifest()
			if err != nil {
				add([]RuntimeItem{{Collection: filepath.Base(dir), Path: "runtime", State: "unknown", Error: err.Error()}})
				continue
			}
			if m == nil {
				add(nil)
				continue
			}
			if binaryErr != nil {
				var items []RuntimeItem
				for _, task := range m.Tasks {
					item := task.RuntimeItem
					item.State = "unknown"
					item.Error = binaryErr.Error()
					items = append(items, item)
				}
				// Resource hook facts come from the manifest, not the runner.
				add(append(items, target.runtimeResourceItems(m, false)...))
				continue
			}
			m.Binary = binary
			add(runtimeTaskDiagnostics(target.runtimeStatus(m)))
		}
	}
	for i := range snapshot.Repos {
		row := &snapshot.Repos[i]
		counts := map[string]int{}
		for _, item := range snapshot.Runtime {
			if item.Repo == row.Repo && item.Collection == row.Collection {
				state := item.State
				if item.Lifecycle != "" && state != "unknown" {
					state = "tunnel missing"
					if item.TunnelIntent == "stale" {
						state = "choice stale"
					}
				}
				if item.Kind == "resources" && state == "provision-hook-completed" {
					state = "provisioned"
				}
				counts[state]++
			}
		}
		states := []string{}
		for state := range counts {
			states = append(states, state)
		}
		// Keep service state visible in a narrow repo column. The detailed
		// list retains the exact resource-hook fact rather than implying health.
		sort.Slice(states, func(i, j int) bool {
			rank := func(state string) int {
				switch state {
				case "ready":
					return 1
				case "provisioned":
					return 2
				default:
					return 0
				}
			}
			if rank(states[i]) != rank(states[j]) {
				return rank(states[i]) < rank(states[j])
			}
			return states[i] < states[j]
		})
		parts := []string{}
		for _, state := range states {
			parts = append(parts, fmt.Sprintf("%d %s", counts[state], state))
		}
		row.Runtime = strings.Join(parts, ", ")
	}
}

func RuntimeText(items []RuntimeItem) string {
	if len(items) == 0 {
		return ""
	}
	items = append([]RuntimeItem(nil), items...)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Collection+"/"+items[i].Group+"/"+items[i].Path < items[j].Collection+"/"+items[j].Group+"/"+items[j].Path
	})
	var b strings.Builder
	b.WriteString("Services (dekit; ready = startup probe passed, then looked at now)\n")
	group := ""
	for _, item := range items {
		key := item.Collection + "/" + item.Group
		if key != group {
			fmt.Fprintf(&b, "  %s\n", runtimeTextSafe(key))
			group = key
		}
		name := strings.TrimPrefix(item.Path, item.Group+"/")
		if name == item.Path {
			name = filepath.Base(item.Path)
		}
		fmt.Fprintf(&b, "    %-12s %-10s %s", runtimeTextSafe(name), runtimeTextSafe(item.Kind), runtimeTextSafe(item.State))
		if item.Label != "" {
			fmt.Fprintf(&b, " · %s", runtimeTextSafe(item.Label))
		}
		if item.URL != "" {
			fmt.Fprintf(&b, " · %s", runtimeTextSafe(item.URL))
		}
		if item.Kind == "endpoint" {
			exposure := item.Exposure
			if exposure == "" {
				exposure = ExposureUnknown
			}
			fmt.Fprintf(&b, " · exposure: %s", runtimeTextSafe(exposure))
		}
		if item.ExitCode != nil {
			fmt.Fprintf(&b, " · exit %d", *item.ExitCode)
		}
		if item.Signal != nil {
			fmt.Fprintf(&b, " · signal %d", *item.Signal)
		}
		if item.Reason != "" {
			fmt.Fprintf(&b, " · %s", runtimeTextSafe(item.Reason))
		}
		if item.Error != "" {
			fmt.Fprintf(&b, " · %s", runtimeTextSafe(item.Error))
		}
		if item.Answering == "no" {
			fmt.Fprintf(&b, " · not answering now: %s", runtimeTextSafe(item.AnswerDetail))
		}
		if item.Lifecycle != "" {
			fmt.Fprintf(&b, " · %s", runtimeTextSafe(item.Lifecycle))
		}
		if item.Login != "" {
			fmt.Fprintf(&b, " · %s", runtimeTextSafe(item.Login))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// runtimeTextSafe keeps a repo's labels and dekit's messages from writing
// escape sequences or control characters to the terminal.
func runtimeTextSafe(value string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return ' '
		}
		return r
	}, value)
}
