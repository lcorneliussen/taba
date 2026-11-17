//go:build !darwin && !linux

package wtc

import "os"

func statusResourceLeaseEvents(*os.File) (<-chan struct{}, func()) {
	return nil, func() {}
}
