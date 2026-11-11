package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestLocatorMigratesLegacyAndPreservesEveryDeployment(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	path := filepath.Join(root, "locator.yaml")
	legacy, err := yaml.Marshal(locatorRecord{SchemaVersion: 1, Dir: first})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	locator := Locator{Path: path}
	for _, dir := range []string{second, first, second} {
		if err := locator.WithLock(context.Background(), func() error { return locator.Write(dir) }); err != nil {
			t.Fatal(err)
		}
	}
	dirs, err := locator.Directories()
	if err != nil || len(dirs) != 2 {
		t.Fatalf("directories=%v error=%v", dirs, err)
	}
	selected, err := locator.Resolve("")
	if err != nil || selected != first {
		t.Fatalf("selected=%s error=%v", selected, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved locatorRecord
	if err := yaml.Unmarshal(data, &saved); err != nil || saved.SchemaVersion != 2 {
		t.Fatalf("schema not migrated: %s %v", data, err)
	}
}

func TestLocatorConcurrentRegistrationsDoNotLoseEntries(t *testing.T) {
	root := t.TempDir()
	locator := Locator{Path: filepath.Join(root, "locator.yaml")}
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			err := locator.WithLock(context.Background(), func() error { return locator.Write(filepath.Join(root, strconv.Itoa(i))) })
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	dirs, err := locator.Directories()
	if err != nil || len(dirs) != 6 {
		t.Fatalf("directories=%v error=%v", dirs, err)
	}
}

func TestLocatorRejectsMalformedRegistryWithoutOverwriting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "locator.yaml")
	data, err := yaml.Marshal(locatorRecord{SchemaVersion: 2, Dir: root, Dirs: []string{"relative/path"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	locator := Locator{Path: path}
	if _, err := locator.Directories(); err == nil {
		t.Fatal("accepted a relative registry path")
	}
	if err := locator.Write(filepath.Join(root, "other")); err == nil {
		t.Fatal("overwrote malformed registry")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatalf("registry was modified: %s %v", after, err)
	}
}
