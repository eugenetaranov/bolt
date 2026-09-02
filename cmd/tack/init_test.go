package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRunInit_CreatesFilesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var buf bytes.Buffer
	initCmd.SetOut(&buf)

	// First run creates both files.
	if err := runInit(initCmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	for _, name := range []string{"site.yaml", "inventory.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to be created: %v", name, err)
		}
	}
	if !bytes.Contains(buf.Bytes(), []byte("Created site.yaml")) {
		t.Errorf("expected creation message, got: %s", buf.String())
	}

	// Second run must not overwrite; it reports skips.
	buf.Reset()
	if err := runInit(initCmd, nil); err != nil {
		t.Fatalf("runInit (2nd): %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("Skipped site.yaml")) {
		t.Errorf("expected skip message on re-run, got: %s", buf.String())
	}
}
