package main

import (
	"testing"

	"taba.sh/taba/internal/testenv"
)

// The network guard is the package's fixture: its cleanup fails the run when
// a real forge client was launched (#471).
func TestMain(m *testing.M) { testenv.Main(m, networkGuardFixture) }
