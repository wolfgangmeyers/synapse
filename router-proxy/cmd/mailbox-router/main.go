// Command mailbox-router runs the Go Mailbox V2 router. The legacy
// router-proxy command remains a separate binary and is not modified here.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/codexdevice"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
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
	codexCredentialPath   string
	embeddedWorkerDefault bool
	testClock             bool
	managed               managed.Config
	embeddedRoot          embeddedRootConfig
}

type embeddedRootConfig struct {
	Enabled            bool
	RequestID          string
	Name               string
	Cwd                string
	Model              managed.Model
	InitialInstruction string
}

var embeddedRootID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// This command is the sole source of root authority. Never synthesize an ID or
// fill missing fields from HTTP state or the manager's default model.
func launchConfiguredEmbeddedRoot(ctx context.Context, options config, manager *managed.Manager) error {
	if !options.embeddedRoot.Enabled {
		return nil
	}
	if manager == nil || !options.managed.Enabled {
		return errors.New("embedded root bootstrap requires managed mode")
	}
	root := options.embeddedRoot
	_, err := manager.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{
		RequestID: root.RequestID, Name: root.Name, Cwd: root.Cwd,
		Runtime: "embedded", Model: &root.Model, InitialInstruction: root.InitialInstruction,
	})
	return err
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
	flags.StringVar(&result.codexCredentialPath, "codex-credential-path", "", "explicit private local Codex credential file (managed mode only)")
	flags.BoolVar(&result.embeddedWorkerDefault, "embedded-worker-default", false, "default bare managed HTTP launches to embedded workers (requires private Codex credential)")
	flags.BoolVar(&result.testClock, "test-clock", false, "enable deterministic fixed test clock with /v1/test/clock/advance endpoint")
	var managedEnabled bool
	flags.BoolVar(&managedEnabled, "managed", true, "enable router-managed Pi launches (set false to preserve legacy-only routing)")
	flags.BoolVar(&result.embeddedRoot.Enabled, "embedded-root-bootstrap", false, "reserve the explicitly configured embedded root before readiness")
	flags.StringVar(&result.embeddedRoot.RequestID, "embedded-root-request-id", "", "stable root launch request UUID")
	flags.StringVar(&result.embeddedRoot.Name, "embedded-root-name", "", "root agent name")
	flags.StringVar(&result.embeddedRoot.Cwd, "embedded-root-cwd", "", "absolute allowed root working directory")
	flags.StringVar(&result.embeddedRoot.Model.Provider, "embedded-root-model-provider", "", "root model provider")
	flags.StringVar(&result.embeddedRoot.Model.ID, "embedded-root-model-id", "", "root model ID")
	flags.StringVar(&result.embeddedRoot.InitialInstruction, "embedded-root-initial-instruction", "", "root initial instruction")
	var managedPi, managedHome, managedPiAgentDir, managedSession, managedManifest, managedExtension, managedSubagents, managedCursorExtension, managedAuthHelper, managedRuntimeRoot, managedRoots, managedRecipient string
	var allowManagedLaunchRecipeMismatch bool
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
	flags.BoolVar(&allowManagedLaunchRecipeMismatch, "managed-allow-launch-recipe-mismatch", false, "allow recovery when the current launch recipe SHA differs from the stored recipe")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if os.Getenv("MECHA_TEST_CLOCK") == "true" || os.Getenv("MECHA_TEST_CLOCK") == "1" {
		result.testClock = true
	}
	if flags.NArg() != 0 {
		return config{}, errors.New("unexpected positional argument")
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
	root := result.embeddedRoot
	rootFieldsPresent := root.RequestID != "" || root.Name != "" || root.Cwd != "" || root.Model.Provider != "" || root.Model.ID != "" || root.InitialInstruction != ""
	if root.Enabled {
		if !managedEnabled {
			return config{}, errors.New("embedded root bootstrap requires managed mode")
		}
		if result.codexCredentialPath == "" {
			return config{}, errors.New("embedded root bootstrap requires --codex-credential-path")
		}
		if !embeddedRootID.MatchString(root.RequestID) || strings.TrimSpace(root.Name) == "" || !filepath.IsAbs(root.Cwd) || strings.TrimSpace(root.Model.Provider) == "" || strings.TrimSpace(root.Model.ID) == "" || strings.TrimSpace(root.InitialInstruction) == "" {
			return config{}, errors.New("embedded root bootstrap requires request ID, name, absolute cwd, model provider and ID, and initial instruction")
		}
	} else if rootFieldsPresent {
		return config{}, errors.New("embedded root fields require --embedded-root-bootstrap=true")
	}
	if result.codexCredentialPath != "" && !managedEnabled {
		return config{}, errors.New("--codex-credential-path requires managed mode")
	}
	if result.embeddedWorkerDefault && (!managedEnabled || result.codexCredentialPath == "") {
		return config{}, errors.New("--embedded-worker-default requires managed mode and --codex-credential-path")
	}
	result.managed = managed.Config{Enabled: managedEnabled, AllowManagedLaunchRecipeMismatch: allowManagedLaunchRecipeMismatch, PiExecutable: managedPi, ManagedHome: managedHome, PiAgentDir: managedPiAgentDir, SessionRoot: managedSession, RouterURL: managedRouterURL, ManifestPath: managedManifest, MechaRPCExtension: managedExtension, PiSubagentsExtension: managedSubagents, CursorExtension: managedCursorExtension, AuthHelperPath: managedAuthHelper, RuntimeRoot: managedRuntimeRoot, RequireProviderRuntime: managedEnabled, AllowedWorkingDirectoryRoots: roots, CanonicalQuietRecipientID: managedRecipient}
	return result, nil
}

// The transport option's historical name refers to bare HTTP launch defaults,
// not root authority. Only LaunchEmbeddedRoot can reserve a root role.
func embeddedWorkerDefaultEnabled(options config) bool {
	return options.embeddedWorkerDefault
}

// configureCodexCredentials fails closed before manager construction or listener binding.
// The local file is the only credential source for this opt-in path.
func configureCodexCredentials(ctx context.Context, options config, managerOptions *managed.ManagerOptions) error {
	if options.codexCredentialPath == "" {
		return nil
	}
	if !options.managed.Enabled {
		return errors.New("Codex credential path requires managed mode")
	}
	credentials, err := store.OpenLocalCodexCredentialStore(options.codexCredentialPath)
	if err != nil {
		return fmt.Errorf("open local Codex credential: %w", err)
	}
	credential, err := credentials.Load(ctx)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load local Codex credential: %w", err)
	}
	// An empty protected store is signed out, not a startup failure: the
	// unchanged browser login must be able to populate it.
	if err == nil && (credential.AccountID == "" || credential.ExpiresAt.IsZero()) {
		return errors.New("local Codex credential is incomplete")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	managerOptions.CodexCredentialStore = credentials
	managerOptions.CodexRefresh = func(ctx context.Context, credential store.CodexCredential) (store.CodexCredential, error) {
		return codexdevice.Refresh(ctx, client, credential)
	}
	return nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "codex-device-login" {
		if err := productionCodexDeviceLogin(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println("Usage: mailbox-router --database <sqlite-path> [--log <jsonl-path>] [--host 127.0.0.1|::1|0.0.0.0] [--port 8765] [--managed=true|false] [managed launch flags]")
		return
	}
	options, err := parseConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	// The tagged observer must own its private output before credentials, DB,
	// or any managed Router work can start. Untagged builds do nothing here.
	if err := managed.PreparePrivateCodexObserver(); err != nil {
		log.Fatal("private Codex observer setup failed")
	}
	// CLI parsing predates observer preparation. Invalid CLI exits leave CLI
	// UNKNOWN; never fabricate an entered/result record after the boundary.
	ctx := context.Background()
	codexOptions := managed.ManagerOptions{}
	if err := startupRun(stageCredential, func() error {
		return configureCodexCredentials(ctx, options, &codexOptions)
	}); err != nil {
		log.Fatal(err)
	}
	if err := startupRun(stageConfig, func() error {
		if err := configureEmbeddedRestartFake(options, &codexOptions); err != nil {
			return err
		}
		return validateManagedStartup(options.managed)
	}); err != nil {
		log.Fatal(err)
	}
	if err := startupEntered(stageStoreManifest); err != nil {
		log.Fatal(err)
	}
	var testClock *deterministic.FixedClock
	if options.testClock {
		testClock = deterministic.NewFixedClock(time.Now())
	}
	var attachmentsClock attachments.Clock
	if testClock != nil {
		attachmentsClock = testClock
	}
	var durable *store.Store
	attachmentsStore, err := attachments.New(attachments.Options{
		Directory: options.database + ".attachments",
		Clock:     attachmentsClock,
		Referenced: func(id string) bool {
			if durable == nil {
				return false
			}
			referenced, _ := durable.ConversationAttachmentReferenced(context.Background(), id)
			return referenced
		},
	})
	if err != nil {
		log.Fatal(startupResult(stageStoreManifest, err))
	}
	storeOpts := store.Options{MaxBatchBodyChars: options.maxBatch}
	if testClock != nil {
		storeOpts.Clock = testClock
	}
	durable, err = store.Open(ctx, options.database, storeOpts)
	if err != nil {
		_, _ = attachmentsStore.Sweep(nil)
		log.Fatal(startupResult(stageStoreManifest, err))
	}
	routerLogReader, err := transport.NewConfiguredRouterLogReader(options.logPath)
	if err != nil {
		_, _ = attachmentsStore.Sweep(nil)
		_ = durable.Close()
		log.Fatal(startupResult(stageStoreManifest, err))
	}
	logWriter, err := newLogWriter(options.logPath)
	if err != nil {
		_, _ = attachmentsStore.Sweep(nil)
		_ = durable.Close()
		log.Fatal(startupResult(stageStoreManifest, err))
	}
	defer logWriter.Close()
	logger := eventLogger(logWriter, os.Stderr)
	managedLogger := managedEventLogger(logWriter, os.Stderr)
	var managedManager *managed.Manager
	var providerAuth *managed.ProviderAuthCoordinator
	if options.managed.Enabled {
		if err := durable.SetCanonicalQuietRecipient(ctx, options.managed.CanonicalQuietRecipientID); err != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(startupResult(stageStoreManifest, err))
		}
		manifest, _, manifestErr := managed.ValidateManifestFile(options.managed.ManifestPath, "")
		if manifestErr != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(startupResult(stageStoreManifest, manifestErr))
		}
		if err := startupResult(stageStoreManifest, nil); err != nil {
			log.Fatal(err)
		}
		if err := startupEntered(stageHelper); err != nil {
			log.Fatal(err)
		}
		rootExecutor, rootErr := newMailboxRouterRootControlExecutor(options.managed)
		if rootErr != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(startupResult(stageHelper, rootErr))
		}
		rootHandlers, rootErr := managed.NewOperationalRootControlHandlers(manifest, rootExecutor)
		if rootErr != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(startupResult(stageHelper, rootErr))
		}
		codexOptions.Attachments = attachmentsStore
		codexOptions.Store = durable
		codexOptions.EventSink = managedLogger
		codexOptions.Config = options.managed
		codexOptions.Manifest = manifest
		codexOptions.ProcessFactory = managedProcessFactory()
		codexOptions.RPCFactory = managedRPCFactory()
		codexOptions.CatalogSource = managed.StoreCatalogSource{Store: durable}
		codexOptions.RootControlHandlers = rootHandlers
		if testClock != nil {
			codexOptions.Clock = testClock
		}
		if codexOptions.GoalContinuationDelay <= 0 {
			codexOptions.GoalContinuationDelay = 10 * time.Second
		}
		if err := managed.LoadCodexModelsStore(options.managed.PiAgentDir); err != nil {
			log.Printf("ordinary Pi model store unavailable: %v", err)
		}
		managedManager, err = managed.NewManager(codexOptions)
		if err != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(startupResult(stageHelper, err))
		}
		if err := configurePrivateStatusProbe(managedManager); err != nil {
			_ = managedManager.Close(ctx)
			_ = durable.Close()
			log.Fatal(startupResult(stageHelper, errors.New("private status probe activation failed")))
		}
		nodeExecutable, nodeErr := exec.LookPath("node")
		if nodeErr != nil || !filepath.IsAbs(nodeExecutable) {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(startupResult(stageHelper, errors.New("managed provider helper requires an absolute Node executable")))
		}
		ordinaryAuth := fixedAuthHelper{config: options.managed, nodeExecutable: nodeExecutable, ownedCodex: codexOptions.CodexCredentialStore != nil}
		var authHelper managed.ProviderAuthHelper = ordinaryAuth
		if codexOptions.CodexCredentialStore != nil {
			codexOptions.CodexCredentialStore.SetProjection(ordinaryAuth.consumeCodexCredential)
			if err := codexOptions.CodexCredentialStore.SynchronizeProjection(ctx); err != nil {
				_ = managedManager.Close(ctx)
				_ = durable.Close()
				log.Fatal(startupResult(stageHelper, errors.New("provider credential synchronization failed")))
			}
			authHelper = managed.CodexBrowserAuth{Ordinary: ordinaryAuth, Credentials: codexOptions.CodexCredentialStore, Client: &http.Client{Timeout: 30 * time.Second}, Manager: managedManager}
		}
		providerAuth, err = managed.NewProviderAuthCoordinator(durable, managedManager, authHelper)
		if err != nil {
			_, _ = attachmentsStore.Sweep(nil)
			_ = durable.Close()
			log.Fatal(startupResult(stageHelper, err))
		}
		// Provider settings starts from ordinary Pi's current local auth. This
		// records only the bounded status/catalog projection; credentials never
		// enter router storage or logs.
		if _, err := providerAuth.Snapshot(ctx); err != nil {
			if err := startupSnapshotUnavailable(); err != nil {
				log.Fatal(err)
			}
			log.Print("managed provider status is unavailable")
		} else if err := startupResult(stageHelper, nil); err != nil {
			log.Fatal(err)
		}
	} else if err := startupResult(stageStoreManifest, nil); err != nil {
		log.Fatal(err)
	}
	managedBridgeHandler := transport.NewManagedBridgeHandler(durable)
	if err := startupEntered(stageListen); err != nil {
		log.Fatal(err)
	}
	router, err := transport.New(transport.Options{Store: durable, Host: options.host, Port: options.port, Clock: storeOpts.Clock, EnableTestClock: options.testClock, Attachments: attachmentsStore, EventLogger: logger, Tmux: newTmuxAdapter("tmux"), EnableTmuxRouterAudit: options.auditSupervisorTarget != "" && options.auditTmuxSession != "", AuditSupervisorTarget: options.auditSupervisorTarget, AuditTmuxSession: options.auditTmuxSession, PolicyTimeout: options.policyTimeout, Managed: managedManager, DefaultEmbeddedRoot: embeddedWorkerDefaultEnabled(options), StartupManaged: func(startupCtx context.Context, manager *managed.Manager) error {
		if !options.embeddedRoot.Enabled {
			return launchConfiguredEmbeddedRoot(startupCtx, options, manager)
		}
		// Callback completion is admission only, never root readiness.
		return startupRun(stageRootAdmission, func() error {
			return launchConfiguredEmbeddedRoot(startupCtx, options, manager)
		})
	}, ManagedBridge: managedBridgeHandler, ProviderAuth: providerAuth, StaticUIRoot: options.managedUIDist, RuntimeIdentity: transport.RuntimeIdentity{SourceCommit: buildSourceCommit, SourceTree: buildSourceTree}, RouterLogReader: routerLogReader})
	if err != nil {
		_, _ = attachmentsStore.Sweep(nil)
		_ = durable.Close()
		log.Fatal(startupResult(stageListen, err))
	}
	if err := router.Listen(ctx); err != nil {
		_ = router.Close(context.Background())
		_ = durable.Close()
		log.Fatal(startupResult(stageListen, err))
	}
	// Listen returns only after managed startup and readiness publication.
	if err := startupResult(stageListen, nil); err != nil {
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
