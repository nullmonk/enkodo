package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPanicRecoveryPrintsFileAndLine(t *testing.T) {
	// Create a temporary directory with a Go file that causes a panic during processing.
	// In GetStructFields, an embedded/anonymous field has len(field.Names) == 0,
	// which causes a panic on field.Names[0].
	tmpDir := t.TempDir()
	badFile := filepath.Join(tmpDir, "bad.go")
	badContent := `package badpkg

import "sync"

type Crasher struct {
	sync.Mutex
	Name string ` + "`enkodo:\"\"`" + `
}
`
	if err := os.WriteFile(badFile, []byte(badContent), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Capture stderr and mock exitFunc
	var exitCode int
	oldExit := exitFunc
	exitFunc = func(code int) {
		exitCode = code
	}
	defer func() {
		exitFunc = oldExit
	}()

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stderr = w

	_ = objectsInFile(badFile)

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	output := buf.String()

	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(output, "bad.go:6") {
		t.Errorf("expected output to contain 'bad.go:6', got: %s", output)
	}
	if !strings.Contains(output, "panic:") {
		t.Errorf("expected output to contain 'panic:', got: %s", output)
	}
}

func TestPanicRecoveryWithoutPosition(t *testing.T) {
	var exitCode int
	oldExit := exitFunc
	exitFunc = func(code int) {
		exitCode = code
	}
	defer func() {
		exitFunc = oldExit
	}()

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stderr = w

	currentPos = 0
	currentFset = nil

	func() {
		defer func() {
			if rec := recover(); rec != nil {
				filename := "unknown.go"
				line := 0
				if currentFset != nil && currentPos.IsValid() {
					pos := currentFset.Position(currentPos)
					line = pos.Line
				}
				if line > 0 {
					fmt.Fprintf(os.Stderr, "%s:%d: panic: %v\n", filename, line, rec)
				} else {
					fmt.Fprintf(os.Stderr, "%s: panic: %v\n", filename, rec)
				}
				exitFunc(1)
			}
		}()
		panic("synthetic panic without position")
	}()

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	output := buf.String()

	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(output, "unknown.go: panic: synthetic panic without position") {
		t.Errorf("expected output to contain 'unknown.go: panic: synthetic panic without position', got: %s", output)
	}
}

func TestCLIProcessFilePanic(t *testing.T) {
	tmpDir := t.TempDir()
	badFile := filepath.Join(tmpDir, "crash.go")
	badContent := `package crashpkg

import "sync"

type CrashingStruct struct {
	sync.Mutex
	Value int ` + "`enkodo:\"\"`" + `
}
`
	if err := os.WriteFile(badFile, []byte(badContent), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cmd := exec.Command("go", "run", ".", badFile)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error, got success")
	}

	out := stderr.String()
	if !strings.Contains(out, "crash.go:6") {
		t.Errorf("expected stderr to contain 'crash.go:6', got:\n%s", out)
	}
	if !strings.Contains(out, "panic:") {
		t.Errorf("expected stderr to contain 'panic:', got:\n%s", out)
	}
}

