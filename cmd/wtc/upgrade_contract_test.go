package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The upgrade contract between a released 0.1.x binary and a taba release
// (#202). A 0.1.x `wtc upgrade --version X`:
//
//  1. checks `gh api repos/lcorneliussen/wtc-cli/releases/tags/vX`;
//  2. pins `github:lcorneliussen/wtc-cli` = X, and mise picks the release
//     asset for this OS and architecture and puts the archive root on PATH;
//  3. runs `mise exec github:lcorneliussen/wtc-cli@X -- wtc release-notes
//     --since <from> --json` and decodes `{"data": [...]}` from stdout.
//
// Steps 1 and 2 reach GitHub through the old repository name, which works
// only through GitHub's rename redirect and stays unproven until a release is
// published. Check it then with `gh api repos/<owner>/<old-repo>/releases/latest`
// and `mise install github:<owner>/<old-repo>@<version>`, which must put both
// `wtc` and `taba` on PATH. These tests pin what the
// release itself must provide: an archive per OS and architecture whose root
// holds `wtc` and `taba`, and a `wtc release-notes --json` that answers as
// 0.1.x expects, on stdout, with nothing else there.

type goreleaserConfig struct {
	ProjectName string `yaml:"project_name"`
	Builds      []struct {
		ID     string   `yaml:"id"`
		Main   string   `yaml:"main"`
		Binary string   `yaml:"binary"`
		Goos   []string `yaml:"goos"`
		Goarch []string `yaml:"goarch"`
	} `yaml:"builds"`
	Archives []struct {
		IDs             []string `yaml:"ids"`
		Formats         []string `yaml:"formats"`
		NameTemplate    string   `yaml:"name_template"`
		WrapInDirectory any      `yaml:"wrap_in_directory"`
		Files           []any    `yaml:"files"`
	} `yaml:"archives"`
}

func TestReleaseArchiveServesLegacyUpgrades(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var config goreleaserConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	binaries := map[string]string{}
	for _, build := range config.Builds {
		binaries[build.ID] = build.Binary
		if build.Main != config.Builds[0].Main {
			t.Errorf("build %s compiles %s; both names must be one program", build.ID, build.Main)
		}
		for _, want := range []string{"darwin", "linux"} {
			if !strings.Contains(strings.Join(build.Goos, " "), want) {
				t.Errorf("build %s lacks %s", build.ID, want)
			}
		}
		for _, want := range []string{"amd64", "arm64"} {
			if !strings.Contains(strings.Join(build.Goarch, " "), want) {
				t.Errorf("build %s lacks %s", build.ID, want)
			}
		}
	}
	if len(config.Archives) != 1 {
		t.Fatalf("want one archive per platform, got %d archive definitions", len(config.Archives))
	}
	archive := config.Archives[0]
	got := map[string]bool{}
	for _, id := range archive.IDs {
		got[binaries[id]] = true
	}
	if !got["wtc"] || !got["taba"] {
		t.Fatalf("archive must hold both wtc and taba, holds %v", got)
	}
	// mise links the archive root: a wrapping directory would hide both names.
	if archive.WrapInDirectory != nil && archive.WrapInDirectory != false {
		t.Fatalf("archive must not wrap its contents in a directory: %v", archive.WrapInDirectory)
	}
	// mise selects the asset by OS and architecture in the file name.
	if !strings.Contains(archive.NameTemplate, "{{ .Os }}") || !strings.Contains(archive.NameTemplate, "{{ .Arch }}") {
		t.Fatalf("archive name must carry OS and architecture: %s", archive.NameTemplate)
	}
	if len(archive.Formats) != 1 || archive.Formats[0] != "tar.gz" {
		t.Fatalf("archive format = %v, want tar.gz", archive.Formats)
	}
}

func TestArchivedWtcAnswersLegacyReleaseNotesCall(t *testing.T) {
	root := t.TempDir() // stands in for the extracted archive
	for _, name := range []string{"taba", "wtc"} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(root, name), ".").CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, out)
		}
	}
	for _, name := range []string{"wtc", "taba"} {
		cmd := exec.Command(filepath.Join(root, name), "release-notes", "--since", "0.1.0", "--json")
		cmd.Env = append(os.Environ(), "WTC_DEV_BUILD=off", "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s release-notes: %v", name, err)
		}
		var envelope struct {
			Data []struct {
				Version string `json:"version"`
				Content string `json:"content"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out, &envelope); err != nil {
			t.Fatalf("%s release-notes stdout is not the JSON envelope 0.1.x decodes: %v\n%s", name, err, out)
		}
		if len(envelope.Data) == 0 || envelope.Data[0].Content == "" {
			t.Fatalf("%s release-notes returned no notes: %s", name, out)
		}
	}
}
