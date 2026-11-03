package wtc

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func aliasSafetySnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		value := fmt.Sprintf("%v:%d", info.Mode(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + string(data)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += ":" + target
		}
		result[relative] = value
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return result
}

func TestAliasSafetyMalformedRetirementMarkersKeepGuard(t *testing.T) {
	for _, valid := range retiredMarkerNames {
		for _, kind := range []string{"directory", "symlink"} {
			t.Run(valid+"/"+kind, func(t *testing.T) {
				target := filepath.Join(t.TempDir(), "retired")
				fixtureFile(t, filepath.Join(target, valid), "{}", 0600)
				malformed := retiredMarkerNames[0]
				if malformed == valid {
					malformed = retiredMarkerNames[1]
				}
				outside := t.TempDir()
				fixtureFile(t, filepath.Join(outside, "keep"), "untouched", 0600)
				if kind == "directory" {
					fixtureFile(t, filepath.Join(target, malformed, "keep"), "leftover", 0600)
				} else if err := os.Symlink(outside, filepath.Join(target, malformed)); err != nil {
					t.Fatal(err)
				}
				before := aliasSafetySnapshot(t, outside)
				malformedPath := filepath.Join(target, malformed)
				beforeMalformed := aliasSafetySnapshot(t, malformedPath)
				for attempt := 0; attempt < 2; attempt++ {
					result, err := (&Context{}).finishRetiredFolder(target)
					if err != nil {
						t.Fatal(err)
					}
					if result.FolderRemoved || !RetiredFolder(target) || !strings.Contains(strings.Join(result.Leftovers, " "), malformed) {
						t.Fatalf("lost retirement guard or malformed leftover: %+v", result)
					}
					if !reflect.DeepEqual(beforeMalformed, aliasSafetySnapshot(t, malformedPath)) {
						t.Fatal("guard refresh replaced the malformed marker")
					}
				}
				if !reflect.DeepEqual(before, aliasSafetySnapshot(t, outside)) {
					t.Fatal("cleanup followed malformed marker")
				}
			})
		}
	}
}

func TestAliasSafetyLinkedServiceParentsRefuseBeforeEditing(t *testing.T) {
	for _, namespace := range runtimeNamespaces {
		t.Run(namespace, func(t *testing.T) {
			c := runtimeFixture(t)
			outside := t.TempDir()
			fixtureFile(t, filepath.Join(outside, "widget.yaml"), "tasks: {}\n", 0600)
			if err := os.MkdirAll(filepath.Join(c.Collection, namespace), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(c.Collection, namespace, "svc")); err != nil {
				t.Fatal(err)
			}
			before := aliasSafetySnapshot(t, outside)
			if err := c.runtimePathsSafe(); err == nil {
				t.Error("linked services accepted by path validation")
			}
			if _, _, err := c.runtimePlan("dekit"); err == nil {
				t.Error("linked services accepted by planning")
			}
			if err := c.runtimeChange(c.runtimeLocalFragment("widget"), func(tasks *yaml.Node) error {
				return runtimeAddTask(tasks, "added/main", map[string]any{"cmd": []string{"synthetic-command"}})
			}); err == nil {
				t.Error("linked services accepted by editing")
			}
			if !reflect.DeepEqual(before, aliasSafetySnapshot(t, outside)) {
				t.Fatal("refused service edit changed linked target")
			}
		})
	}
}

func TestAliasSafetyRefusedRuntimeCommandsDoNotNotify(t *testing.T) {
	operations := map[string]func(*Context) error{
		"retirement": func(c *Context) error { return c.RetireRuntime() },
		"removal":    func(c *Context) error { _, err := c.RemoveService("web", true); return err },
		"render":     func(c *Context) error { _, err := c.RenderRuntime(false); return err },
		"action":     func(c *Context) error { _, err := c.RuntimeAction("up", "", 0); return err },
	}
	for _, namespace := range runtimeNamespaces {
		for _, state := range []string{"dual", "runtime-link", "namespace-link"} {
			for name, operation := range operations {
				t.Run(namespace+"/"+state+"/"+name, func(t *testing.T) {
					c := runtimeFixture(t)
					outside := t.TempDir()
					fixtureFile(t, filepath.Join(outside, "manifest.json"), "{}", 0600)
					switch state {
					case "dual":
						for _, home := range runtimeNamespaces {
							fixtureFile(t, filepath.Join(c.Collection, home, "runtime", "manifest.json"), "{}", 0600)
						}
					case "runtime-link":
						if err := os.MkdirAll(filepath.Join(c.Collection, namespace), 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(outside, filepath.Join(c.Collection, namespace, "runtime")); err != nil {
							t.Fatal(err)
						}
					case "namespace-link":
						fixtureFile(t, filepath.Join(outside, "runtime", "manifest.json"), "{}", 0600)
						if err := os.Symlink(outside, filepath.Join(c.Collection, namespace)); err != nil {
							t.Fatal(err)
						}
					}
					beforeOutside := aliasSafetySnapshot(t, outside)
					before := map[string]map[string]string{}
					for _, home := range runtimeNamespaces {
						before[home] = aliasSafetySnapshot(t, filepath.Join(c.Collection, home))
					}
					if err := operation(c); err == nil {
						t.Fatal("unsafe operation was not refused")
					}
					if !reflect.DeepEqual(beforeOutside, aliasSafetySnapshot(t, outside)) {
						t.Error("refusal changed external state")
					}
					for _, home := range runtimeNamespaces {
						if !reflect.DeepEqual(before[home], aliasSafetySnapshot(t, filepath.Join(c.Collection, home))) {
							t.Errorf("refusal changed %s state", home)
						}
					}
				})
			}
		}
	}
}

func TestAliasSafetyNotifierRejectsLinkedMarker(t *testing.T) {
	for _, namespace := range runtimeNamespaces {
		for _, dangling := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dangling=%v", namespace, dangling), func(t *testing.T) {
				c := &Context{Collection: t.TempDir()}
				fixtureFile(t, filepath.Join(c.Collection, namespace, "runtime", "manifest.json"), "{}", 0600)
				outside := t.TempDir()
				target := filepath.Join(outside, "untouched")
				if !dangling {
					fixtureFile(t, target, "keep", 0600)
					old := time.Unix(100, 0)
					if err := os.Chtimes(target, old, old); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, c.statusSignalPath()); err != nil {
					t.Fatal(err)
				}
				before := aliasSafetySnapshot(t, outside)
				c.signalServicesChanged()
				if !reflect.DeepEqual(before, aliasSafetySnapshot(t, outside)) {
					t.Fatal("notifier followed marker link")
				}
				if info, err := os.Lstat(c.statusSignalPath()); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("notifier replaced linked marker")
				}
			})
		}
	}
}
