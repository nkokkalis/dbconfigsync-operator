package controller

import (
	"os"
	"strings"
	"testing"
)

func TestNewLocalReconciler(t *testing.T) {
	r := NewLocalReconciler("config.json", "out.env")

	if r.ConfigFile != "config.json" {
		t.Errorf("ConfigFile: got %q, want %q", r.ConfigFile, "config.json")
	}
	if r.OutEnvFile != "out.env" {
		t.Errorf("OutEnvFile: got %q, want %q", r.OutEnvFile, "out.env")
	}
	if r.ActiveConfigs == nil {
		t.Error("ActiveConfigs must not be nil")
	}
	if r.Processes == nil {
		t.Error("Processes must not be nil")
	}
	if r.ActiveVersion != 0 {
		t.Errorf("ActiveVersion: got %d, want 0", r.ActiveVersion)
	}
}

func TestWriteEnvFile(t *testing.T) {
	dir := t.TempDir()
	outFile := dir + "/out.env"

	r := NewLocalReconciler("config.json", outFile)
	r.ActiveVersion = 1

	configs := map[string]string{
		"DB_HOST": "localhost",
		"DB_PORT": "5432",
	}

	if err := r.writeEnvFile(configs); err != nil {
		t.Fatalf("writeEnvFile failed: %v", err)
	}

	content, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	lines := string(content)

	if !strings.Contains(lines, "DB_HOST=localhost") {
		t.Errorf("expected DB_HOST=localhost in output:\n%s", lines)
	}
	if !strings.Contains(lines, "DB_PORT=5432") {
		t.Errorf("expected DB_PORT=5432 in output:\n%s", lines)
	}

	info, err := os.Stat(outFile)
	if err != nil {
		t.Fatalf("failed to stat output file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("file permissions: got %v, want 0600", info.Mode().Perm())
	}
}

func TestWriteEnvFile_SkipsInvalidKeys(t *testing.T) {
	dir := t.TempDir()
	outFile := dir + "/out.env"

	r := NewLocalReconciler("config.json", outFile)
	r.ActiveVersion = 1

	configs := map[string]string{
		"VALID_KEY":   "valid_value",
		"KEY=INVALID": "bad_value_eq",
		"KEY\nBAD":    "bad_value_nl",
		"KEY\rBAD":    "bad_value_cr",
	}

	if err := r.writeEnvFile(configs); err != nil {
		t.Fatalf("writeEnvFile failed: %v", err)
	}

	content, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	lines := string(content)

	if !strings.Contains(lines, "VALID_KEY=valid_value") {
		t.Errorf("expected VALID_KEY=valid_value in output:\n%s", lines)
	}
	for _, bad := range []string{"bad_value_eq", "bad_value_nl", "bad_value_cr"} {
		if strings.Contains(lines, bad) {
			t.Errorf("expected %q to be skipped but found it in output:\n%s", bad, lines)
		}
	}
}

func TestWriteEnvFile_EscapesNewlinesInValues(t *testing.T) {
	dir := t.TempDir()
	outFile := dir + "/out.env"

	r := NewLocalReconciler("config.json", outFile)
	r.ActiveVersion = 2

	// Value contains literal newline and carriage return characters.
	configs := map[string]string{
		"MULTILINE": "line1\nline2\r\nline3",
	}

	if err := r.writeEnvFile(configs); err != nil {
		t.Fatalf("writeEnvFile failed: %v", err)
	}

	content, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	lines := string(content)

	// Newlines in values must be escaped so the file stays single-key-per-line.
	if !strings.Contains(lines, `MULTILINE=line1\nline2\r\nline3`) {
		t.Errorf("expected escaped newlines in output:\n%s", lines)
	}
}

func TestWriteEnvFile_VersionHeader(t *testing.T) {
	dir := t.TempDir()
	outFile := dir + "/out.env"

	r := NewLocalReconciler("config.json", outFile)
	r.ActiveVersion = 7

	if err := r.writeEnvFile(map[string]string{}); err != nil {
		t.Fatalf("writeEnvFile failed: %v", err)
	}

	content, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	if !strings.Contains(string(content), "Version v7") {
		t.Errorf("expected version header containing 'Version v7' in:\n%s", string(content))
	}
}
