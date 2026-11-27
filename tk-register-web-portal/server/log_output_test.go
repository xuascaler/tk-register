package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiskLogsSurviveReopenAndRotate(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "logs", "server.log")
	first, err := newDiskLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write([]byte("before restart\n")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := newDiskLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	next.Compress = false
	var console bytes.Buffer
	if _, err := (diskAndConsoleLog{disk: next, console: &console}).Write([]byte("after restart\n")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filename)
	if err != nil || string(content) != "before restart\nafter restart\n" || console.String() != "after restart\n" {
		t.Fatal("logs did not persist across reopening")
	}
	if err := next.Rotate(); err != nil {
		t.Fatal(err)
	}
	if _, err := next.Write([]byte("after rotation\n")); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(filename), "server-*.log"))
	if err != nil || len(backups) != 1 {
		t.Fatal("missing rotated backup")
	}
	saved, err := os.ReadFile(backups[0])
	if err != nil || string(saved) != string(content) {
		t.Fatal("rotation lost original entries")
	}
	for _, path := range append(backups, filename) {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("log permissions are not private")
		}
	}
}

type failingDiskLog struct{}

func (failingDiskLog) Write([]byte) (int, error) { return 0, errors.New("secret filesystem error") }
func TestDiskLogFailureStillWritesConsole(t *testing.T) {
	var console bytes.Buffer
	_, err := (diskAndConsoleLog{disk: failingDiskLog{}, console: &console}).Write([]byte("diagnostic entry\n"))
	if err == nil || !strings.Contains(console.String(), "TK_REGISTER_LOG_WRITE_FAILED") || !strings.Contains(console.String(), "diagnostic entry") || strings.Contains(console.String(), "secret") {
		t.Fatal("disk failure hid diagnostics or exposed error text")
	}
}
