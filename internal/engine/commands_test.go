package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEnvAndOutputFile(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, "env")

	// Standard multiline and single line key-value pairs
	content := "KEY1=VAL1\nKEY2<<EOF\nline1\nline2\nEOF\nKEY3=VAL3\n"
	if err := os.WriteFile(envPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	vars, err := ParseCommandFile(envPath)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if vars["KEY1"] != "VAL1" {
		t.Errorf("expected KEY1=VAL1, got: %s", vars["KEY1"])
	}
	if vars["KEY2"] != "line1\nline2" {
		t.Errorf("expected KEY2 to be 'line1\\nline2', got: %s", vars["KEY2"])
	}
	if vars["KEY3"] != "VAL3" {
		t.Errorf("expected KEY3=VAL3, got: %s", vars["KEY3"])
	}
}

func TestParseNonExistentFile(t *testing.T) {
	vars, err := ParseCommandFile("/non/existent/path/for/test")
	if err != nil {
		t.Fatalf("expected nil error for non-existent file, got: %v", err)
	}
	if len(vars) != 0 {
		t.Fatalf("expected empty map, got: %v", vars)
	}
}

func TestParseEnvAndOutputFileAliases(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, "env")
	outPath := filepath.Join(tmpDir, "output")

	if err := os.WriteFile(envPath, []byte("A=1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, []byte("B=2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	envVars, err := ParseEnvFile(envPath)
	if err != nil {
		t.Fatalf("ParseEnvFile failed: %v", err)
	}
	if envVars["A"] != "1" {
		t.Errorf("expected A=1, got: %s", envVars["A"])
	}

	outVars, err := ParseOutputFile(outPath)
	if err != nil {
		t.Fatalf("ParseOutputFile failed: %v", err)
	}
	if outVars["B"] != "2" {
		t.Errorf("expected B=2, got: %s", outVars["B"])
	}
}

func TestParsePathFile(t *testing.T) {
	tmpDir := t.TempDir()
	pathFile := filepath.Join(tmpDir, "path")

	content := "/usr/local/bin\n/opt/homebrew/bin\n\n/custom/bin\n"
	if err := os.WriteFile(pathFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	paths, err := ParsePathFile(pathFile)
	if err != nil {
		t.Fatalf("ParsePathFile failed: %v", err)
	}
	expected := []string{"/usr/local/bin", "/opt/homebrew/bin", "/custom/bin"}
	if len(paths) != len(expected) {
		t.Fatalf("expected %d paths, got %d: %v", len(expected), len(paths), paths)
	}
	for i, p := range expected {
		if paths[i] != p {
			t.Errorf("expected path[%d]=%s, got %s", i, p, paths[i])
		}
	}

	// Non-existent path file
	nonExistent, err := ParsePathFile("/non/existent/path")
	if err != nil {
		t.Fatalf("expected nil error for non-existent path file, got: %v", err)
	}
	if len(nonExistent) != 0 {
		t.Fatalf("expected empty slice, got: %v", nonExistent)
	}
}

func TestParseComplexDelimiters(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, "env")

	content := "JSON_DATA<<ghadelimiter_unique123\n{\"foo\": \"bar\",\n\"num\": 123}\nghadelimiter_unique123\nEQUALS=foo=bar=baz\n"
	if err := os.WriteFile(envPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	vars, err := ParseCommandFile(envPath)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	expectedJSON := "{\"foo\": \"bar\",\n\"num\": 123}"
	if vars["JSON_DATA"] != expectedJSON {
		t.Errorf("expected JSON_DATA to be %q, got: %q", expectedJSON, vars["JSON_DATA"])
	}
	if vars["EQUALS"] != "foo=bar=baz" {
		t.Errorf("expected EQUALS to be 'foo=bar=baz', got: %q", vars["EQUALS"])
	}
}

func TestParsePreservesWhitespace(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, "env")

	content := "KEY=  value with spaces  \n"
	if err := os.WriteFile(envPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	vars, err := ParseCommandFile(envPath)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	expected := "  value with spaces  "
	if vars["KEY"] != expected {
		t.Errorf("expected %q, got: %q", expected, vars["KEY"])
	}
}

func TestParseCommandFile_EqualsBeforeHeredoc(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, "env")

	content := "CMD=cat <<EOF\nANOTHER=value\n"
	if err := os.WriteFile(envPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	vars, err := ParseCommandFile(envPath)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if vars["CMD"] != "cat <<EOF" {
		t.Errorf("expected CMD='cat <<EOF', got: %q", vars["CMD"])
	}
	if vars["ANOTHER"] != "value" {
		t.Errorf("expected ANOTHER='value', got: %q", vars["ANOTHER"])
	}
}

func TestParseCommandFile_LargeLine128KB(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, "env")

	largeVal := strings.Repeat("A", 128*1024)
	content := "LARGE_KEY=" + largeVal + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	vars, err := ParseCommandFile(envPath)
	if err != nil {
		t.Fatalf("expected successful parse of 128KB line, got err: %v", err)
	}

	if len(vars["LARGE_KEY"]) != 128*1024 {
		t.Errorf("expected 128KB value length, got %d", len(vars["LARGE_KEY"]))
	}
}

func TestParsePathFile_LargeLine128KB(t *testing.T) {
	tmpDir := t.TempDir()
	pathFile := filepath.Join(tmpDir, "path")

	largePath := "/usr/local/" + strings.Repeat("a", 128*1024)
	content := largePath + "\n"
	if err := os.WriteFile(pathFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	paths, err := ParsePathFile(pathFile)
	if err != nil {
		t.Fatalf("expected successful parse of 128KB path, got err: %v", err)
	}

	if len(paths) != 1 || paths[0] != largePath {
		t.Errorf("expected 1 path with length %d, got %d", len(largePath), len(paths[0]))
	}
}

func TestParseCommandFile_RejectsUnterminatedHeredocAndEmptyKey(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "unterminated")
	require.NoError(t, os.WriteFile(p1, []byte("KEY<<EOF\nhello\n"), 0o600))
	_, err := ParseCommandFile(p1)
	require.Error(t, err)

	p2 := filepath.Join(dir, "emptykey")
	require.NoError(t, os.WriteFile(p2, []byte("=bad\r\nGOOD=val\r\n"), 0o600))
	m, err := ParseCommandFile(p2)
	require.NoError(t, err)
	assert.NotContains(t, m, "")
	assert.Equal(t, "val", m["GOOD"])
}

