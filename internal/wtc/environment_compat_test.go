package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTabaConfigRootEnvironmentAndGeneratedFile(t *testing.T) {
	c := fixture(t)
	t.Setenv("WTC_CONFIG_ROOT", c.ConfigRoot)
	t.Setenv("TABA_CONFIG_ROOT", filepath.Join(t.TempDir(), "foreign"))
	if _, err := OpenCollection(c.Collection); err == nil {
		t.Fatal("conflicting root aliases accepted")
	}
	t.Setenv("TABA_CONFIG_ROOT", c.ConfigRoot)
	opened, err := OpenCollection(c.Collection)
	if err != nil {
		t.Fatal(err)
	}
	data, err := opened.RenderEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "WTC_CONFIG_ROOT='"+c.ConfigRoot+"'\nTABA_CONFIG_ROOT='"+c.ConfigRoot+"'\n") {
		t.Fatal("approved root aliases missing")
	}
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection"), "TABA_CONFIG_ROOT='"+c.ConfigRoot+"'\nWTC_CONFIG_ROOT=/foreign\n", 0644)
	if _, err := OpenCollection(c.Collection); err == nil {
		t.Fatal("conflicting generated roots accepted")
	}
}

func TestTabaDataRootAndCacheSettings(t *testing.T) {
	t.Setenv("WTC_DATA_HOME", filepath.Join(t.TempDir(), "legacy"))
	want := filepath.Join(t.TempDir(), "current")
	t.Setenv("TABA_DATA_HOME", want)
	if got, err := UserDataRoot(); err != nil || got != want {
		t.Fatalf("data root = %q, %v", got, err)
	}
	t.Setenv("WTC_FORGE_CACHE_AGE", "11")
	t.Setenv("TABA_FORGE_CACHE_AGE", "23")
	if statusForgeCacheAge() != 23*time.Second || statusMergedCacheAge() != 23*time.Second {
		t.Fatal("TABA cache age did not apply to active and merged facts")
	}
	t.Setenv("WTC_STATUS_RETURN_MERGED", "yes")
	t.Setenv("TABA_STATUS_RETURN_MERGED", "no")
	if fixture(t).statusReturnMergedEnabled() {
		t.Fatal("new-prefix opt-out was ignored")
	}
}

func TestTabaTWGSiteSourcePrecedenceAndExplicitEmpty(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WTC_TWG_SITE", "")
	t.Setenv("TABA_TWG_SITE", "")
	for _, key := range []string{"WTC_TWG_SITE", "TABA_TWG_SITE"} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	fixtureFile(t, filepath.Join(root, "wtc.env"), "TABA_TWG_SITE='current'\nexport WTC_TWG_SITE=legacy\n", 0600)
	if got, err := machineTWGSite(root); err != nil || got != "current" {
		t.Fatalf("new-prefix workspace default = %q, %v", got, err)
	}
	t.Setenv("WTC_TWG_SITE", "environment")
	if got, _ := machineTWGSite(root); got != "environment" {
		t.Fatalf("environment must override file defaults: %q", got)
	}
	t.Setenv("TABA_TWG_SITE", "")
	if got, _ := machineTWGSite(root); got != "" {
		t.Fatalf("explicit empty new value should disable the site: %q", got)
	}
}
