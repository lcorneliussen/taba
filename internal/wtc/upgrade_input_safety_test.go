package wtc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradePreservesDirtySymlinkTarget(t *testing.T) {
	c, _ := upgradeFixture(t)
	fixtureGit(t, "-C", c.Harness, "mv", ".wtc-cli-version", ".taba-version")
	if err := os.Symlink(".taba-version", filepath.Join(c.Harness, ".wtc-cli-version")); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", c.Harness, "add", ".wtc-cli-version")
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "alias")
	target := filepath.Join(c.Harness, ".taba-version")
	fixtureFile(t, target, "0.1.10\n", 0644)
	_, err := c.Upgrade(UpgradeOptions{Version: "0.1.12"})
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "0.1.10\n" {
		t.Errorf("dirty target overwritten: %q (%v)", data, readErr)
	}
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("dirty target not refused: %v", err)
	}
}

func TestUpgradeRefusesInProgressGit(t *testing.T) {
	for _, operation := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry=%t", operation, dry), func(t *testing.T) {
				c, _ := upgradeFixture(t)
				marker := fixtureGit(t, "-C", c.Harness, "rev-parse", "--git-path", operation)
				if !filepath.IsAbs(marker) {
					marker = filepath.Join(c.Harness, marker)
				}
				if strings.Contains(operation, "rebase-") || operation == "sequencer" {
					if err := os.Mkdir(marker, 0755); err != nil {
						t.Fatal(err)
					}
				} else {
					fixtureFile(t, marker, fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")+"\n", 0644)
				}
				assertUpgradeRefusal(t, c, dry, operation)
			})
		}
	}
}

func upgradeAlias(t *testing.T, c *Context, name, target string) {
	t.Helper()
	fixtureGit(t, "-C", c.Harness, "mv", name, target)
	if err := os.Symlink(target, filepath.Join(c.Harness, name)); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", c.Harness, "add", "--", name)
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "input alias")
}

func TestUpgradeProtectsAliasedInputs(t *testing.T) {
	for _, name := range []string{".wtc-cli-version", "wtc.toml"} {
		for _, staged := range []bool{false, true} {
			for _, dry := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/staged=%t/dry=%t", name, staged, dry), func(t *testing.T) {
					c, _ := upgradeFixture(t)
					target := ".taba-version"
					edited := "0.1.10\n"
					if name == "wtc.toml" {
						target, edited = "taba.toml", "[compatibility]\nrequires = \">=0.1.9,<0.2\"\n# owner edit\n"
					}
					upgradeAlias(t, c, name, target)
					fixtureFile(t, filepath.Join(c.Harness, target), edited, 0644)
					if staged {
						fixtureGit(t, "-C", c.Harness, "add", "--", target)
					}
					assertUpgradeRefusal(t, c, dry, "uncommitted")
				})
			}
		}
	}
}

func assertUpgradeRefusal(t *testing.T, c *Context, dry bool, message string) {
	t.Helper()
	before := map[string][]byte{}
	for _, name := range []string{".wtc-cli-version", "wtc.toml", ".taba-version", "taba.toml", "middle", "alternate"} {
		if data, err := os.ReadFile(filepath.Join(c.Harness, name)); err == nil {
			before[name] = data
		}
	}
	installLog := filepath.Join(c.Workspace, "refused-install.log")
	t.Setenv("MOCK_REFUSED_UPGRADE_INSTALL_LOG", installLog)
	fixtureFile(t, filepath.Join(c.Workspace, "bin", "mise"), "#!/bin/sh\nprintf called >> \"$MOCK_REFUSED_UPGRADE_INSTALL_LOG\"\n", 0755)
	envFile := filepath.Join(c.Collection, "mise.toml")
	envBefore, envErr := os.ReadFile(envFile)
	_, err := c.Upgrade(UpgradeOptions{Version: "0.1.12", DryRun: dry})
	if err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("expected %q refusal, got %v", message, err)
	}
	for name, want := range before {
		if data, err := os.ReadFile(filepath.Join(c.Harness, name)); err != nil || string(data) != string(want) {
			t.Errorf("refusal changed %s: %q (%v)", name, data, err)
		}
	}
	if data, err := os.ReadFile(installLog); !os.IsNotExist(err) {
		t.Errorf("refused upgrade called mise: %q %v", data, err)
	}
	if data, err := os.ReadFile(envFile); string(data) != string(envBefore) || os.IsNotExist(err) != os.IsNotExist(envErr) {
		t.Errorf("refusal changed generated environment: %q %v", data, err)
	}
}

func TestUpgradeCleanAliasesPreserveLinksAndIndependentInputs(t *testing.T) {
	c, _ := upgradeFixture(t)
	upgradeAlias(t, c, ".wtc-cli-version", ".taba-version")
	upgradeAlias(t, c, "wtc.toml", "taba.toml")
	// An unrelated untracked file must not block the upgrade.
	fixtureGit(t, "-C", c.Harness, "mv", ".wtc-cli-version", "middle")
	if err := os.Symlink("middle", filepath.Join(c.Harness, ".wtc-cli-version")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.Harness, "unrelated"), "keep\n", 0644)
	fixtureGit(t, "-C", c.Harness, "add", ".wtc-cli-version")
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "chain")
	if _, err := c.Upgrade(UpgradeOptions{Version: "0.1.11", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(c.Harness, ".taba-version")); string(data) != "0.1.9\n" {
		t.Fatal("dry run changed target")
	}
	result, err := c.Upgrade(UpgradeOptions{Version: "0.1.11"})
	if err != nil || strings.Join(result.Files, ",") != ".wtc-cli-version,wtc.toml" {
		t.Fatalf("upgrade: %+v %v", result, err)
	}
	for name, target := range map[string]string{".wtc-cli-version": "middle", "middle": ".taba-version", "wtc.toml": "taba.toml"} {
		if got, err := os.Readlink(filepath.Join(c.Harness, name)); err != nil || got != target {
			t.Errorf("alias %s changed: %s %v", name, got, err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(c.Harness, ".taba-version")); string(data) != "0.1.11\n" {
		t.Fatalf("target not upgraded: %q", data)
	}
	assertUpgradeRefusal(t, c, false, "uncommitted")
	fixtureGit(t, "-C", c.Harness, "add", ".taba-version", "taba.toml")
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "upgrade")
	if _, err := c.Upgrade(UpgradeOptions{Version: "0.1.11"}); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("same version: %v", err)
	}
}

func TestUpgradeRejectsUnsafeAliasTargets(t *testing.T) {
	for _, kind := range []string{"outside", "relative-outside", "broken", "loop", "untracked", "ignored", "directory", "dirty-middle"} {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry=%t", kind, dry), func(t *testing.T) {
				c, _ := upgradeFixture(t)
				target, message := "middle", ""
				switch kind {
				case "outside":
					target, message = filepath.Join(c.Workspace, "outside-pin"), "escapes"
					fixtureFile(t, target, "0.1.9\n", 0644)
				case "relative-outside":
					target, message = "../outside-pin", "escapes"
					fixtureFile(t, filepath.Join(c.Collection, "outside-pin"), "0.1.9\n", 0644)
				case "broken":
					message = "no such file"
				case "loop":
					target, message = ".wtc-cli-version", "too many"
				case "untracked", "ignored":
					message = "tracked"
					fixtureFile(t, filepath.Join(c.Harness, target), "0.1.9\n", 0644)
					if kind == "ignored" {
						fixtureFile(t, filepath.Join(c.Harness, ".gitignore"), "middle\n", 0644)
						fixtureGit(t, "-C", c.Harness, "add", ".gitignore")
					}
				case "directory":
					target, message = ".", "regular file"
				case "dirty-middle":
					message = "uncommitted"
					fixtureFile(t, filepath.Join(c.Harness, "alternate"), "0.1.9\n", 0644)
					if err := os.Symlink(".taba-version", filepath.Join(c.Harness, "middle")); err != nil {
						t.Fatal(err)
					}
					fixtureFile(t, filepath.Join(c.Harness, ".taba-version"), "0.1.9\n", 0644)
					fixtureGit(t, "-C", c.Harness, "add", "middle", "alternate", ".taba-version")
				}
				if err := os.Remove(filepath.Join(c.Harness, ".wtc-cli-version")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(c.Harness, ".wtc-cli-version")); err != nil {
					t.Fatal(err)
				}
				fixtureGit(t, "-C", c.Harness, "add", ".wtc-cli-version")
				fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "alias")
				if kind == "dirty-middle" {
					if err := os.Remove(filepath.Join(c.Harness, "middle")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("alternate", filepath.Join(c.Harness, "middle")); err != nil {
						t.Fatal(err)
					}
				}
				assertUpgradeRefusal(t, c, dry, message)
				if kind == "outside" {
					if data, _ := os.ReadFile(target); string(data) != "0.1.9\n" {
						t.Fatalf("outside target changed: %q", data)
					}
				}
			})
		}
	}
}

func TestUpgradeDoesNotSynchronizeIndependentInputs(t *testing.T) {
	c, _ := upgradeFixture(t)
	fixtureFile(t, filepath.Join(c.Harness, ".taba-version"), "0.1.10\n", 0644)
	fixtureFile(t, filepath.Join(c.Harness, "taba.toml"), "# independently maintained\n", 0644)
	if _, err := c.Upgrade(UpgradeOptions{Version: "0.1.12"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{".taba-version": "0.1.10\n", "taba.toml": "# independently maintained\n"} {
		if data, _ := os.ReadFile(filepath.Join(c.Harness, name)); string(data) != want {
			t.Fatalf("independent input %s changed: %q", name, data)
		}
	}
}

func TestUpgradeNestedAliasUsesLiteralGitPaths(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(fmt.Sprint(absolute), func(t *testing.T) {
			c, _ := upgradeFixture(t)
			target := "pins/[selected]*"
			fixtureFile(t, filepath.Join(c.Harness, target), "0.1.9\n", 0644)
			fixtureGit(t, "-C", c.Harness, "add", "--", ":(literal)"+target)
			if err := os.Remove(filepath.Join(c.Harness, ".wtc-cli-version")); err != nil {
				t.Fatal(err)
			}
			link := target
			if absolute {
				link = filepath.Join(c.Harness, target)
			}
			if err := os.Symlink(link, filepath.Join(c.Harness, ".wtc-cli-version")); err != nil {
				t.Fatal(err)
			}
			fixtureGit(t, "-C", c.Harness, "add", ".wtc-cli-version")
			fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "nested alias")
			fixtureFile(t, filepath.Join(c.Harness, "pins/sibling"), "unrelated edit\n", 0644)
			if _, err := c.Upgrade(UpgradeOptions{Version: "0.1.11"}); err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(filepath.Join(c.Harness, target)); string(data) != "0.1.11\n" {
				t.Fatalf("target not upgraded: %q", data)
			}
		})
	}
}

func TestUpgradeReportsGitInspectionFailure(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprint(dry), func(t *testing.T) {
			c, _ := upgradeFixture(t)
			if err := os.Rename(filepath.Join(c.Harness, ".git"), filepath.Join(c.Harness, ".git.saved")); err != nil {
				t.Fatal(err)
			}
			assertUpgradeRefusal(t, c, dry, "not a git repository")
		})
	}
}
