package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	acppb "github.com/odvcencio/buckley/pkg/acp/proto"
	acpserver "github.com/odvcencio/buckley/pkg/acp/server"
	"github.com/odvcencio/buckley/pkg/config"
	coordination "github.com/odvcencio/buckley/pkg/coordination/coordinator"
	coordevents "github.com/odvcencio/buckley/pkg/coordination/events"
	"github.com/odvcencio/buckley/pkg/ipc"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/setup"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

func resolveDependencies(checker *setup.Checker) error {
	missing, err := checker.CheckAll()
	if err != nil {
		return err
	}

	if len(missing) == 0 {
		return nil
	}

	if err := checker.RunWizard(missing); err != nil {
		return err
	}

	missing, err = checker.CheckAll()
	if err != nil {
		return err
	}

	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for _, dep := range missing {
			names = append(names, dep.Name)
		}
		return fmt.Errorf("missing dependencies: %s", strings.Join(names, ", "))
	}

	return nil
}

func hasACPTLS(cfg config.ACPConfig) bool {
	return strings.TrimSpace(cfg.TLSCertFile) != "" &&
		strings.TrimSpace(cfg.TLSKeyFile) != "" &&
		strings.TrimSpace(cfg.TLSClientCAFile) != ""
}

func isLoopbackAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func startEmbeddedIPCServer(cfg *config.Config, store *storage.Store, telemetryHub *telemetry.Hub, commandGateway *command.Gateway, planStore orchestrator.PlanStore, workflow *orchestrator.WorkflowManager, models *model.Manager) (func(), string, error) {
	ipcCfg := cfg.IPC
	if !ipcCfg.Enabled {
		return nil, "", nil
	}
	if strings.TrimSpace(ipcCfg.Bind) == "" {
		return nil, "", nil
	}

	token := strings.TrimSpace(os.Getenv("BUCKLEY_IPC_TOKEN"))
	if ipcCfg.RequireToken && token == "" && !ipcCfg.BasicAuthEnabled {
		return nil, "", fmt.Errorf("ipc token required (set BUCKLEY_IPC_TOKEN)")
	}

	projectRoot := config.ResolveProjectRoot(cfg)
	serverCfg := ipc.Config{
		BindAddress:       ipcCfg.Bind,
		StaticDir:         "",
		EnableBrowser:     ipcCfg.EnableBrowser,
		AllowedOrigins:    append([]string{}, ipcCfg.AllowedOrigins...),
		PublicMetrics:     ipcCfg.PublicMetrics,
		RequireToken:      ipcCfg.RequireToken,
		AuthToken:         token,
		Version:           version,
		BasicAuthEnabled:  ipcCfg.BasicAuthEnabled,
		BasicAuthUsername: ipcCfg.BasicAuthUsername,
		BasicAuthPassword: ipcCfg.BasicAuthPassword,
		ProjectRoot:       projectRoot,
	}

	ctx, cancel := context.WithCancel(context.Background())
	server := ipc.NewServer(serverCfg, store, telemetryHub, commandGateway, planStore, cfg, workflow, models)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start(ctx)
	}()

	select {
	case err := <-errCh:
		cancel()
		if errors.Is(err, context.Canceled) {
			return nil, "", nil
		}
		return nil, "", err
	case <-time.After(350 * time.Millisecond):
	}

	url := humanReadableURL(serverCfg.BindAddress)
	stop := func() {
		cancel()
	}
	return stop, url, nil
}

func humanReadableURL(bind string) string {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return fmt.Sprintf("http://%s", bind)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if port == "" {
		return fmt.Sprintf("http://%s", host)
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

// startACPServer launches the ACP gRPC server when configured.
func startACPServer(cfg *config.Config, mgr *model.Manager, store *storage.Store, runtime *coordinationRuntime) (func(), error) {
	acpCfg := cfg.ACP
	if strings.TrimSpace(acpCfg.Listen) == "" {
		return nil, nil
	}

	useTLS := hasACPTLS(acpCfg)
	allowInsecure := acpCfg.AllowInsecureLocal && isLoopbackAddress(acpCfg.Listen)
	if !useTLS && !allowInsecure {
		return nil, fmt.Errorf("acp listener %s requires mTLS or allow_insecure_local=true on loopback", acpCfg.Listen)
	}

	var tlsCfg *tls.Config
	if useTLS {
		cert, err := tls.LoadX509KeyPair(acpCfg.TLSCertFile, acpCfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load ACP TLS certs: %w", err)
		}

		clientCAPEM, err := os.ReadFile(acpCfg.TLSClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("load ACP client CA: %w", err)
		}

		clientCAPool := x509.NewCertPool()
		if ok := clientCAPool.AppendCertsFromPEM(clientCAPEM); !ok {
			return nil, fmt.Errorf("invalid ACP client CA bundle")
		}

		tlsCfg = &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    clientCAPool,
			MinVersion:   tls.VersionTLS12,
		}
	} else {
		fmt.Fprintf(os.Stderr, "Warning: starting ACP without TLS (allow_insecure_local=true for %s)\n", acpCfg.Listen)
	}

	var err error
	var eventStore coordevents.EventStore
	var closeStore func()
	if runtime != nil && runtime.eventStore != nil {
		eventStore = runtime.eventStore
	} else {
		store, closer, err := buildCoordinationEventStore(cfg)
		if err != nil {
			return nil, err
		}
		eventStore = store
		closeStore = closer
	}

	coord := (*coordination.Coordinator)(nil)
	if runtime != nil {
		coord = runtime.coordinator
	}
	if coord == nil {
		coord, err = coordination.NewCoordinator(coordination.DefaultConfig(), eventStore)
		if err != nil {
			if closeStore != nil {
				closeStore()
			}
			return nil, fmt.Errorf("init ACP coordinator: %w", err)
		}
	}
	if coord == nil {
		if closeStore != nil {
			closeStore()
		}
		return nil, fmt.Errorf("init ACP coordinator: coordinator is nil")
	}
	srv, err := acpserver.NewServer(coord, mgr, cfg, store)
	if err != nil {
		if closeStore != nil {
			closeStore()
		}
		return nil, fmt.Errorf("init ACP gRPC server: %w", err)
	}

	grpcOpts := []grpc.ServerOption{}
	if tlsCfg != nil {
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}
	grpcOpts = append(grpcOpts,
		grpc.ChainUnaryInterceptor(srv.UnaryAuthInterceptor),
		grpc.ChainStreamInterceptor(srv.StreamAuthInterceptor),
	)
	grpcServer := grpc.NewServer(grpcOpts...)
	acppb.RegisterAgentCommunicationServer(grpcServer, srv)

	lis, err := net.Listen("tcp", acpCfg.Listen)
	if err != nil {
		if closeStore != nil {
			closeStore()
		}
		return nil, fmt.Errorf("listen on %s: %w", acpCfg.Listen, err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- grpcServer.Serve(lis)
	}()

	stop := func() {
		grpcServer.GracefulStop()
		if closeStore != nil {
			closeStore()
		}
	}

	// Non-blocking health check: give the server a moment to start
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			stop()
			return nil, err
		}
	case <-time.After(150 * time.Millisecond):
	}

	fmt.Printf("🚀 ACP gRPC server listening on %s (event store: %s)\n", acpCfg.Listen, strings.ToLower(acpCfg.EventStore))
	return stop, nil
}
