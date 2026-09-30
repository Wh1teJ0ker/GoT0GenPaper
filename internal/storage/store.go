// Package storage wraps persistent storage for data artifacts.
// MVP uses local filesystem; V2 can swap in SQLite/Postgres.
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Store manages data, output, and template directories.
type Store struct {
	DataDir   string // 真题/KG/标注
	OutputDir string // 产出试卷
	Templates string // LaTeX 模板库
}

// New creates a Store, ensuring directories exist.
func New(dataDir, outputDir, templates string) (*Store, error) {
	for _, d := range []string{dataDir, outputDir, templates} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return &Store{DataDir: dataDir, OutputDir: outputDir, Templates: templates}, nil
}

// SaveJSON marshals and writes a JSON file to the given dir.
func (s *Store) SaveJSON(dir, name string, v interface{}) error {
	path := filepath.Join(dir, name+".json")
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", name, err)
	}
	return os.WriteFile(path, data, 0644)
}

// LoadJSON reads and unmarshals a JSON file.
func (s *Store) LoadJSON(dir, name string, v interface{}) error {
	path := filepath.Join(dir, name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return json.Unmarshal(data, v)
}

// SaveText writes a text file (e.g. LaTeX output).
func (s *Store) SaveText(dir, name, content string) error {
	path := filepath.Join(dir, name)
	return os.WriteFile(path, []byte(content), 0644)
}

// LoadText reads a text file.
func (s *Store) LoadText(dir, name string) (string, error) {
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

// ListDir returns file names in a directory (non-recursive).
func (s *Store) ListDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Exists checks whether a file exists.
func (s *Store) Exists(dir, name string) bool {
	path := filepath.Join(dir, name)
	_, err := os.Stat(path)
	return err == nil
}

// SaveDataJSON saves to the DataDir.
func (s *Store) SaveDataJSON(name string, v interface{}) error {
	return s.SaveJSON(s.DataDir, name, v)
}

// LoadDataJSON loads from DataDir.
func (s *Store) LoadDataJSON(name string, v interface{}) error {
	return s.LoadJSON(s.DataDir, name, v)
}

// SaveOutputJSON saves to OutputDir.
func (s *Store) SaveOutputJSON(name string, v interface{}) error {
	return s.SaveJSON(s.OutputDir, name, v)
}

// SaveOutputText saves a text file to OutputDir.
func (s *Store) SaveOutputText(name, content string) error {
	return s.SaveText(s.OutputDir, name, content)
}
