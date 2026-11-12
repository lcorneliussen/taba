package main

import (
	"encoding/json"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"testing"
)

func TestNativePublicationCommandRequiresReceiptAndCollection(t *testing.T) {
	root := &cobra.Command{Use: "openclaw"}
	addOpenClawPublicationCommand(root)
	root.SetArgs([]string{"enlist-publication", "--url", "https://github.com/example/widget/pull/7"})
	if err := root.Execute(); err == nil {
		t.Fatal("accepted incomplete receipt")
	}
}

func TestNativePublicationRuntimeAssetsAreInstalled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin")
	if err := writeOpenClawPlugin(dir, "test"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"publication.js", "dist/control-ui/v1/pr-preview.js", "dist/control-ui/v1/vendor/marked.esm.js", "dist/control-ui/v1/vendor/marked.LICENSE"} {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err != nil || len(data) == 0 {
			t.Fatalf("runtime asset %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "openclaw.plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Contracts struct {
			Dispatch []string `json:"gatewayMethodDispatch"`
		} `json:"contracts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil || len(manifest.Contracts.Dispatch) != 1 || manifest.Contracts.Dispatch[0] != "authenticated-request" {
		t.Fatalf("dispatch contract missing: %+v %v", manifest, err)
	}
}
