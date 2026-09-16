// Command managed-pi-bridge is the standalone Phase 1 managed RPC client.
// It is intentionally not wired into the router or supervisor.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managedbridge"
)

func runMain(args []string) int {
	flags := flag.NewFlagSet("managed-pi-bridge", flag.ContinueOnError)
	// Keep flag.Parse diagnostics fixed too; malformed arguments can contain
	// paths, tokens, or other private launch data.
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "mode-0600 bridge configuration")
	if err := flags.Parse(args); err != nil || *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "managed bridge configuration is invalid")
		return 2
	}
	bridge, err := managedbridge.NewBridgeFromFiles(*configPath, managedbridge.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "managed bridge configuration is invalid")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bridge.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "managed bridge runtime failed")
		return 1
	}
	return 0
}

func main() { os.Exit(runMain(os.Args[1:])) }
