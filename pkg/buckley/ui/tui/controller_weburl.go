package tui

import (
	"fmt"
	"net"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
)

func resolveWebBaseURL(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if strings.TrimSpace(cfg.WebUI.BaseURL) != "" {
		return cfg.WebUI.BaseURL
	}
	if !cfg.IPC.Enabled || !cfg.IPC.EnableBrowser {
		return ""
	}
	bind := strings.TrimSpace(cfg.IPC.Bind)
	if bind == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		if strings.HasPrefix(bind, ":") {
			host = "127.0.0.1"
			port = strings.TrimPrefix(bind, ":")
		} else {
			return ""
		}
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if port == "" {
		return ""
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}
