package levelup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

// ManagedRuntime is supplied by the framework, never by a web request.
type ManagedRuntime struct {
	Schema        int    `json:"schema"`
	Transport     string `json:"transport"`
	FrameworkAPI  string `json:"framework_api"`
	InstanceID    string `json:"instance_id"`
	DataDirectory string `json:"data_directory"`
}

func ReadManagedRuntime(path, fallback string) (ManagedRuntime, error) {
	var r ManagedRuntime
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return r, err
		}
		if err = json.Unmarshal(b, &r); err != nil {
			return r, err
		}
		if r.Schema != 1 || r.Transport != "native-ipc-v1" || r.FrameworkAPI != "3.0.0" || !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(r.InstanceID) || !filepath.IsAbs(r.DataDirectory) {
			return r, errors.New("invalid managed connection file")
		}
	} else if fallback != "" {
		if !filepath.IsAbs(fallback) {
			return r, errors.New("managed data directory must be absolute")
		}
		r.DataDirectory = fallback
	}
	return r, nil
}

// The immutable package provides defaults; changes and state belong in persistent /data.
func PrepareManagedConfig(base string, r ManagedRuntime) (string, error) {
	path := filepath.Join(r.DataDirectory, "config.json")
	if err := os.MkdirAll(r.DataDirectory, 0700); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(base, "config.json"))
	if err != nil {
		return "", err
	}
	if err = atomicFile(path, raw); err != nil {
		return "", err
	}
	return path, nil
}
