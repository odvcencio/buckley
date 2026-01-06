package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	acppb "github.com/odvcencio/buckley/pkg/acp/proto"
	"github.com/odvcencio/buckley/pkg/agentserver"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/ui/viewmodel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// acpAgentClient is the subset of ACP client methods required by the agent server.
type acpAgentClient interface {
	StreamInlineCompletions(ctx context.Context, in *acppb.InlineCompletionRequest, opts ...grpc.CallOption) (acppb.AgentCommunication_StreamInlineCompletionsClient, error)
	ProposeEdits(ctx context.Context, in *acppb.ProposeEditsRequest, opts ...grpc.CallOption) (*acppb.ProposeEditsResponse, error)
	ApplyEdits(ctx context.Context, in *acppb.ApplyEditsRequest, opts ...grpc.CallOption) (*acppb.ApplyEditsResponse, error)
	UpdateEditorState(ctx context.Context, in *acppb.UpdateEditorStateRequest, opts ...grpc.CallOption) (*acppb.UpdateEditorStateResponse, error)
}

var connectACPFn = func(ctx context.Context, target string, opts ...grpc.DialOption) (acpAgentClient, func() error, error) {
	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, nil, err
	}
	client := acppb.NewAgentCommunicationClient(conn)
	return client, conn.Close, nil
}

var buildViewProviderFn = buildViewProvider
var agentServerListenFn = func(server *http.Server) error {
	return server.ListenAndServe()
}

func runAgentServerCommand(args []string) error {
	fs := flag.NewFlagSet("agent-server", flag.ContinueOnError)
	bind := fs.String("bind", "127.0.0.1:5555", "Listen address for the agent server HTTP endpoint")
	allowRemote := fs.Bool("allow-remote", false, "Allow binding agent-server to non-loopback addresses without auth (dangerous)")
	acpTarget := fs.String("acp-target", "127.0.0.1:50051", "ACP gRPC target (host:port)")
	acpCAFile := fs.String("acp-ca-file", "", "Path to PEM CA bundle for ACP TLS (enables TLS)")
	acpClientCert := fs.String("acp-client-cert", "", "Path to PEM client certificate for ACP mTLS")
	acpClientKey := fs.String("acp-client-key", "", "Path to PEM client private key for ACP mTLS")
	acpServerName := fs.String("acp-server-name", "", "Override TLS server name for ACP (optional)")
	acpInsecureSkipVerify := fs.Bool("acp-insecure-skip-verify", false, "Skip ACP TLS certificate verification (dangerous; dev only)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !isLoopbackAddress(strings.TrimSpace(*bind)) && !*allowRemote {
		return fmt.Errorf("refusing to bind agent-server to %q without explicit --allow-remote", strings.TrimSpace(*bind))
	}

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dialCancel()

	dialOpts := []grpc.DialOption{grpc.WithBlock()}
	if usesACPTLS(*acpCAFile, *acpClientCert, *acpClientKey, *acpServerName, *acpInsecureSkipVerify) {
		creds, err := buildACPTLSCredentials(*acpCAFile, *acpClientCert, *acpClientKey, *acpServerName, *acpInsecureSkipVerify, *acpTarget)
		if err != nil {
			return err
		}
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	client, closeConn, err := connectACPFn(dialCtx, *acpTarget, dialOpts...)
	if err != nil {
		return fmt.Errorf("connect to ACP target: %w", err)
	}
	if closeConn != nil {
		defer func() { _ = closeConn() }()
	}

	viewProv, viewCleanup, err := buildViewProviderFn()
	if err != nil {
		fmt.Printf("Warning: view provider unavailable: %v (view_state endpoint disabled)\n", err)
	}
	if viewCleanup != nil {
		defer viewCleanup()
	}

	srv := agentserver.New(client, agentserver.WithViewProvider(viewProv))

	server := &http.Server{
		Addr:         *bind,
		Handler:      srv.Router(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	fmt.Printf("Agent server listening on http://%s (proxy to ACP %s)\n", *bind, *acpTarget)
	if err := agentServerListenFn(server); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("agent server failed: %w", err)
	}
	return nil
}

func usesACPTLS(caFile, clientCert, clientKey, serverName string, insecureSkipVerify bool) bool {
	return strings.TrimSpace(caFile) != "" ||
		strings.TrimSpace(clientCert) != "" ||
		strings.TrimSpace(clientKey) != "" ||
		strings.TrimSpace(serverName) != "" ||
		insecureSkipVerify
}

func buildACPTLSCredentials(caFile, clientCert, clientKey, serverName string, insecureSkipVerify bool, target string) (credentials.TransportCredentials, error) {
	rootCAs, _ := x509.SystemCertPool()
	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}
	if caFile = strings.TrimSpace(caFile); caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read ACP CA bundle: %w", err)
		}
		if ok := rootCAs.AppendCertsFromPEM(pem); !ok {
			return nil, fmt.Errorf("invalid ACP CA bundle")
		}
	}

	var certs []tls.Certificate
	clientCert = strings.TrimSpace(clientCert)
	clientKey = strings.TrimSpace(clientKey)
	switch {
	case clientCert == "" && clientKey == "":
		// Allow TLS without client cert (server may still reject).
	case clientCert == "" || clientKey == "":
		return nil, fmt.Errorf("acp-client-cert and acp-client-key must be set together")
	default:
		cert, err := tls.LoadX509KeyPair(clientCert, clientKey)
		if err != nil {
			return nil, fmt.Errorf("load ACP client certificate: %w", err)
		}
		certs = []tls.Certificate{cert}
	}

	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		serverName = strings.TrimSpace(hostnameFromGRPCTarget(target))
	}

	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		Certificates:       certs,
		RootCAs:            rootCAs,
		ServerName:         serverName,
		InsecureSkipVerify: insecureSkipVerify,
	}
	return credentials.NewTLS(cfg), nil
}

func hostnameFromGRPCTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(target); err == nil && strings.TrimSpace(host) != "" {
		return strings.TrimSpace(host)
	}
	if strings.Contains(target, "/") {
		return ""
	}
	if strings.Contains(target, ":") {
		return ""
	}
	return target
}

// For debugging locally via curl.
func debugJSON(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// buildViewProvider wires the shared view assembler so editor clients can fetch view_state snapshots.
// It is best-effort; failures leave the endpoint disabled.
func buildViewProvider() (agentserver.ViewProvider, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, func() {}, err
	}
	dbPath, err := resolveDBPath()
	if err != nil {
		return nil, func() {}, err
	}
	store, err := storage.New(dbPath)
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { store.Close() }
	planStore := orchestrator.NewFilePlanStore(cfg.Artifacts.PlanningDir)
	assembler := viewmodel.NewAssembler(store, planStore, nil)
	return assembler, cleanup, nil
}
