// Command mailbox-router runs the Go Mailbox V2 router. The legacy
// router-proxy command remains a separate binary and is not modified here.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
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
	durable, err = store.Open(ctx, options.database, store.Options{MaxBatchBodyChars: options.maxBatch})
	if err != nil {
		_, _ = attachmentsStore.Sweep(nil)
		log.Fatal(err)
	}
	logWriter, err := newLogWriter(options.logPath)
	if err != nil {
		_, _ = attachmentsStore.Sweep(nil)
		_ = durable.Close()
		log.Fatal(err)
	}
	defer logWriter.Close()
	logger := eventLogger(logWriter, os.Stderr)
	router, err := transport.New(transport.Options{Store: durable, Host: options.host, Port: options.port, Attachments: attachmentsStore, EventLogger: logger})
	if err != nil {
		_, _ = attachmentsStore.Sweep(nil)
		_ = durable.Close()
		log.Fatal(err)
	}
	if err := router.Listen(ctx); err != nil {
		_ = router.Close(context.Background())
		_ = durable.Close()
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
	if err := durable.Close(); err != nil {
		log.Print(err)
	}
}
