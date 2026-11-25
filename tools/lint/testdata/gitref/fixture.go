// Package fixture holds ref-building lines taken from the parents of past
// fixes. tools/lint/check-gitref.sh asserts that the gitref rule flags exactly
// the lines marked "want gitref", so the rule keeps catching them.
package fixture

import (
	"fmt"
	"strings"
)

func refs(ref, branch, target string) []string {
	return []string{
		strings.TrimPrefix(ref, "origin/"),            // want gitref: f5467d2^ catch_up.go
		"origin/" + target,                            // want gitref: 70832a1^ adhoc.go
		"refs/heads/" + target,                        // want gitref: 70832a1^ adhoc.go
		fmt.Sprintf("refs/remotes/origin/%s", branch), // want gitref
		"HEAD~1",
		branch,
	}
}
