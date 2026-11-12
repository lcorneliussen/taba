// Export synthetic input plus the current planner's actual output. This is a
// development fixture tool, not a command installed with wtc.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"taba.sh/taba/internal/next"
	"taba.sh/taba/internal/next/nexttest"
)

func main() {
	filter := flag.String("filter", "", "Only case IDs containing this text")
	summary := flag.Bool("summary", false, "Print counts by current recommendation instead of JSONL cases")
	flag.Parse()
	counts := map[string]int{}
	enc := json.NewEncoder(os.Stdout)
	for _, c := range nexttest.Cases() {
		if !strings.Contains(c.ID, *filter) {
			continue
		}
		choices := next.ChoicesFor(c.Facts)
		step := choices.Primary()
		name := "none"
		if step != nil {
			name = step.Step
		}
		counts[name]++
		if !*summary {
			if err := enc.Encode(struct {
				nexttest.Case
				Current *next.Step   `json:"current"`
				Choices next.Choices `json:"choices"`
			}{c, step, choices}); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}
	if *summary {
		if err := enc.Encode(counts); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
