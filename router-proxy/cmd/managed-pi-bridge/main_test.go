package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func captureStderr(t *testing.T, run func() int) (int, string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = writer
	code := run()
	_ = writer.Close()
	os.Stderr = original
	data, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	return code, string(data)
}

func TestRunMainRedactsMalformedFlagArguments(t *testing.T) {
	const secret = "/private/config-token-value"
	code, output := captureStderr(t, func() int {
		return runMain([]string{"--unknown=" + secret})
	})
	if code != 2 {
		t.Fatalf("exit code=%d", code)
	}
	if strings.Contains(output, secret) || strings.Contains(output, "flag provided but not defined") {
		t.Fatalf("flag parser leaked private input: %q", output)
	}
	if output != "managed bridge configuration is invalid\n" {
		t.Fatalf("unexpected error output: %q", output)
	}
}

func TestRunMainRedactsInvalidConfigPath(t *testing.T) {
	const secret = "/private/config-path"
	code, output := captureStderr(t, func() int {
		return runMain([]string{"--config=" + secret})
	})
	if code != 1 {
		t.Fatalf("exit code=%d", code)
	}
	if strings.Contains(output, secret) || output != "managed bridge configuration is invalid\n" {
		t.Fatalf("config error leaked private input: %q", output)
	}
}
