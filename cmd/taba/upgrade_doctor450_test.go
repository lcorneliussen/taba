package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func TestPrintUpgradeDoctorReportsAdvice450(t *testing.T) {
	capture := func(d *wtc.UpgradeDoctor) string {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout := os.Stdout
		os.Stdout = w
		printUpgradeDoctor(d)
		os.Stdout = stdout
		w.Close()
		out, _ := io.ReadAll(r)
		return string(out)
	}
	if out := capture(nil); out != "" {
		t.Fatalf("nil doctor printed %q", out)
	}
	out := capture(&wtc.UpgradeDoctor{Status: "warnings", Version: "0.2.9", Reason: "suggestions only",
		Findings: []wtc.UpgradeDoctorFinding{{Name: "named-tunnels", Detail: "declare x-wtc.port_env on widget/web"}}, Truncated: 3})
	for _, want := range []string{"diagnostics (advisory) with wtc 0.2.9 doctor: warnings — suggestions only", "named-tunnels", "x-wtc.port_env", "3 more"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if out := capture(&wtc.UpgradeDoctor{Status: "unavailable", Version: "0.2.9", Reason: "wtc 0.2.9 is not installed for this collection; diagnostics did not run"}); !strings.Contains(out, "unavailable — wtc 0.2.9 is not installed") {
		t.Fatalf("unavailable not explicit:\n%s", out)
	}
}
