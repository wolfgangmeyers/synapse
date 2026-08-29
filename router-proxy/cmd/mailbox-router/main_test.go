package main

import (
	"bufio"
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

func collectLines(reader io.Reader, lines chan<- string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		lines <- scanner.Text()
	}
}
func formatPort(port int) string { return strconv.Itoa(port) }
