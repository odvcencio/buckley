package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func runRemoteCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley remote <attach|sessions|tokens|login|console> [flags]")
	}

	switch args[0] {
	case "attach":
		return runRemoteAttach(args[1:])
	case "sessions":
		return runRemoteSessions(args[1:])
	case "tokens":
		return runRemoteTokens(args[1:])
	case "login":
		return runRemoteLogin(args[1:])
	case "console":
		return runRemoteConsole(args[1:])
	default:
		return fmt.Errorf("unknown remote subcommand: %s", args[0])
	}
}

type remoteBaseOptions struct {
	BaseURL      string
	IPCAuthToken string
	BasicUser    string
	BasicPass    string
	InsecureTLS  bool
}

type remoteAttachOptions struct {
	remoteBaseOptions
	SessionID string
}

func runRemoteAttach(args []string) error {
	opts, err := parseRemoteAttachFlags(args)
	if err != nil {
		return fmt.Errorf("parsing attach flags: %w", err)
	}

	client, err := newRemoteClient(opts.remoteBaseOptions)
	if err != nil {
		return fmt.Errorf("creating remote client: %w", err)
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nInterrupted – closing remote session")
		cancel()
	}()

	sessionToken, err := client.issueSessionToken(ctx, opts.SessionID)
	if err != nil {
		return fmt.Errorf("issuing session token: %w", err)
	}

	var activePlan string
	if detail, err := client.getSessionDetail(ctx, opts.SessionID); err == nil {
		fmt.Printf("Session %s status: %s (branch: %s)\n", detail.Session.ID, detail.Session.Status, detail.Session.GitBranch)
		if detail.Plan.ID != "" {
			activePlan = detail.Plan.ID
			fmt.Printf("Plan %s – %s (%d tasks)\n", detail.Plan.ID, detail.Plan.FeatureName, len(detail.Plan.Tasks))
		}
	}

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	eventCh := make(chan remoteEvent, 64)
	go func() {
		defer close(eventCh)
		if err := client.streamEvents(streamCtx, opts.SessionID, eventCh); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "remote stream ended: %v\n", err)
			cancel()
		}
	}()

	fmt.Printf("📡 Attached to session %s at %s\n", opts.SessionID, opts.BaseURL)
	fmt.Println("Type slash commands (/plan, /execute, /workflow pause ...) or plain text. Type :q to exit.")

	go func() {
		for evt := range eventCh {
			if evt.SessionID != "" && evt.SessionID != opts.SessionID {
				continue
			}
			printRemoteEvent(evt)
		}
	}()

	return remoteInputLoop(ctx, client, opts.SessionID, sessionToken, activePlan)
}

func runRemoteSessions(args []string) error {
	base, err := parseRemoteBaseFlags("sessions", args)
	if err != nil {
		return fmt.Errorf("parsing sessions flags: %w", err)
	}
	client, err := newRemoteClient(base)
	if err != nil {
		return fmt.Errorf("creating remote client: %w", err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessions, err := client.listSessions(ctx)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}
	if len(sessions) == 0 {
		fmt.Println("No active sessions")
		return nil
	}
	fmt.Printf("%-16s %-12s %-24s %s\n", "SESSION", "STATUS", "LAST ACTIVE", "BRANCH")
	for _, sess := range sessions {
		last := sess.LastActive
		if last != "" {
			if ts, err := time.Parse(time.RFC3339Nano, last); err == nil {
				last = ts.Local().Format(time.RFC822)
			}
		}
		fmt.Printf("%-16s %-12s %-24s %s\n", sess.ID, sess.Status, last, sess.GitBranch)
	}
	return nil
}

func runRemoteTokens(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley remote tokens <list|create|revoke> [flags]")
	}
	sub := args[0]
	switch sub {
	case "list":
		base, err := parseRemoteBaseFlags("tokens list", args[1:])
		if err != nil {
			return fmt.Errorf("parsing token list flags: %w", err)
		}
		client, err := newRemoteClient(base)
		if err != nil {
			return fmt.Errorf("creating remote client: %w", err)
		}
		defer func() { _ = client.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		tokens, err := client.listAPITokens(ctx)
		if err != nil {
			return fmt.Errorf("listing api tokens: %w", err)
		}
		fmt.Printf("%-8s %-12s %-10s %-12s %s\n", "ID", "NAME", "SCOPE", "LAST USED", "STATUS")
		for _, tok := range tokens {
			last := tok.LastUsed
			if last != "" {
				if ts, err := time.Parse(time.RFC3339Nano, last); err == nil {
					last = ts.Local().Format(time.RFC822)
				}
			} else {
				last = "—"
			}
			status := "active"
			if tok.Revoked {
				status = "revoked"
			}
			fmt.Printf("%.8s %-12s %-10s %-12s %s\n", tok.ID, tok.Name, tok.Scope, last, status)
		}
	case "create":
		fs := flag.NewFlagSet("remote tokens create", flag.ContinueOnError)
		var base remoteBaseOptions
		registerRemoteBaseFlags(fs, &base)
		name := fs.String("name", "", "Token label")
		owner := fs.String("owner", "", "Owner / contact")
		scope := fs.String("scope", "member", "Scope (operator|member|viewer)")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("parsing token create flags: %w", err)
		}
		if strings.TrimSpace(base.BaseURL) == "" {
			return fmt.Errorf("--url is required")
		}
		client, err := newRemoteClient(base)
		if err != nil {
			return fmt.Errorf("creating remote client: %w", err)
		}
		defer func() { _ = client.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		token, record, err := client.createAPIToken(ctx, *name, *owner, *scope)
		if err != nil {
			return fmt.Errorf("creating api token: %w", err)
		}
		fmt.Println("Token created. Store this secret safely:")
		fmt.Printf("  %s\n", token)
		fmt.Printf("Scope: %s  Prefix: %s\n", record.Scope, record.Prefix)
	case "revoke":
		fs := flag.NewFlagSet("remote tokens revoke", flag.ContinueOnError)
		var base remoteBaseOptions
		registerRemoteBaseFlags(fs, &base)
		tokenID := fs.String("id", "", "Token ID to revoke")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("parsing token revoke flags: %w", err)
		}
		if strings.TrimSpace(base.BaseURL) == "" {
			return fmt.Errorf("--url is required")
		}
		client, err := newRemoteClient(base)
		if err != nil {
			return fmt.Errorf("creating remote client: %w", err)
		}
		defer func() { _ = client.Close() }()
		if strings.TrimSpace(*tokenID) == "" {
			return fmt.Errorf("--id required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := client.revokeAPIToken(ctx, *tokenID); err != nil {
			return fmt.Errorf("revoking api token: %w", err)
		}
		fmt.Println("Token revoked.")
	default:
		return fmt.Errorf("unknown tokens subcommand %s", sub)
	}
	return nil
}

type remoteLoginOptions struct {
	remoteBaseOptions
	Label     string
	NoBrowser bool
	Timeout   time.Duration
}

type remoteConsoleOptions struct {
	remoteBaseOptions
	SessionID string
	Command   string
}

func runRemoteLogin(args []string) error {
	opts, err := parseRemoteLoginFlags(args)
	if err != nil {
		return fmt.Errorf("parsing login flags: %w", err)
	}
	client, err := newRemoteClient(opts.remoteBaseOptions)
	if err != nil {
		return fmt.Errorf("creating remote client: %w", err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()
	ticket, err := client.createCliTicket(ctx, opts.Label)
	if err != nil {
		return fmt.Errorf("creating cli ticket: %w", err)
	}
	fmt.Printf("📟 Ticket %s created. Secret stored locally.\n", ticket.Ticket)
	fmt.Printf("Open this URL in a logged-in browser session to approve:\n  %s\n", ticket.LoginURL)
	if !opts.NoBrowser {
		_ = openBrowser(ticket.LoginURL)
	}
	fmt.Println("Waiting for approval… Press Ctrl+C to cancel.")
	if err := client.waitForCliApproval(ctx, ticket.Ticket, ticket.Secret, 3*time.Second); err != nil {
		return fmt.Errorf("waiting for cli approval: %w", err)
	}
	if err := client.persistCookies(); err != nil {
		return fmt.Errorf("persisting auth cookies: %w", err)
	}
	fmt.Println("✅ Buckley CLI is now authenticated via your browser session.")
	return nil
}

func runRemoteConsole(args []string) error {
	opts, err := parseRemoteConsoleFlags(args)
	if err != nil {
		return fmt.Errorf("parsing console flags: %w", err)
	}
	client, err := newRemoteClient(opts.remoteBaseOptions)
	if err != nil {
		return fmt.Errorf("creating remote client: %w", err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionToken, err := client.issueSessionToken(ctx, opts.SessionID)
	if err != nil {
		return fmt.Errorf("issuing session token: %w", err)
	}
	fmt.Printf("Opening remote console for session %s. Press Ctrl+C to exit.\n", opts.SessionID)
	return client.openRemotePTY(ctx, opts.SessionID, sessionToken, opts.Command)
}
