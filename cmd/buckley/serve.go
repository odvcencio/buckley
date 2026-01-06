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

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/headless"
	"github.com/odvcencio/buckley/pkg/ipc"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
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

func runServeCommand(args []string) error {
	appCfg, err := serveLoadConfigFn()
	if err != nil {
		return withExitCode(err, 2)
	}

	ipcDefaults := appCfg.IPC
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
		return err
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

	tokenFilePath := strings.TrimSpace(*tokenFile)
	if tokenFilePath == "" {
		tokenFilePath = strings.TrimSpace(os.Getenv(envBuckleyIPCTokenFile))
	}
	if tokenFilePath != "" {
		expanded, err := expandHomePath(tokenFilePath)
		if err != nil {
			return err
		}
		tokenFilePath = expanded
	} else if dir := strings.TrimSpace(os.Getenv(envBuckleyDataDir)); dir != "" {
		expanded, err := expandHomePath(dir)
		if err != nil {
			return err
		}
		tokenFilePath = filepath.Join(expanded, "ipc-token")
	}

	if *requireToken && token == "" {
		if tokenFilePath != "" {
			stored, readErr := readTokenFile(tokenFilePath)
			switch {
			case readErr == nil:
				token = stored
			case errors.Is(readErr, iofs.ErrNotExist):
				if !generateTokenFinal {
					return fmt.Errorf("--require-token set but no token provided (set BUCKLEY_IPC_TOKEN, --auth-token, or use --generate-token with --token-file)")
				}
				generated, err := generateTokenFile(tokenFilePath)
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "Saved IPC token to %s\n", tokenFilePath)
				if printTokenFinal {
					fmt.Fprintf(os.Stderr, "Generated IPC token (store this securely): %s\n", generated)
				}
				token = generated
			default:
				return readErr
			}
		} else if generateTokenFinal {
			return fmt.Errorf("--generate-token requires --token-file or BUCKLEY_DATA_DIR to be set")
		}
	}

	if *requireToken && token == "" {
		return fmt.Errorf("--require-token set but no token provided (set BUCKLEY_IPC_TOKEN or --auth-token)")
	}

	staticDir := strings.TrimSpace(*assetPath)
	if staticDir != "" {
		abs, err := filepath.Abs(staticDir)
		if err != nil {
			return fmt.Errorf("invalid assets path: %w", err)
		}
		staticDir = abs
	}

	enableBrowserFinal := *enableBrowser || staticDir != ""

	if strings.TrimSpace(*basicAuthUser) != "" || strings.TrimSpace(*basicAuthPass) != "" {
		if strings.TrimSpace(*basicAuthUser) == "" || strings.TrimSpace(*basicAuthPass) == "" {
			return fmt.Errorf("--basic-auth-user and --basic-auth-pass must be set together")
		}
		appCfg.IPC.BasicAuthUsername = strings.TrimSpace(*basicAuthUser)
		appCfg.IPC.BasicAuthPassword = strings.TrimSpace(*basicAuthPass)
		appCfg.IPC.BasicAuthEnabled = true
	}
	if appCfg.IPC.BasicAuthEnabled {
		if strings.TrimSpace(appCfg.IPC.BasicAuthUsername) == "" || strings.TrimSpace(appCfg.IPC.BasicAuthPassword) == "" {
			return fmt.Errorf("basic auth enabled but username/password missing")
		}
	}
	if !isLoopbackAddress(strings.TrimSpace(*bind)) {
		if !*requireToken && !appCfg.IPC.BasicAuthEnabled {
			return fmt.Errorf("refusing to bind IPC to %q without authentication (set --require-token or basic auth)", strings.TrimSpace(*bind))
		}
	}

	store, err := serveInitStoreFn()
	if err != nil {
		return err
	}
	defer store.Close()

	projectRoot := config.ResolveProjectRoot(appCfg)
	cfg := ipc.Config{
		BindAddress:       *bind,
		StaticDir:         staticDir,
		EnableBrowser:     enableBrowserFinal,
		AuthToken:         token,
		AllowedOrigins:    allowedOrigins,
		PublicMetrics:     *publicMetrics,
		RequireToken:      *requireToken,
		Version:           version,
		BasicAuthEnabled:  appCfg.IPC.BasicAuthEnabled,
		BasicAuthUsername: appCfg.IPC.BasicAuthUsername,
		BasicAuthPassword: appCfg.IPC.BasicAuthPassword,
		ProjectRoot:       projectRoot,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	telemetryHub := telemetry.NewHub()
	defer telemetryHub.Close()
	commandGateway := command.NewGateway()
	planStore := orchestrator.NewFilePlanStore(appCfg.Artifacts.PlanningDir)

	var models *model.Manager
	if appCfg != nil && appCfg.Providers.HasReadyProvider() {
		mgr, err := model.NewManager(appCfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to initialize models (headless disabled): %v\n", err)
		} else if err := mgr.Initialize(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to initialize models (headless disabled): %v\n", err)
		} else {
			models = mgr
		}
	} else {
		fmt.Fprintf(os.Stderr, "warning: no provider API keys configured; headless sessions disabled\n")
	}

	autoACP := false
	if appCfg != nil && strings.TrimSpace(appCfg.ACP.Listen) == "" && isLoopbackAddress(strings.TrimSpace(*bind)) {
		appCfg.ACP.Listen = "127.0.0.1:50051"
		appCfg.ACP.AllowInsecureLocal = true
		autoACP = true
	}
	if stopACP, err := startACPServer(appCfg, models, store); err != nil {
		if autoACP {
			fmt.Fprintf(os.Stderr, "warning: failed to start ACP server: %v\n", err)
		} else {
			return err
		}
	} else if stopACP != nil {
		defer stopACP()
	}

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
