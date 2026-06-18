package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/headless"
	"m31labs.dev/buckley/pkg/ipc"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
)

type ipcServer interface {
	Start(ctx context.Context) error
}

const (
	envBuckleyGenerateIPCToken = "BUCKLEY_GENERATE_IPC_TOKEN"
	// #nosec G101 -- env var name (not a credential)
	envBuckleyIPCTokenFile  = "BUCKLEY_IPC_TOKEN_FILE"
	envBuckleyPrintIPCToken = "BUCKLEY_PRINT_GENERATED_IPC_TOKEN"
)

var serveLoadConfigFn = config.Load
var serveInitStoreFn = initIPCStore
var serveNewServerFn = func(cfg ipc.Config, store *storage.Store, telemetryHub *telemetry.Hub, commandGateway *command.Gateway, planStore orchestrator.PlanStore, appCfg *config.Config, workflow *orchestrator.WorkflowManager, models *model.Manager) ipcServer {
	return ipc.NewServer(cfg, store, telemetryHub, commandGateway, planStore, appCfg, workflow, models)
}

type serveCommandOptions struct {
	bind           string
	assetPath      string
	enableBrowser  bool
	requireToken   bool
	publicMetrics  bool
	authToken      string
	tokenFile      string
	generateToken  bool
	printToken     bool
	basicAuthUser  string
	basicAuthPass  string
	allowedOrigins []string
}

func parseServeCommandOptions(args []string, ipcDefaults config.IPCConfig) (serveCommandOptions, error) {
	if strings.TrimSpace(ipcDefaults.Bind) == "" {
		ipcDefaults.Bind = "127.0.0.1:4488"
	}
	allowedOrigins := append([]string{}, ipcDefaults.AllowedOrigins...)
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	bind := fs.String("bind", ipcDefaults.Bind, "address to bind the IPC server")
	assetPath := fs.String("assets", "", "path to built frontend assets (served when --browser)")
	enableBrowser := fs.Bool("browser", ipcDefaults.EnableBrowser, "serve the embedded browser UI (or --assets override)")
	requireToken := fs.Bool("require-token", ipcDefaults.RequireToken, "reject clients that do not supply an auth token")
	publicMetrics := fs.Bool("public-metrics", ipcDefaults.PublicMetrics, "expose /metrics without authentication (useful for Prometheus scraping)")
	authTokenFlag := fs.String("auth-token", "", "token clients must supply (default: BUCKLEY_IPC_TOKEN)")
	tokenFile := fs.String("token-file", "", "path to a file containing the IPC auth token (supports ~)")
	generateToken := fs.Bool("generate-token", false, "generate and persist an IPC auth token when missing (uses --token-file or BUCKLEY_DATA_DIR)")
	printToken := fs.Bool("print-token", false, "print generated IPC auth token to stderr (use cautiously; may leak via logs)")
	basicAuthUser := fs.String("basic-auth-user", "", "Basic auth username (overrides config/env)")
	basicAuthPass := fs.String("basic-auth-pass", "", "Basic auth password (overrides config/env)")
	fs.Var(&stringListValue{target: &allowedOrigins}, "allow-origin", "additional allowed Origin (repeatable, accepts comma-separated list)")

	if err := fs.Parse(args); err != nil {
		return serveCommandOptions{}, err
	}

	token := strings.TrimSpace(*authTokenFlag)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("BUCKLEY_IPC_TOKEN"))
	}

	generateTokenFinal := *generateToken
	if v, ok := parseBoolEnv(envBuckleyGenerateIPCToken); ok {
		generateTokenFinal = v
	}
	printTokenFinal := *printToken
	if v, ok := parseBoolEnv(envBuckleyPrintIPCToken); ok {
		printTokenFinal = v
	}

	return serveCommandOptions{
		bind:           strings.TrimSpace(*bind),
		assetPath:      strings.TrimSpace(*assetPath),
		enableBrowser:  *enableBrowser,
		requireToken:   *requireToken,
		publicMetrics:  *publicMetrics,
		authToken:      token,
		tokenFile:      strings.TrimSpace(*tokenFile),
		generateToken:  generateTokenFinal,
		printToken:     printTokenFinal,
		basicAuthUser:  strings.TrimSpace(*basicAuthUser),
		basicAuthPass:  strings.TrimSpace(*basicAuthPass),
		allowedOrigins: allowedOrigins,
	}, nil
}

func finalizeServeCommandOptions(appCfg *config.Config, opts *serveCommandOptions) error {
	tokenFilePath, err := resolveServeTokenFilePath(opts.tokenFile)
	if err != nil {
		return err
	}
	opts.tokenFile = tokenFilePath

	if err := ensureServeAuthToken(opts); err != nil {
		return err
	}
	if err := finalizeServeAssetPath(opts); err != nil {
		return err
	}
	if err := applyServeBasicAuth(appCfg, *opts); err != nil {
		return err
	}
	return validateServeBindAuth(appCfg, *opts)
}

func resolveServeTokenFilePath(rawPath string) (string, error) {
	tokenFilePath := strings.TrimSpace(rawPath)
	if tokenFilePath == "" {
		tokenFilePath = strings.TrimSpace(os.Getenv(envBuckleyIPCTokenFile))
	}
	if tokenFilePath != "" {
		return expandHomePath(tokenFilePath)
	}
	if dir := strings.TrimSpace(os.Getenv(envBuckleyDataDir)); dir != "" {
		expanded, err := expandHomePath(dir)
		if err != nil {
			return "", err
		}
		return filepath.Join(expanded, "ipc-token"), nil
	}
	return "", nil
}

func ensureServeAuthToken(opts *serveCommandOptions) error {
	if !opts.requireToken {
		return nil
	}
	if opts.authToken != "" {
		return nil
	}
	if opts.tokenFile == "" {
		if opts.generateToken {
			return fmt.Errorf("--generate-token requires --token-file or BUCKLEY_DATA_DIR to be set")
		}
		return fmt.Errorf("--require-token set but no token provided (set BUCKLEY_IPC_TOKEN or --auth-token)")
	}

	stored, readErr := readTokenFile(opts.tokenFile)
	switch {
	case readErr == nil:
		opts.authToken = stored
		return nil
	case errors.Is(readErr, iofs.ErrNotExist):
		if !opts.generateToken {
			return fmt.Errorf("--require-token set but no token provided (set BUCKLEY_IPC_TOKEN, --auth-token, or use --generate-token with --token-file)")
		}
		generated, err := generateTokenFile(opts.tokenFile)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Saved IPC token to %s\n", opts.tokenFile)
		if opts.printToken {
			fmt.Fprintf(os.Stderr, "Generated IPC token (store this securely): %s\n", generated)
		}
		opts.authToken = generated
		return nil
	default:
		return readErr
	}
}

func finalizeServeAssetPath(opts *serveCommandOptions) error {
	if opts.assetPath != "" {
		abs, err := filepath.Abs(opts.assetPath)
		if err != nil {
			return fmt.Errorf("invalid assets path: %w", err)
		}
		opts.assetPath = abs
	}
	opts.enableBrowser = opts.enableBrowser || opts.assetPath != ""
	return nil
}

func applyServeBasicAuth(appCfg *config.Config, opts serveCommandOptions) error {
	if opts.basicAuthUser != "" || opts.basicAuthPass != "" {
		if opts.basicAuthUser == "" || opts.basicAuthPass == "" {
			return fmt.Errorf("--basic-auth-user and --basic-auth-pass must be set together")
		}
		appCfg.IPC.BasicAuthUsername = opts.basicAuthUser
		appCfg.IPC.BasicAuthPassword = opts.basicAuthPass
		appCfg.IPC.BasicAuthEnabled = true
	}
	if appCfg.IPC.BasicAuthEnabled {
		if strings.TrimSpace(appCfg.IPC.BasicAuthUsername) == "" || strings.TrimSpace(appCfg.IPC.BasicAuthPassword) == "" {
			return fmt.Errorf("basic auth enabled but username/password missing")
		}
	}
	return nil
}

func validateServeBindAuth(appCfg *config.Config, opts serveCommandOptions) error {
	if isLoopbackAddress(opts.bind) {
		return nil
	}
	if opts.requireToken || appCfg.IPC.BasicAuthEnabled {
		return nil
	}
	return fmt.Errorf("refusing to bind IPC to %q without authentication (set --require-token or basic auth)", opts.bind)
}

func runServeCommand(args []string) error {
	appCfg, err := serveLoadConfigFn()
	if err != nil {
		return withExitCode(err, 2)
	}
	agentProfile, err := loadStartupAgentProfile(agentProfileFlag)
	if err != nil {
		return withExitCode(fmt.Errorf("loading agent spec: %w", err), 2)
	}
	if agentProfile != nil {
		agentProfile.ApplyToConfig(appCfg)
	}
	applyStartupModelOverride(appCfg, modelOverrideFlag)

	opts, err := parseServeCommandOptions(args, appCfg.IPC)
	if err != nil {
		return err
	}
	if err := finalizeServeCommandOptions(appCfg, &opts); err != nil {
		return err
	}

	store, err := serveInitStoreFn()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	telemetryHub := telemetry.NewHub()
	defer telemetryHub.Close()
	commandGateway := command.NewGateway()
	planStore := orchestrator.NewFilePlanStore(appCfg.Artifacts.PlanningDir)
	models := initServeModels(appCfg)

	if stopACP, err := maybeStartServeACP(appCfg, models, store, opts.bind); err != nil {
		return err
	} else if stopACP != nil {
		defer stopACP()
	}

	cfg := buildServeIPCConfig(appCfg, opts, agentPromptSection(agentProfile))
	server := serveNewServerFn(cfg, store, telemetryHub, commandGateway, planStore, appCfg, nil, models)
	if models != nil {
		if registryInit, ok := server.(interface {
			InitHeadlessRegistry(context.Context) *headless.Registry
		}); ok && commandGateway != nil {
			if reg := registryInit.InitHeadlessRegistry(ctx); reg != nil {
				commandGateway.Register(reg)
			}
		}
	}
	return server.Start(ctx)
}

func initServeModels(appCfg *config.Config) *model.Manager {
	if appCfg == nil || !appCfg.Providers.HasReadyProvider() {
		fmt.Fprintf(os.Stderr, "warning: no provider API keys configured; headless sessions disabled\n")
		return nil
	}
	mgr, err := model.NewManager(appCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to initialize models (headless disabled): %v\n", err)
		return nil
	}
	if err := mgr.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to initialize models (headless disabled): %v\n", err)
		return nil
	}
	return mgr
}

func maybeStartServeACP(appCfg *config.Config, models *model.Manager, store *storage.Store, bind string) (func(), error) {
	autoACP := false
	if appCfg != nil && strings.TrimSpace(appCfg.ACP.Listen) == "" && isLoopbackAddress(bind) {
		appCfg.ACP.Listen = "127.0.0.1:50051"
		appCfg.ACP.AllowInsecureLocal = true
		autoACP = true
	}
	stopACP, err := startACPServer(appCfg, models, store)
	if err != nil {
		if autoACP {
			fmt.Fprintf(os.Stderr, "warning: failed to start ACP server: %v\n", err)
			return nil, nil
		}
		return nil, err
	}
	return stopACP, nil
}

func buildServeIPCConfig(appCfg *config.Config, opts serveCommandOptions, agentProfile string) ipc.Config {
	return ipc.Config{
		BindAddress:       opts.bind,
		StaticDir:         opts.assetPath,
		EnableBrowser:     opts.enableBrowser,
		AuthToken:         opts.authToken,
		AllowedOrigins:    opts.allowedOrigins,
		PublicMetrics:     opts.publicMetrics,
		RequireToken:      opts.requireToken,
		Version:           version,
		BasicAuthEnabled:  appCfg.IPC.BasicAuthEnabled,
		BasicAuthUsername: appCfg.IPC.BasicAuthUsername,
		BasicAuthPassword: appCfg.IPC.BasicAuthPassword,
		ProjectRoot:       config.ResolveProjectRoot(appCfg),
		AgentProfile:      agentProfile,
	}
}

func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func generateTokenFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("token file path cannot be empty")
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := hex.EncodeToString(buf)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token file: %w", err)
	}
	return token, nil
}

func initIPCStore() (*storage.Store, error) {
	dbPath, err := resolveDBPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("failed to ensure data directory: %w", err)
	}
	store, err := storage.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open storage: %w", err)
	}
	return store, nil
}

type stringListValue struct {
	target *[]string
}

func (s *stringListValue) String() string {
	if s == nil || s.target == nil {
		return ""
	}
	return strings.Join(*s.target, ",")
}

func (s *stringListValue) Set(value string) error {
	if s.target == nil {
		return fmt.Errorf("no target slice configured")
	}
	parts := strings.Split(value, ",")
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		*s.target = append(*s.target, trimmed)
	}
	return nil
}
