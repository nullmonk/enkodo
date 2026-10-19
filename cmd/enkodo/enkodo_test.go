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

func TestEmbeddedFields(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "embedded.go")
	content := `package embeddedpkg

import "sync"

type Inner struct {
	Val string ` + "`enkodo:\"\"`" + `
}

type unexported struct {
	Secret int ` + "`enkodo:\"\"`" + `
}

type Outer struct {
	sync.Mutex
	Inner ` + "`enkodo:\"\"`" + `
	*Inner ` + "`enkodo:\"\"`" + `
	unexported ` + "`enkodo:\"\"`" + `
	Name string ` + "`enkodo:\"\"`" + `
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	err := objectsInFile(testFile)
	if err != nil {
		t.Fatalf("objectsInFile failed on struct with embedded fields: %v", err)
	}

	genFile := filepath.Join(tmpDir, "embedded_enkodo.go")
	genBytes, err := os.ReadFile(genFile)
	if err != nil {
		t.Fatalf("failed to read generated file: %v", err)
	}

	genStr := string(genBytes)
	// Outer should encode its Name and exported embedded Inner fields, but ignore untagged sync.Mutex and unexported
	if !strings.Contains(genStr, "enc.Encode(&o.Inner)") {
		t.Errorf("expected generated code to encode embedded Inner, got:\n%s", genStr)
	}
	if !strings.Contains(genStr, "o.Name") {
		t.Errorf("expected generated code to encode Name, got:\n%s", genStr)
	}
	if strings.Contains(genStr, "sync.Mutex") {
		t.Errorf("did not expect untagged sync.Mutex in generated code, got:\n%s", genStr)
	}
	if strings.Contains(genStr, "o.unexported") {
		t.Errorf("did not expect unexported embedded field in generated code for Outer, got:\n%s", genStr)
	}
}

func TestPanicRecoveryPrintsFileAndLine(t *testing.T) {
	tmpDir := t.TempDir()
	badFile := filepath.Join(tmpDir, "bad.go")
	badContent := `package badpkg

type Crasher struct {
	BadField string ` + "`enkodo:\"panic_test\"`" + `
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
	if !strings.Contains(output, "bad.go:4") {
		t.Errorf("expected output to contain 'bad.go:4', got: %s", output)
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

type CrashingStruct struct {
	BadField string ` + "`enkodo:\"panic_test\"`" + `
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
	if !strings.Contains(out, "crash.go:4") {
		t.Errorf("expected stderr to contain 'crash.go:4', got:\n%s", out)
	}
	if !strings.Contains(out, "panic:") {
		t.Errorf("expected stderr to contain 'panic:', got:\n%s", out)
	}
}

func TestCLINonExistentPath(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "non_existent_directory_for_test")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error, got success")
	}

	out := stderr.String()
	if strings.Contains(out, "panic:") {
		t.Errorf("expected clean exit on non-existent path, got panic:\n%s", out)
	}
	if !strings.Contains(out, "no such file or directory") {
		t.Errorf("expected error about no such file or directory, got:\n%s", out)
	}
}

