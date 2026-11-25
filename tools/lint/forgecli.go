//go:build ruleguard

package lint

import "github.com/quasilyte/go-ruleguard/dsl"

// forgeChild rejects a forge CLI started through os/exec. An exec.Cmd offers
// Output, CombinedOutput, StdoutPipe and Run with any Stdout, and four of
// those are wrong for a body that gets parsed: a Node-based forge CLI writes
// a --json body in one call and exits, so on a pipe only the first 64KiB
// arrives (#126, #125), and CombinedOutput puts stderr in the body. internal/
// forgecli has one read path, a regular file, and keeps stderr separate.
//
// The rule matches only a literal program name. A runner that takes its
// program as a parameter is not flagged, so it builds its command with
// forgecli, which has no stdout pipe to read.
// tools/lint/testdata/forgecli holds the lines this rule has to keep
// catching, with where each came from.
func forgeChild(m dsl.Matcher) {
	m.Match(`exec.Command($name, $*_)`, `exec.CommandContext($_, $name, $*_)`).
		Where(m["name"].Node.Is(`BasicLit`) && m["name"].Text.Matches("^[\"`](gh|bb|twg)[\"`]$")).
		At(m["name"]).
		Report(`start a forge CLI with forgecli.Command or forgecli.CommandContext: Output reads the body from a regular file, Run is for a call whose output nothing parses`)
}
