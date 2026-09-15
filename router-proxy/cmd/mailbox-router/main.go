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
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
)

// These values are set by the deployment Make target. Keeping the identity in
// the executable makes health a proof of the listener's source revision rather
// than a claim about whichever checkout happens to be on disk.
var (
	buildSourceCommit = "unknown"
	buildSourceTree   = "unknown"
)

type config struct {
	database              string
	logPath               string
	host                  string
	port                  int
	maxBatch              int
	auditSupervisorTarget string
	auditTmuxSession      string
	policyTimeout         time.Duration
	managedUIDist         string
	managed               managed.Config
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
	flags.StringVar(&result.auditSupervisorTarget, "audit-supervisor-target", "", "supervisor agent ID for tmux/router audit delivery (must be paired with --audit-tmux-session)")
	flags.StringVar(&result.auditTmuxSession, "audit-tmux-session", "", "tmux session for read-only tmux/router audit inventory (must be paired with --audit-supervisor-target)")
	flags.DurationVar(&result.policyTimeout, "policy-timeout", transport.DefaultPolicyTimeout, "maximum duration for one native policy run")
	flags.StringVar(&result.managedUIDist, "managed-ui-dist", "", "built React UI directory served by the router")
	var managedEnabled bool
	flags.BoolVar(&managedEnabled, "managed", true, "enable router-managed Pi launches (set false to preserve legacy-only routing)")
	var managedPi, managedHome, managedPiAgentDir, managedSession, managedManifest, managedExtension, managedSubagents, managedCursorExtension, managedAuthHelper, managedRuntimeRoot, managedRoots, managedRecipient string
	flags.StringVar(&managedPi, "managed-pi", "", "installed Pi executable")
	flags.StringVar(&managedHome, "managed-home", "", "managed Pi HOME root")
	flags.StringVar(&managedPiAgentDir, "managed-pi-agent-dir", "", "existing ordinary Pi agent directory")
	flags.StringVar(&managedSession, "managed-session-root", "", "managed Pi session root")
	flags.StringVar(&managedManifest, "managed-manifest", "", "managed tool manifest")
	flags.StringVar(&managedExtension, "managed-mecha-rpc", "", "fixed mecha-rpc extension")
	flags.StringVar(&managedSubagents, "managed-pi-subagents", "", "checked pi-subagents extension")
	flags.StringVar(&managedCursorExtension, "managed-cursor-extension", "", "fixed pi-cursor-sdk extension")
	flags.StringVar(&managedAuthHelper, "managed-auth-helper", "", "fixed provider auth helper entry")
	flags.StringVar(&managedRuntimeRoot, "managed-runtime-root", "", "checked project runtime root")
	flags.StringVar(&managedRoots, "managed-cwd-roots", "", "comma-separated allowed working-directory roots (defaults to the current user home)")
	flags.StringVar(&managedRecipient, "managed-quiet-recipient", "", "canonical quiet recipient")
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
	managedRouterURL, err := managed.ManagedRouterURLForListener(result.host, result.port)
	if err != nil {
		return config{}, err
	}
	if result.maxBatch < 0 {
		return config{}, errors.New("--max-batch-chars must be a non-negative integer")
	}
	if (result.auditSupervisorTarget == "") != (result.auditTmuxSession == "") {
		return config{}, errors.New("--audit-supervisor-target and --audit-tmux-session must be provided together")
	}
	if result.auditSupervisorTarget != "" && result.auditTmuxSession != "" {
		if strings.TrimSpace(result.auditTmuxSession) == "" {
			return config{}, errors.New("--audit-tmux-session is required")
		}
	}
	if result.policyTimeout <= 0 {
		return config{}, errors.New("--policy-timeout must be positive")
	}
	roots := parseManagedRoots(managedRoots)
	if len(roots) == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return config{}, fmt.Errorf("managed working-directory home is unavailable: %w", err)
		}
		roots = []string{home}
	}
	result.managed = managed.Config{Enabled: managedEnabled, PiExecutable: managedPi, ManagedHome: managedHome, PiAgentDir: managedPiAgentDir, SessionRoot: managedSession, RouterURL: managedRouterURL, ManifestPath: managedManifest, MechaRPCExtension: managedExtension, PiSubagentsExtension: managedSubagents, CursorExtension: managedCursorExtension, AuthHelperPath: managedAuthHelper, RuntimeRoot: managedRuntimeRoot, RequireProviderRuntime: managedEnabled, AllowedWorkingDirectoryRoots: roots, CanonicalQuietRecipientID: managedRecipient}
	return result, nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println("Usage: mailbox-router --database <sqlite-path> [--log <jsonl-path>] [--host 127.0.0.1|::1|0.0.0.0] [--port 8765] [--managed=true|false] [managed launch flags]")
		return
	}
	options, err := parseConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	if err := validateManagedStartup(options.managed); err != nil {
		log.Fatal(err)
	}
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
	routerLogReader, err := transport.NewConfiguredRouterLogReader(options.logPath)
	if err != nil {
		_, _ = attachmentsStore.Sweep(nil)
		_ = durable.Close()
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
	var managedManager *managed.Manager
	var providerAuth *managed.ProviderAuthCoordinator
	if options.managed.Enabled {
		if err := durable.SetCanonicalQuietRecipient(ctx, options.managed.CanonicalQuietRecipientID); err != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(err)
		}
		manifest, _, manifestErr := managed.ValidateManifestFile(options.managed.ManifestPath, "")
		if manifestErr != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(manifestErr)
		}
		rootExecutor, rootErr := newMailboxRouterRootControlExecutor(options.managed)
		if rootErr != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(rootErr)
		}
		rootHandlers, rootErr := managed.NewOperationalRootControlHandlers(manifest, rootExecutor)
		if rootErr != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(rootErr)
		}
		managedManager, err = managed.NewManager(managed.ManagerOptions{Store: durable, Config: options.managed, Manifest: manifest, ProcessFactory: managedProcessFactory(), RPCFactory: managedRPCFactory(), CatalogSource: managed.StoreCatalogSource{Store: durable}, RootControlHandlers: rootHandlers})
		if err != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(err)
		}
		nodeExecutable, nodeErr := exec.LookPath("node")
		if nodeErr != nil || !filepath.IsAbs(nodeExecutable) {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal("managed provider helper requires an absolute Node executable")
		}
		providerAuth, err = managed.NewProviderAuthCoordinator(durable, managedManager, fixedAuthHelper{config: options.managed, nodeExecutable: nodeExecutable})
		if err != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(err)
		}
		// Provider settings starts from ordinary Pi's current local auth. This
		// records only the bounded status/catalog projection; credentials never
		// enter router storage or logs.
		if _, err := providerAuth.Snapshot(ctx); err != nil {
			log.Print("managed provider status is unavailable")
		}
	}
	router, err := transport.New(transport.Options{Store: durable, Host: options.host, Port: options.port, Attachments: attachmentsStore, EventLogger: logger, Tmux: newTmuxAdapter("tmux"), EnableTmuxRouterAudit: options.auditSupervisorTarget != "" && options.auditTmuxSession != "", AuditSupervisorTarget: options.auditSupervisorTarget, AuditTmuxSession: options.auditTmuxSession, PolicyTimeout: options.policyTimeout, Managed: managedManager, ProviderAuth: providerAuth, StaticUIRoot: options.managedUIDist, RuntimeIdentity: transport.RuntimeIdentity{SourceCommit: buildSourceCommit, SourceTree: buildSourceTree}, RouterLogReader: routerLogReader})
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
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	httpURL, websocketURL := router.URLs()
	fmt.Printf("MECHA_V2_ROUTER_READY http=%s ws=%s database=%s log=%s\n", httpURL, websocketURL, options.database, options.logPath)

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
