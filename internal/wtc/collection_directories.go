package wtc

import (
	"os"
	"path/filepath"
)

// Select each namespace independently: generated .wtc/skills must not shadow
// an existing .taba/config. A malformed legacy parent or namespace fails rather
// than selecting a second configuration. This lookup never creates a directory.
func collectionDirectoryInput(collection, namespace string) (string, error) {
	for _, root := range []string{".wtc", ".taba"} {
		parent := filepath.Join(collection, root)
		if _, err := os.Lstat(parent); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return parent, err
		}
		if info, err := os.Stat(parent); err != nil {
			return parent, err
		} else if !info.IsDir() {
			return parent, &os.PathError{Op: "read", Path: parent, Err: os.ErrInvalid}
		}
		path := filepath.Join(parent, namespace)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return path, err
		}
		if info, err := os.Stat(path); err != nil {
			return path, err
		} else if !info.IsDir() {
			return path, &os.PathError{Op: "read", Path: path, Err: os.ErrInvalid}
		}
		return path, nil
	}
	return filepath.Join(collection, ".wtc", namespace), nil
}

// collectionMarker is shared by normal discovery and the early dev handover.
// An incomplete config directory still marks a boundary: opening it reports
// the missing registry instead of silently discovering a parent collection.
func collectionMarker(collection string) (bool, error) {
	if descriptorPresent(collection, ProjectDescriptorFile) {
		return false, nil
	}
	if descriptorPresent(collection, CollectionDescriptorFile) {
		return true, nil
	}
	return legacyCollectionMarker(collection)
}

func legacyCollectionMarker(collection string) (bool, error) {
	config, err := collectionDirectoryInput(collection, "config")
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(config); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	_, err = os.Lstat(filepath.Join(collection, "harness", ".harness-repos.yml"))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
