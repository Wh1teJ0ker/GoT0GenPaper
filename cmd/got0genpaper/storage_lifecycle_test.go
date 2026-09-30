package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStorageRoundTrip covers the untested persistence layer: JSON save/load
// for data artifacts, text outputs, and directory listing.
func TestStorageRoundTrip(t *testing.T) {
	a := newTestAPI(t)

	type payload struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	in := payload{Name: "parse_result", N: 42}
	if err := a.store.SaveDataJSON("probe", in); err != nil {
		t.Fatalf("SaveDataJSON: %v", err)
	}
	var out payload
	if err := a.store.LoadDataJSON("probe", &out); err != nil {
		t.Fatalf("LoadDataJSON: %v", err)
	}
	if out != in {
		t.Errorf("round-trip mismatch: got %+v want %+v", out, in)
	}

	// Loading a missing artifact must be an error (not a silent zero value).
	if err := a.store.LoadDataJSON("missing", &out); err == nil {
		t.Error("LoadDataJSON(missing) should error")
	}

	if err := a.store.SaveOutputText("probe.txt", "hello"); err != nil {
		t.Fatalf("SaveOutputText: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(a.store.OutputDir, "probe.txt"))
	if err != nil || string(got) != "hello" {
		t.Errorf("SaveOutputText round-trip: %q, %v", got, err)
	}

	files, err := a.store.ListDir(a.store.DataDir)
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	found := false
	for _, f := range files {
		if f == "probe.json" {
			found = true
		}
	}
	if !found {
		t.Errorf("ListDir missing probe.json: %v", files)
	}
}
