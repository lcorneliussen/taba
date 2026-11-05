//go:build !unix

package wtc

import (
	"context"
	"fmt"
	"os"
)

func ownedByCurrentUser(os.FileInfo) bool { return false }

func lockDevFile(context.Context, string) (func(), error) {
	return nil, fmt.Errorf("dev follow mode is supported on macOS and Linux")
}

// Following cannot publish on this platform; retain ordinary retirement.
func lockDevRetirement(string) (func(), error) { return func() {}, nil }
