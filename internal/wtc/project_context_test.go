package wtc

import (
	"os"
	"path/filepath"
	"testing"
)

func localProject(t *testing.T) *ProjectContext {
	t.Helper()
	root := t.TempDir()
	fixtureFile(t, filepath.Join(root, ProjectDescriptorFile), "schema_version = 1\nmode = 'local'\n", 0644)
	fixtureFile(t, filepath.Join(root, ".taba/config/repos.yml"), "repos: []\n", 0644)
	p, err := OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectLocationWithoutCollections(t *testing.T) {
	p := localProject(t)
	for _, name := range []string{"TABA_CONFIG_ROOT", "WTC_CONFIG_ROOT"} {
		t.Setenv(name, "")
	}
	location, err := DiscoverLocation(p.Root)
	if err != nil || location.Kind != LocationProject || location.Collection != "" {
		t.Fatalf("location: %+v %v", location, err)
	}
	if _, err := Discover(p.Root); err == nil {
		t.Fatal("project selected an implicit collection")
	}
	planned := filepath.Join(p.Root, "first")
	scope, err := ResolveReceivingScope(ReceivingContext{Project: p, Collection: planned})
	if err != nil || scope.Root != filepath.Join(p.Root, ".config") {
		t.Fatalf("scope: %+v %v", scope, err)
	}
	for _, name := range []string{"first", ".bare", "harness", ".config", ".wtc"} {
		if _, err := os.Lstat(filepath.Join(p.Root, name)); !os.IsNotExist(err) {
			t.Fatalf("read-only discovery created %s", name)
		}
	}
	// .taba/config belongs to the project; it is not an ad-hoc collection.
	if present, err := collectionMarker(p.Root); err != nil || present {
		t.Fatalf("project misclassified as collection: %v %v", present, err)
	}
}

func TestNativeMemberAndStandaloneLocations(t *testing.T) {
	p := localProject(t)
	for _, name := range []string{"TABA_CONFIG_ROOT", "WTC_CONFIG_ROOT"} {
		t.Setenv(name, "")
	}
	member := filepath.Join(p.Root, "task")
	fixtureFile(t, filepath.Join(member, CollectionDescriptorFile), "schema_version=1\nproject='..'\n", 0644)
	c, err := OpenCollection(member)
	if err != nil {
		t.Fatal(err)
	}
	if c.AdHoc || c.Project == nil || c.ConfigRoot != filepath.Join(p.Root, ".config") {
		t.Fatalf("wrong receiving member: %+v", c)
	}
	if _, err := c.RenderEnv(); err != nil {
		t.Fatal(err)
	}
	standalone := filepath.Join(p.Root, "standalone")
	fixtureFile(t, filepath.Join(standalone, AdHocConfigDir, "repos.yml"), "repos: []\n", 0644)
	other, err := OpenCollection(standalone)
	if err != nil || !other.AdHoc || other.Project != nil || other.ConfigRoot != filepath.Join(standalone, ".config") {
		t.Fatalf("nested standalone inherited project: %+v %v", other, err)
	}
}

func TestProjectMetadataBoundariesFailClosed(t *testing.T) {
	for _, descriptor := range []string{
		"schema_version=2\nmode='local'\n",
		"schema_version=1\nmode='local'\nroot='/foreign'\n",
		"schema_version=1\nmode='local'\n[secrets]\nallow_external=true\n",
		"schema_version=1\nmode='harness'\n[harness]\npath='../foreign'\n",
		"schema_version=1\nmode='local'\n[harness]\npath='harness'\n",
	} {
		p := localProject(t)
		child := filepath.Join(p.Root, "nested")
		fixtureFile(t, filepath.Join(child, ProjectDescriptorFile), descriptor, 0644)
		location, err := DiscoverLocation(filepath.Join(child, "subdir"))
		if err == nil || location.Kind != LocationConflict {
			t.Fatalf("malformed boundary fell through to parent: %+v %v", location, err)
		}
	}
	p := localProject(t)
	outer := filepath.Join(p.Root, "outer")
	fixtureFile(t, filepath.Join(outer, ProjectDescriptorFile), "schema_version=1\nmode='local'\n", 0644)
	member := filepath.Join(outer, "task")
	fixtureFile(t, filepath.Join(member, CollectionDescriptorFile), "schema_version=1\nproject='../..'\n", 0644)
	if _, err := DiscoverLocation(member); err == nil {
		t.Fatal("collection pointer skipped nested project")
	}
	if err := os.Remove(filepath.Join(member, CollectionDescriptorFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(p.Root, ProjectDescriptorFile), filepath.Join(member, CollectionDescriptorFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverLocation(member); err == nil {
		t.Fatal("accepted symlinked descriptor")
	}
}

func TestProjectReadsExplicitOwnerSnapshot(t *testing.T) {
	for _, name := range []string{"TABA_CONFIG_ROOT", "WTC_CONFIG_ROOT"} {
		t.Setenv(name, "")
	}
	root := realPath(t, t.TempDir())
	source := filepath.Join(root, "source")
	fixtureFile(t, filepath.Join(source, "taba.toml"), "[secrets]\nroot = '../../../.config'\n", 0644)
	fixtureFile(t, filepath.Join(source, ".harness-repos.yml"), "repos: []\n", 0644)
	fixtureGit(t, "-C", source, "init", "-q", "-b", "main")
	fixtureGit(t, "-C", source, "add", "-A")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	owner := filepath.Join(root, ".bare/harness.git")
	fixtureGit(t, "clone", "-q", "--bare", source, owner)
	fixtureFile(t, filepath.Join(root, ProjectDescriptorFile), "schema_version=1\nmode='harness'\n[harness]\nowner='.bare/harness.git'\nref='main'\n", 0644)
	p, err := OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.Source.Owner != owner || len(p.Source.Ref) != 40 {
		t.Fatalf("snapshot not resolved: %+v", p)
	}
	// Receiver policy is from the explicit immutable source, never a first collection.
	if _, err := ResolveReceivingScope(ReceivingContext{Project: p}); err == nil {
		t.Fatal("external snapshot policy lacked receiving opt-in")
	}
	if _, err := os.Stat(filepath.Join(root, "first")); !os.IsNotExist(err) {
		t.Fatal("snapshot discovery created a collection")
	}
}
