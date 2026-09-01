package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseConfigUsesIsolatedRouterDefaults(t *testing.T) {
	parsed, err := parseConfig([]string{"--database", "/tmp/router.db"})
	require.NoError(t, err)
	require.Equal(t, "/tmp/router.db.jsonl", parsed.logPath)
	require.Equal(t, "127.0.0.1", parsed.host)
	require.Equal(t, 8765, parsed.port)
}

func TestCommandStartsWithDistinctDatabaseLogAttachmentsAndPort(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "go-router.db")
	logPath := filepath.Join(directory, "go-router.jsonl")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())

	binary := filepath.Join(directory, "mailbox-router")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Dir(filepath.Join(".", "main.go"))
	require.NoError(t, build.Run())
	command := exec.Command(binary, "--database", database, "--log", logPath, "--host", "127.0.0.1", "--port", formatPort(port))
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	stderr, err := command.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	defer func() {
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()

	lines := make(chan string, 32)
	go collectLines(stdout, lines)
	go collectLines(stderr, lines)
	var ready string
	deadline := time.After(30 * time.Second)
	for ready == "" {
		select {
		case line := <-lines:
			if strings.HasPrefix(line, "MECHA_V2_ROUTER_READY ") {
				ready = line
			}
		case <-deadline:
			t.Fatal("timed out waiting for isolated Go router readiness")
		}
	}
	require.Contains(t, ready, "database="+database)
	require.Contains(t, ready, "log="+logPath)
	require.FileExists(t, database)
	attachmentInfo, err := os.Stat(database + ".attachments")
	require.NoError(t, err)
	require.True(t, attachmentInfo.IsDir())
	require.FileExists(t, logPath)

	response, err := http.Get("http://127.0.0.1:" + formatPort(port) + "/v1/health")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_, _ = io.Copy(io.Discard, response.Body)
	require.NoError(t, response.Body.Close())

	require.NoError(t, command.Process.Signal(syscall.SIGTERM))
	require.NoError(t, command.Wait())
	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(logBytes), `"event":"router_stopped"`)
}

func TestEventLogRotatesAfterOneDay(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, os.Chtimes(path, current, current))
	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()
	require.NoError(t, writer.Log("first", nil))
	current = current.Add(logRotationAge)
	require.NoError(t, writer.Log("second", nil))

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	var rotated []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "router.jsonl.") {
			rotated = append(rotated, entry.Name())
		}
	}
	require.Len(t, rotated, 1)
	rotatedBytes, err := os.ReadFile(filepath.Join(directory, rotated[0]))
	require.NoError(t, err)
	require.Contains(t, string(rotatedBytes), `"event":"first"`)
	activeBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(activeBytes), `"event":"second"`)
}

func TestEventLogRefreshesGenerationTimestampAfterRotation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()

	require.NoError(t, writer.Log("first", nil))
	current = current.Add(logRotationAge)
	require.NoError(t, writer.Log("second", nil))
	current = current.Add(time.Hour)
	require.NoError(t, writer.Log("third", nil))

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	var rotated []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "router.jsonl.") {
			rotated = append(rotated, entry.Name())
		}
	}
	require.Len(t, rotated, 1)

	current = current.Add(logRotationAge - time.Hour)
	require.NoError(t, writer.Log("fourth", nil))
	entries, err = os.ReadDir(directory)
	require.NoError(t, err)
	rotated = rotated[:0]
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "router.jsonl.") {
			rotated = append(rotated, entry.Name())
		}
	}
	require.Len(t, rotated, 2)

	var retained strings.Builder
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "router.jsonl") {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		require.NoError(t, readErr)
		retained.Write(contents)
	}
	require.Contains(t, retained.String(), `"event":"second"`)
	require.Contains(t, retained.String(), `"event":"third"`)
	require.Contains(t, retained.String(), `"event":"fourth"`)
}

func TestEventLogDropsActiveRecordsOlderThanRetention(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	old := current.Add(-logRetentionAge - time.Hour)
	oldRecord := fmt.Sprintf(`{"event":"old","timestamp":%q}`+"\n", old.Format("2006-01-02T15:04:05.000Z"))
	require.NoError(t, os.WriteFile(path, []byte(oldRecord), 0o600))
	require.NoError(t, os.Chtimes(path, old, old))
	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()
	require.NoError(t, writer.Log("current", nil))
	activeBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(activeBytes), `"event":"old"`)
	require.Contains(t, string(activeBytes), `"event":"current"`)
}

func TestEventLogRemovesRotationsOlderThanRetention(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	oldPath := path + "." + current.Add(-logRetentionAge-time.Hour).Format(rotationLayout)
	newPath := path + "." + current.Add(-logRetentionAge+time.Hour).Format(rotationLayout)
	require.NoError(t, os.WriteFile(oldPath, []byte("old\n"), 0o600))
	require.NoError(t, os.WriteFile(newPath, []byte("new\n"), 0o600))

	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()
	require.NoError(t, writer.Log("current", nil))
	_, err = os.Stat(oldPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(newPath)
	require.NoError(t, err)
}

func TestEventLogBoundsEachRetainedFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.jsonl")
	current := time.Now().UTC().Truncate(time.Millisecond)
	writer, err := newLogWriterAt(path, func() time.Time { return current })
	require.NoError(t, err)
	defer writer.Close()
	payload := strings.Repeat("x", maxLogBytes/2-128)
	require.NoError(t, writer.Log("first", map[string]any{"payload": payload}))
	require.NoError(t, writer.Log("second", map[string]any{"payload": payload}))

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "router.jsonl") {
			continue
		}
		info, statErr := entry.Info()
		require.NoError(t, statErr)
		require.LessOrEqual(t, info.Size(), int64(maxLogBytes), entry.Name())
	}
}

func TestEventLoggerReportsWriteFailureWithoutRecursing(t *testing.T) {
	writer, err := newLogWriter(filepath.Join(t.TempDir(), "router.jsonl"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	var output strings.Builder
	eventLogger(writer, &output)("router_stopped", nil)
	require.Contains(t, output.String(), "event log write failed")
}

func collectLines(reader io.Reader, lines chan<- string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		lines <- scanner.Text()
	}
}
func formatPort(port int) string { return strconv.Itoa(port) }
