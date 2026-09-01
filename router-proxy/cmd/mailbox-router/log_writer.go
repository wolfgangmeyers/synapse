package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	logRotationAge  = 24 * time.Hour
	logRetentionAge = 48 * time.Hour
	// Match the supported JSONL logger's 16 MiB generation limit. The active
	// file and retained rotations together stay within two generations.
	maxLogBytes    = 16 * 1024 * 1024
	rotationLayout = "20060102T150405.000000000Z"
)

type logWriter struct {
	path     string
	now      func() time.Time
	file     *os.File
	openedAt time.Time
	mu       sync.Mutex
}

func newLogWriter(path string) (*logWriter, error) {
	return newLogWriterAt(path, time.Now)
}

func newLogWriterAt(path string, now func() time.Time) (*logWriter, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	openedAt := info.ModTime().UTC()
	if info.Size() > 0 {
		openedAt = oldestRecordTime(path, openedAt)
	}
	if openedAt.IsZero() {
		openedAt = now().UTC()
	}
	return &logWriter{path: path, now: now, file: file, openedAt: openedAt}, nil
}

// eventLogger keeps diagnostics best effort. Its error path writes directly to
// the process stderr and never emits another router event.
func eventLogger(writer *logWriter, errorWriter io.Writer) func(string, map[string]any) {
	return func(event string, fields map[string]any) {
		if err := writer.Log(event, fields); err != nil {
			_, _ = fmt.Fprintf(errorWriter, "mailbox-router: event log write failed: %v\n", err)
		}
	}
}

func (w *logWriter) Log(event string, fields map[string]any) error {
	if w == nil {
		return errors.New("event log writer is nil")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return errors.New("event log writer is closed")
	}

	now := w.now().UTC()
	record := map[string]any{"event": event, "timestamp": now.Format("2006-01-02T15:04:05.000Z")}
	for key, value := range fields {
		record[key] = value
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	line := append(encoded, '\n')
	if len(line) > maxLogBytes {
		return fmt.Errorf("event log record exceeds %d-byte limit", maxLogBytes)
	}
	if err := w.rotateIfNeeded(now, int64(len(line))); err != nil {
		return err
	}
	n, err := w.file.Write(line)
	if err != nil {
		return fmt.Errorf("write event log: %w", err)
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	return w.cleanup(now)
}

func (w *logWriter) rotateIfNeeded(now time.Time, incoming int64) error {
	info, err := w.file.Stat()
	if err != nil {
		return fmt.Errorf("stat event log: %w", err)
	}
	if info.Size() == 0 {
		w.openedAt = now
		return nil
	}
	if info.Size()+incoming <= maxLogBytes && now.Sub(w.openedAt) < logRotationAge {
		return nil
	}

	// A pre-existing log may have been written before the size limit existed.
	// Drop it before rotation so every retained file obeys the same bound.
	if info.Size() > maxLogBytes {
		if err := w.file.Truncate(0); err != nil {
			return fmt.Errorf("bound event log: %w", err)
		}
		if _, err := w.file.Seek(0, io.SeekEnd); err != nil {
			return fmt.Errorf("seek bounded event log: %w", err)
		}
		w.openedAt = now
		return nil
	}
	return w.rotate(now)
}

func (w *logWriter) rotate(now time.Time) error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close event log for rotation: %w", err)
	}
	w.file = nil
	rotated, err := w.uniqueRotationPath(now)
	if err != nil {
		return err
	}
	if err := os.Rename(w.path, rotated); err != nil {
		file, reopenErr := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		w.file = file
		if reopenErr != nil {
			return fmt.Errorf("rotate event log: %w (reopen: %v)", err, reopenErr)
		}
		return fmt.Errorf("rotate event log: %w", err)
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open rotated event log: %w", err)
	}
	w.file = file
	w.openedAt = now.UTC()
	return nil
}

func (w *logWriter) uniqueRotationPath(now time.Time) (string, error) {
	base := w.path + "." + now.UTC().Format(rotationLayout)
	candidate := base
	for index := 1; ; index++ {
		_, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("check event log rotation path: %w", err)
		}
		candidate = fmt.Sprintf("%s-%d", base, index)
	}
}

type rotatedLog struct {
	path string
	when time.Time
	size int64
}

func (w *logWriter) cleanup(now time.Time) error {
	entries, err := os.ReadDir(filepath.Dir(w.path))
	if err != nil {
		return fmt.Errorf("list event log rotations: %w", err)
	}
	prefix := filepath.Base(w.path) + "."
	cutoff := now.Add(-logRetentionAge)
	rotated := make([]rotatedLog, 0)
	var firstErr error
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		when, ok := rotationTime(name[len(prefix):])
		if !ok {
			continue
		}
		path := filepath.Join(filepath.Dir(w.path), name)
		info, statErr := entry.Info()
		if statErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("stat rotated event log %q: %w", path, statErr)
			}
			continue
		}
		if !when.After(cutoff) {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && firstErr == nil {
				firstErr = fmt.Errorf("remove expired event log %q: %w", path, removeErr)
			}
			continue
		}
		rotated = append(rotated, rotatedLog{path: path, when: when, size: info.Size()})
	}

	// Keep the active file plus at most one active-file-sized set of rotated
	// records. This also bounds growth when a busy router rotates by size.
	sort.Slice(rotated, func(i, j int) bool { return rotated[i].when.After(rotated[j].when) })
	var retained int64
	for _, item := range rotated {
		if retained+item.size <= maxLogBytes {
			retained += item.size
			continue
		}
		if removeErr := os.Remove(item.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && firstErr == nil {
			firstErr = fmt.Errorf("bound rotated event logs: %w", removeErr)
		}
	}
	return firstErr
}

func oldestRecordTime(path string, fallback time.Time) time.Time {
	file, err := os.Open(path)
	if err != nil {
		return fallback
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxLogBytes)
	if !scanner.Scan() {
		return fallback
	}
	var record struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Timestamp == "" {
		return fallback
	}
	when, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		return fallback
	}
	return when.UTC()
}

func rotationTime(suffix string) (time.Time, bool) {
	if dash := strings.LastIndexByte(suffix, '-'); dash >= 0 {
		suffix = suffix[:dash]
	}
	when, err := time.Parse(rotationLayout, suffix)
	return when, err == nil
}

func (w *logWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
