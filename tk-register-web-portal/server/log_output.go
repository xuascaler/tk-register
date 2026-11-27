package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Each server process owns one file. Deployments bind its directory to the
// host so replacing the container does not remove historical diagnostics.
func newDiskLog(filename string) (*lumberjack.Logger, error) {
	if !filepath.IsAbs(filename) {
		return nil, errors.New("TK_REGISTER_LOG_FILE must be an absolute path")
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, errors.New("could not create TK log directory")
	}
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("could not open TK log file")
	}
	permissionErr := file.Chmod(0600)
	closeErr := file.Close()
	if permissionErr != nil || closeErr != nil {
		return nil, errors.New("could not initialize TK log file")
	}
	return &lumberjack.Logger{Filename: filename, MaxSize: 50, MaxBackups: 20, MaxAge: 30, Compress: true}, nil
}

type diskAndConsoleLog struct {
	disk    io.Writer
	console io.Writer
}

func (w diskAndConsoleLog) Write(p []byte) (int, error) {
	n, err := w.disk.Write(p)
	// Always keep container diagnostics available, including on a full disk. Do
	// not recursively log or expose filesystem error text in this fallback.
	if err != nil {
		_, _ = fmt.Fprintln(w.console, "TK_REGISTER_LOG_WRITE_FAILED: disk logging unavailable")
	}
	consoleN, consoleErr := w.console.Write(p)
	if err != nil {
		return consoleN, err
	}
	if consoleErr != nil {
		return n, consoleErr
	}
	return n, nil
}

func configureLogOutput() (func(), error) {
	filename := os.Getenv("TK_REGISTER_LOG_FILE")
	if filename == "" {
		return func() {}, nil
	}
	disk, err := newDiskLog(filename)
	if err != nil {
		return nil, err
	}
	previous := log.Writer()
	log.SetOutput(diskAndConsoleLog{disk: disk, console: previous})
	return func() { log.SetOutput(previous); _ = disk.Close() }, nil
}
