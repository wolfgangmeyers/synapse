package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

const defaultListenHost = "0.0.0.0"

type config struct {
	upstreamHost string
	upstreamPort int
	listenHost   string
	listenPort   int
}

func parseConfig(args []string) (*config, error) {
	fs := flag.NewFlagSet("router-proxy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var upstreamHost string
	var upstreamPort int
	var listenHost string
	var listenPort int

	fs.StringVar(&upstreamHost, "upstream-host", "", "router host to connect to")
	fs.IntVar(&upstreamPort, "upstream-port", 0, "router port to connect to")
	fs.StringVar(&listenHost, "listen-host", defaultListenHost, "bind address for the proxy listener")
	fs.IntVar(&listenPort, "listen-port", 0, "port exposed on this machine")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unknown argument: %s", fs.Arg(0))
	}
	if upstreamHost == "" {
		return nil, errors.New("--upstream-host is required")
	}
	if upstreamPort < 1 || upstreamPort > 65535 {
		return nil, errors.New("--upstream-port must be an integer from 1 through 65535")
	}
	if listenPort < 1 || listenPort > 65535 {
		return nil, errors.New("--listen-port must be an integer from 1 through 65535")
	}
	if listenHost == "" {
		return nil, errors.New("--listen-host must not be empty")
	}

	return &config{
		upstreamHost: upstreamHost,
		upstreamPort: upstreamPort,
		listenHost:   listenHost,
		listenPort:   listenPort,
	}, nil
}

func usage() string {
	return "Usage: router-proxy --upstream-host <host> --upstream-port <port> --listen-port <port> [--listen-host 0.0.0.0]"
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println(usage())
		os.Exit(0)
	}

	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(usage())
			os.Exit(0)
		}
		log.Fatal(err)
	}

	proxy, err := newRouterProxy(cfg.upstreamHost, cfg.upstreamPort)
	if err != nil {
		log.Fatal(err)
	}

	addr := net.JoinHostPort(cfg.listenHost, strconv.Itoa(cfg.listenPort))
	server := &http.Server{
		Addr:              addr,
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
	}

	listenURL := proxy.listenURL(cfg.listenHost, cfg.listenPort)
	fmt.Printf("MECHA_ROUTER_PROXY_READY listen=%s upstream=%s\n", listenURL, proxy.upstreamURL())

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-sigCh:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Fatal(err)
		}
	}
}
