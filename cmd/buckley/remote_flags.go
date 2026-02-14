package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func parseRemoteAttachFlags(args []string) (remoteAttachOptions, error) {
	fs := flag.NewFlagSet("remote attach", flag.ContinueOnError)
	opts := remoteAttachOptions{}
	registerRemoteBaseFlags(fs, &opts.remoteBaseOptions)
	fs.StringVar(&opts.SessionID, "session", "", "Session ID to attach to")

	if err := fs.Parse(args); err != nil {
		return remoteAttachOptions{}, fmt.Errorf("parsing attach flags: %w", err)
	}
	if err := validateRemoteBaseOptions(opts.remoteBaseOptions); err != nil {
		return remoteAttachOptions{}, fmt.Errorf("validating remote options: %w", err)
	}

	if strings.TrimSpace(opts.BaseURL) == "" {
		return remoteAttachOptions{}, fmt.Errorf("--url is required")
	}
	if strings.TrimSpace(opts.SessionID) == "" {
		return remoteAttachOptions{}, fmt.Errorf("--session is required")
	}

	return opts, nil
}

func parseRemoteBaseFlags(cmd string, args []string) (remoteBaseOptions, error) {
	fs := flag.NewFlagSet("remote "+cmd, flag.ContinueOnError)
	var opts remoteBaseOptions
	registerRemoteBaseFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return remoteBaseOptions{}, fmt.Errorf("parsing remote flags: %w", err)
	}
	if err := validateRemoteBaseOptions(opts); err != nil {
		return remoteBaseOptions{}, fmt.Errorf("validating remote options: %w", err)
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return remoteBaseOptions{}, fmt.Errorf("--url is required")
	}
	return opts, nil
}

func registerRemoteBaseFlags(fs *flag.FlagSet, opts *remoteBaseOptions) {
	fs.StringVar(&opts.BaseURL, "url", "", "Base URL of the remote Buckley instance")
	defaultToken := strings.TrimSpace(os.Getenv("BUCKLEY_IPC_TOKEN"))
	fs.StringVar(&opts.IPCAuthToken, "token", defaultToken, "IPC bearer token")
	defaultUser := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_USER"))
	defaultPass := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_PASSWORD"))
	fs.StringVar(&opts.BasicUser, "basic-auth-user", defaultUser, "HTTP basic auth username")
	fs.StringVar(&opts.BasicPass, "basic-auth-pass", defaultPass, "HTTP basic auth password")
	fs.BoolVar(&opts.InsecureTLS, "insecure-skip-verify", false, "Skip TLS verification (development only)")
}

func validateRemoteBaseOptions(opts remoteBaseOptions) error {
	user := strings.TrimSpace(opts.BasicUser)
	pass := strings.TrimSpace(opts.BasicPass)
	if user != "" && pass == "" {
		return fmt.Errorf("--basic-auth-user requires --basic-auth-pass (or BUCKLEY_BASIC_AUTH_PASSWORD)")
	}
	if user == "" && pass != "" {
		return fmt.Errorf("--basic-auth-pass requires --basic-auth-user (or BUCKLEY_BASIC_AUTH_USER)")
	}
	return nil
}

func parseRemoteLoginFlags(args []string) (remoteLoginOptions, error) {
	fs := flag.NewFlagSet("remote login", flag.ContinueOnError)
	var opts remoteLoginOptions
	fs.StringVar(&opts.BaseURL, "url", "", "Base URL of the remote Buckley instance")
	fs.StringVar(&opts.Label, "label", strings.TrimSpace(os.Getenv("USER")), "Label shown to the reviewer")
	fs.BoolVar(&opts.NoBrowser, "no-browser", false, "Do not open the approval URL automatically")
	fs.DurationVar(&opts.Timeout, "timeout", 5*time.Minute, "Time to wait for browser approval")
	defaultToken := strings.TrimSpace(os.Getenv("BUCKLEY_IPC_TOKEN"))
	fs.StringVar(&opts.IPCAuthToken, "token", defaultToken, "API token (optional; browser auth preferred)")
	defaultUser := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_USER"))
	defaultPass := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_PASSWORD"))
	fs.StringVar(&opts.BasicUser, "basic-auth-user", defaultUser, "HTTP basic auth username")
	fs.StringVar(&opts.BasicPass, "basic-auth-pass", defaultPass, "HTTP basic auth password")
	fs.BoolVar(&opts.InsecureTLS, "insecure-skip-verify", false, "Skip TLS verification (development only)")
	if err := fs.Parse(args); err != nil {
		return remoteLoginOptions{}, fmt.Errorf("parsing login flags: %w", err)
	}
	if err := validateRemoteBaseOptions(opts.remoteBaseOptions); err != nil {
		return remoteLoginOptions{}, fmt.Errorf("validating remote options: %w", err)
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return remoteLoginOptions{}, fmt.Errorf("--url is required")
	}
	return opts, nil
}

func parseRemoteConsoleFlags(args []string) (remoteConsoleOptions, error) {
	fs := flag.NewFlagSet("remote console", flag.ContinueOnError)
	var opts remoteConsoleOptions
	fs.StringVar(&opts.BaseURL, "url", "", "Base URL of the remote Buckley instance")
	fs.StringVar(&opts.SessionID, "session", "", "Session ID to open a console for")
	fs.StringVar(&opts.Command, "cmd", "", "Optional command to run instead of the default shell")
	defaultToken := strings.TrimSpace(os.Getenv("BUCKLEY_IPC_TOKEN"))
	fs.StringVar(&opts.IPCAuthToken, "token", defaultToken, "IPC bearer token")
	defaultUser := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_USER"))
	defaultPass := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_PASSWORD"))
	fs.StringVar(&opts.BasicUser, "basic-auth-user", defaultUser, "HTTP basic auth username")
	fs.StringVar(&opts.BasicPass, "basic-auth-pass", defaultPass, "HTTP basic auth password")
	fs.BoolVar(&opts.InsecureTLS, "insecure-skip-verify", false, "Skip TLS verification (development only)")
	if err := fs.Parse(args); err != nil {
		return remoteConsoleOptions{}, fmt.Errorf("parsing console flags: %w", err)
	}
	if err := validateRemoteBaseOptions(opts.remoteBaseOptions); err != nil {
		return remoteConsoleOptions{}, fmt.Errorf("validating remote options: %w", err)
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return remoteConsoleOptions{}, fmt.Errorf("--url is required")
	}
	if strings.TrimSpace(opts.SessionID) == "" {
		return remoteConsoleOptions{}, fmt.Errorf("--session is required")
	}
	return opts, nil
}
