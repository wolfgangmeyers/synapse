// Command managed-acceptance-router is an isolated test harness for the
// managed-RPC browser release runner. It is intentionally separate from the
// mailbox-router production CLI so probe extensions and a deterministic local
// model never widen ordinary managed launches.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
)

type acceptanceConfig struct {
	database, logPath, host string
	port                    int
	pi, home, sessions      string
	manifest, mechaRPC      string
	subagents, probe        string
	cwdRoot, providerURL    string
	staticUIRoot            string
	provider, model         string
	fixtureMode             string
}

func parseConfig(args []string) (acceptanceConfig, error) {
	flags := flag.NewFlagSet("managed-acceptance-router", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var result acceptanceConfig
	flags.StringVar(&result.database, "database", "", "isolated SQLite database")
	flags.StringVar(&result.logPath, "log", "", "isolated JSONL log")
	flags.StringVar(&result.host, "host", "127.0.0.1", "loopback bind host")
	flags.IntVar(&result.port, "port", 0, "loopback bind port")
	flags.StringVar(&result.pi, "managed-pi", "", "locked installed Pi executable")
	flags.StringVar(&result.home, "managed-home", "", "isolated managed HOME")
	flags.StringVar(&result.sessions, "managed-session-root", "", "isolated Pi session directory")
	flags.StringVar(&result.manifest, "managed-manifest", "", "managed tool manifest")
	flags.StringVar(&result.mechaRPC, "managed-mecha-rpc", "", "mecha RPC extension")
	flags.StringVar(&result.subagents, "managed-pi-subagents", "", "pi-subagents extension")
	flags.StringVar(&result.probe, "managed-probe-extension", "", "acceptance-only child compaction probe")
	flags.StringVar(&result.cwdRoot, "managed-cwd-root", "", "one permitted temporary working directory")
	flags.StringVar(&result.providerURL, "test-provider-url", "", "isolated deterministic OpenAI-compatible provider")
	flags.StringVar(&result.staticUIRoot, "static-ui-root", "", "built router UI directory served by this isolated router")
	flags.StringVar(&result.provider, "test-provider", "mecha-router-test", "provider configured only in the isolated HOME")
	flags.StringVar(&result.model, "test-model", "managed-rpc-release", "model configured only in the isolated HOME")
	flags.StringVar(&result.fixtureMode, "fixture-mode", "", "optional acceptance-only installed-Pi fixture mode")
	if err := flags.Parse(args); err != nil {
		return acceptanceConfig{}, err
	}
	if flags.NArg() != 0 {
		return acceptanceConfig{}, errors.New("managed acceptance router does not accept positional arguments")
	}
	if result.database == "" || result.logPath == "" || result.pi == "" || result.home == "" || result.sessions == "" || result.manifest == "" || result.mechaRPC == "" || result.subagents == "" || result.probe == "" || result.cwdRoot == "" || result.providerURL == "" || result.staticUIRoot == "" {
		return acceptanceConfig{}, errors.New("managed acceptance router requires all isolated launch inputs")
	}
	if result.fixtureMode != "" && result.fixtureMode != "parent-bootstrap-hold" && result.fixtureMode != "parent-matrix" {
		return acceptanceConfig{}, errors.New("managed acceptance router fixture mode is invalid")
	}
	if result.host != "127.0.0.1" || result.port < 1 || result.port > 65535 || result.port == 8765 {
		return acceptanceConfig{}, errors.New("managed acceptance router must bind a non-live explicit loopback port")
	}
	provider, err := url.Parse(result.providerURL)
	if err != nil || provider.Scheme != "http" || provider.Hostname() != "127.0.0.1" || provider.User != nil || provider.RawQuery != "" || provider.Fragment != "" {
		return acceptanceConfig{}, errors.New("managed acceptance router requires a loopback deterministic provider URL")
	}
	return result, nil
}

func validateStaticUIRoot(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("managed acceptance router requires a built static UI directory")
	}
	index, err := os.Lstat(filepath.Join(absolute, "index.html"))
	if err != nil || index.Mode()&os.ModeSymlink != 0 || !index.Mode().IsRegular() {
		return errors.New("managed acceptance router static UI must contain index.html")
	}
	return nil
}

func acceptanceLogger(path string) (func(string, map[string]any), func() error, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, nil, err
	}
	var lock sync.Mutex
	write := func(event string, fields map[string]any) {
		lock.Lock()
		defer lock.Unlock()
		line := map[string]any{"event": event, "at": time.Now().UTC().Format(time.RFC3339Nano)}
		for key, value := range fields {
			line[key] = value
		}
		encoded, marshalErr := json.Marshal(line)
		if marshalErr == nil {
			_, _ = file.Write(append(encoded, '\n'))
		}
	}
	return write, file.Close, nil
}

func main() {
	options, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx := context.Background()
	if err := validateStaticUIRoot(options.staticUIRoot); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	logger, closeLog, err := acceptanceLogger(options.logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer closeLog()
	durable, err := store.Open(ctx, options.database, store.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer durable.Close()
	attachmentStore, err := attachments.New(attachments.Options{Directory: options.database + ".attachments", Referenced: func(id string) bool {
		referenced, _ := durable.ConversationAttachmentReferenced(context.Background(), id)
		return referenced
	}})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	manifest, _, err := managed.ValidateManifestFile(options.manifest, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	testEnvironment := []string{"MECHA_TEST_BASE_URL=" + options.providerURL}
	if options.fixtureMode != "" {
		testEnvironment = append(testEnvironment, "MECHA_TEST_FIXTURE_MODE="+options.fixtureMode)
	}
	config := managed.Config{
		Enabled: true, PiExecutable: options.pi, ManagedHome: options.home, SessionRoot: options.sessions,
		ManifestPath: options.manifest, MechaRPCExtension: options.mechaRPC, PiSubagentsExtension: options.subagents,
		TestOnlyExtensions: []string{options.probe}, TestOnlyEnvironment: testEnvironment,
		AllowedWorkingDirectoryRoots: []string{options.cwdRoot}, CanonicalQuietRecipientID: "phase3-supervisor",
	}
	if err := managed.ValidateManagedLaunchConfig(config); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := durable.SetCanonicalQuietRecipient(ctx, config.CanonicalQuietRecipientID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable, Config: config, Manifest: manifest,
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: options.provider, ID: options.model, Name: "Managed RPC release fixture", Available: true}),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	router, err := transport.New(transport.Options{Store: durable, Host: options.host, Port: options.port, Attachments: attachmentStore, EventLogger: logger, Managed: manager, StaticUIRoot: options.staticUIRoot})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := router.Listen(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	httpURL, websocketURL := router.URLs()
	fmt.Printf("MECHA_V2_ROUTER_READY http=%s ws=%s database=%s log=%s\n", httpURL, websocketURL, options.database, options.logPath)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	signalValue := <-signals
	logger("router_stopped", map[string]any{"signal": signalValue.String()})
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := router.Close(shutdown); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	_, _ = attachmentStore.Sweep(nil)
}
