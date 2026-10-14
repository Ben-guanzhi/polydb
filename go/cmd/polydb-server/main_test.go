package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":         slog.LevelInfo,
		"info":     slog.LevelInfo,
		"INFO":     slog.LevelInfo,
		"debug":    slog.LevelDebug,
		"warn":     slog.LevelWarn,
		"warning":  slog.LevelWarn,
		"error":    slog.LevelError,
		"nonsense": slog.LevelInfo, // unknown values fall back to info
	}
	for input, want := range cases {
		if got := parseLogLevel(input); got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", input, got, want)
		}
	}
}

// POLYDB_LOG_FILE routes logs to a file, creating the parent directory, and
// the level comes from POLYDB_LOG_LEVEL. AGENTS.md §15 wants the component
// name on every line, so the handler is built with a fixed `component` attr.
func TestNewLoggerHonoursLogFileAndLevel(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nested", "polydb.log")
	t.Setenv("POLYDB_LOG_LEVEL", "debug")
	t.Setenv("POLYDB_LOG_FILE", file)

	logw := newLogger()
	defer func() { _ = logw.Close() }()
	logw.Debug("probe", "route", "GET /api/health")
	if err := logw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("log file not written: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("log is not JSON: %v\n%s", err, data)
	}
	if entry["level"] != "DEBUG" {
		t.Errorf("level = %v, want DEBUG (POLYDB_LOG_LEVEL was honoured)", entry["level"])
	}
	if entry["msg"] != "probe" {
		t.Errorf("msg = %v", entry["msg"])
	}
	if entry["component"] != "polydb" {
		t.Errorf("component = %v, want polydb", entry["component"])
	}
}

// A logging misconfiguration must not take the server down: an unusable log
// path degrades to stderr and says so, instead of aborting the process.
func TestNewLoggerFallsBackToStderrWhenDirCannotBeCreated(t *testing.T) {
	// A regular file standing in for a directory makes MkdirAll fail while
	// still giving us a path that looks plausible.
	parent := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	t.Setenv("POLYDB_LOG_LEVEL", "info")
	t.Setenv("POLYDB_LOG_FILE", filepath.Join(parent, "nested", "x.log"))

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	logw := newLogger()
	os.Stdout, os.Stderr = oldOut, oldErr
	w.Close()
	defer func() { _ = logw.Close() }()

	out, _ := io.ReadAll(r)
	if len(out) == 0 {
		t.Fatal("no stderr diagnostic for the unusable log path")
	}
	if !strings.Contains(string(out), "falling back to stderr") {
		t.Errorf("diagnostic does not explain the fallback: %q", out)
	}
	// The returned logger is still fully usable after the fallback.
	logw.Info("probe")
}

// Close must be safe to call repeatedly and is a no-op when no file was opened
// (the stderr default), so the deferred call in main never fails.
func TestLogWriterCloseIsIdempotent(t *testing.T) {
	t.Setenv("POLYDB_LOG_LEVEL", "info")
	t.Setenv("POLYDB_LOG_FILE", "")

	logw := newLogger()
	if err := logw.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := logw.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
