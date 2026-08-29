// Command mailbox-router runs the Go Mailbox V2 router. The legacy
// router-proxy command remains a separate binary and is not modified here.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
)

type config struct {
	database string
	logPath  string
	host     string
	port     int
	maxBatch int
}

func parseConfig(args []string) (config, error) {
	flags := flag.NewFlagSet("mailbox-router", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var result config
	flags.StringVar(&result.database, "database", "", "persistent SQLite database path")
	flags.StringVar(&result.logPath, "log", "", "JSONL event log path")
	flags.StringVar(&result.host, "host", transport.DefaultHost, "bind host")
	flags.IntVar(&result.port, "port", transport.DefaultPort, "listen port")
	flags.IntVar(&result.maxBatch, "max-batch-chars", 4096, "maximum UTF-16 characters in a delivery batch")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if result.database == "" {
		return config{}, errors.New("--database is required")
	}
	if result.logPath == "" {
		result.logPath = result.database + ".jsonl"
	}
	if result.host != "127.0.0.1" && result.host != "::1" && result.host != "0.0.0.0" {
		return config{}, errors.New("--host must be 127.0.0.1, ::1, or 0.0.0.0")
	}
	if result.port < 1 || result.port > 65535 {
		return config{}, errors.New("--port must be an integer from 1 through 65535")
	}
	if result.maxBatch < 0 {
		return config{}, errors.New("--max-batch-chars must be a non-negative integer")
	}
	return result, nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println("Usage: mailbox-router --database <sqlite-path> [--log <jsonl-path>] [--host 127.0.0.1|::1|0.0.0.0] [--port 8765] [--max-batch-chars <chars>]")
		return
	}
	options, err := parseConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	var durable *store.Store
	attachmentsStore, err := attachments.New(attachments.Options{
		Directory: options.database + ".attachments",
		Referenced: func(id string) bool {
			if durable == nil {
				return false
			}
			referenced, _ := durable.ConversationAttachmentReferenced(context.Background(), id)
			return referenced
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer attachmentsStore.Sweep(nil)
	durable, err = store.Open(ctx, options.database, store.Options{MaxBatchBodyChars: options.maxBatch})
	if err != nil {
		log.Fatal(err)
	}
	defer durable.Close()
	logWriter, err := newLogWriter(options.logPath)
	if err != nil {
		log.Fatal(err)
	}
	defer logWriter.Close()
	logger := func(event string, fields map[string]any) { _ = logWriter.Log(event, fields) }
	router, err := transport.New(transport.Options{Store: durable, Host: options.host, Port: options.port, Attachments: attachmentsStore, EventLogger: logger})
	if err != nil {
		log.Fatal(err)
	}
	if err := router.Listen(ctx); err != nil {
		log.Fatal(err)
	}
	httpURL, websocketURL := router.URLs()
	fmt.Printf("MECHA_V2_ROUTER_READY http=%s ws=%s database=%s log=%s\n", httpURL, websocketURL, options.database, options.logPath)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	sig := <-signals
	fmt.Printf("MECHA_V2_ROUTER_STOP signal=%s\n", sig)
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := router.Close(shutdown); err != nil {
		log.Print(err)
	}
}

type logWriter struct {
	file *os.File
	lock chan struct{}
}

func newLogWriter(path string) (*logWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &logWriter{file: file, lock: make(chan struct{}, 1)}, nil
}
func (w *logWriter) Log(event string, fields map[string]any) error {
	w.lock <- struct{}{}
	defer func() { <-w.lock }()
	record := map[string]any{"event": event, "timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
	for key, value := range fields {
		record[key] = value
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = w.file.Write(append(encoded, '\n'))
	return err
}
func (w *logWriter) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	return w.file.Close()
}
