package main

import (
	"flag"
	"fmt"
	"net/http"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/gitwatcher"
	"github.com/odvcencio/buckley/pkg/regression"
)

var gitWebhookLoadConfigFn = config.Load
var gitWebhookListenFn = http.ListenAndServe

func runGitWebhookCommand(args []string) error {
	fs := flag.NewFlagSet("git-webhook", flag.ContinueOnError)
	bind := fs.String("bind", "", "address to bind the git webhook listener (default: git_events.webhook_bind or 127.0.0.1:8085)")
	secret := fs.String("secret", "", "shared secret for webhook validation (overrides config)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := gitWebhookLoadConfigFn()
	if err != nil {
		return withExitCode(err, 2)
	}
	if !cfg.GitEvents.Enabled {
		return withExitCode(fmt.Errorf("git events disabled in config; enable git_events.enabled to run this daemon"), 2)
	}

	addr := strings.TrimSpace(*bind)
	if addr == "" {
		addr = strings.TrimSpace(cfg.GitEvents.WebhookBind)
	}
	if addr == "" {
		addr = "127.0.0.1:8085"
	}

	webhookSecret := strings.TrimSpace(chooseSecret(*secret, cfg.GitEvents.Secret))
	if !isLoopbackAddress(addr) && webhookSecret == "" {
		return withExitCode(fmt.Errorf("refusing to bind git webhook listener to %q without a shared secret (set --secret or git_events.secret)", addr), 2)
	}

	pipeline := regression.NewPipeline(cfg.GitEvents)
	handler := gitwatcher.NewHandler(webhookSecret, pipeline.HandleMerge)

	fmt.Printf("Listening for git webhooks on %s\n", addr)
	return gitWebhookListenFn(addr, handler)
}

func chooseSecret(flagValue, cfgValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return cfgValue
}
