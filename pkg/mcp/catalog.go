package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	catalogListMethod     = "buckley/catalog/list"
	catalogProbeMethod    = "buckley/catalog/probe"
	maxCatalogPages       = 64
	maxCatalogCursorBytes = 64 << 10
)

var (
	ErrCatalogBudget     = errors.New("mcp: tool catalog exceeds discovery budget")
	ErrCatalogPagination = errors.New("mcp: invalid tool catalog pagination")
)

// ToolCatalogStatus describes the last successful bounded discovery snapshot.
type ToolCatalogStatus struct {
	Tools     int
	Bytes     int
	Pages     int
	Truncated bool
}

// Private SDK extension aliases keep protocol metadata/dispatch while avoiding
// the SDK's cursor-keyed page cache, which retains old cursor generations.
func installDiscoveryMethods(client *sdkmcp.Client) error {
	if err := sdkmcp.AddSendingCustomMethod[*sdkmcp.ListToolsParams, *sdkmcp.ListToolsResult](client, catalogListMethod); err != nil {
		return err
	}
	if err := sdkmcp.AddSendingCustomMethod[*sdkmcp.DiscoverParams, *sdkmcp.DiscoverResult](client, catalogProbeMethod); err != nil {
		return err
	}
	client.AddSendingMiddleware(func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, request sdkmcp.Request) (sdkmcp.Result, error) {
			switch method {
			case catalogListMethod:
				method = "tools/list"
			case catalogProbeMethod:
				method = "server/discover"
			}
			return next(ctx, method, request)
		}
	})
	return nil
}

// ListTools replaces the catalog only after a successful bounded discovery.
// Count limits return an explicitly partial snapshot; invalid pagination or
// excessive catalog bytes leave the previous working snapshot intact.
func (c *Client) ListTools(ctx context.Context) ([]*sdkmcp.Tool, error) {
	if c == nil {
		return nil, fmt.Errorf("mcp: nil client")
	}
	c.mu.RLock()
	session := c.session
	c.mu.RUnlock()
	if session == nil {
		return nil, fmt.Errorf("mcp: not connected")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	var all []*sdkmcp.Tool
	var status ToolCatalogStatus
	seen := make(map[[32]byte]bool)
	names := make(map[string]bool)
	cursor := ""
	for {
		if status.Pages == maxCatalogPages {
			return nil, fmt.Errorf("%w: more than %d pages", ErrCatalogBudget, maxCatalogPages)
		}
		res, err := sdkmcp.CallCustomMethod[*sdkmcp.ListToolsParams, *sdkmcp.ListToolsResult](ctx, session, catalogListMethod, &sdkmcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp: tools/list failed: %w", c.transport.connectionError(err))
		}
		status.Pages++
		take := min(len(res.Tools), c.cfg.MaxTools-len(all))
		for _, def := range res.Tools[:take] {
			if def == nil || def.Name == "" {
				return nil, fmt.Errorf("%w: missing tool name", ErrCatalogPagination)
			}
			if names[def.Name] {
				return nil, fmt.Errorf("%w: duplicate tool name", ErrCatalogPagination)
			}
			encoded, err := json.Marshal(def)
			if err != nil {
				return nil, fmt.Errorf("mcp: encode catalog tool: %w", err)
			}
			if len(encoded) > c.cfg.MaxMessageBytes-status.Bytes {
				return nil, fmt.Errorf("%w: tool JSON exceeds %d bytes", ErrCatalogBudget, c.cfg.MaxMessageBytes)
			}
			status.Bytes += len(encoded)
			names[def.Name] = true
			all = append(all, def)
		}
		if take < len(res.Tools) || (len(all) == c.cfg.MaxTools && res.NextCursor != "") {
			status.Truncated = true
			break
		}
		if res.NextCursor == "" {
			break
		}
		if len(res.NextCursor) > maxCatalogCursorBytes {
			return nil, fmt.Errorf("%w: cursor exceeds %d bytes", ErrCatalogBudget, maxCatalogCursorBytes)
		}
		hash := sha256.Sum256([]byte(res.NextCursor))
		if seen[hash] {
			return nil, fmt.Errorf("%w: repeated cursor", ErrCatalogPagination)
		}
		seen[hash] = true
		cursor = res.NextCursor
	}
	status.Tools = len(all)
	c.mu.Lock()
	report := status.Truncated && (!c.catalog.Truncated || c.catalog.Tools != status.Tools)
	c.tools, c.catalog = all, status
	c.mu.Unlock()
	if report {
		log.Printf("mcp: server %q catalog limited to %d tools; more tools available", c.cfg.Name, status.Tools)
	}
	return all, nil
}

func (c *Client) CatalogStatus() ToolCatalogStatus {
	if c == nil {
		return ToolCatalogStatus{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.catalog
}

// CheckHealth probes the connection without refreshing tool definitions.
func (c *Client) CheckHealth(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("mcp: nil client")
	}
	c.mu.RLock()
	session := c.session
	c.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("mcp: not connected")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	var err error
	if c.ProtocolVersion() >= "2026-07-28" {
		_, err = sdkmcp.CallCustomMethod[*sdkmcp.DiscoverParams, *sdkmcp.DiscoverResult](ctx, session, catalogProbeMethod, &sdkmcp.DiscoverParams{})
	} else {
		err = session.Ping(ctx, nil)
		var rpcErr *jsonrpc.Error
		if errors.As(err, &rpcErr) && rpcErr.Code == jsonrpc.CodeMethodNotFound {
			_, err = sdkmcp.CallCustomMethod[*sdkmcp.ListToolsParams, *sdkmcp.ListToolsResult](ctx, session, catalogListMethod, &sdkmcp.ListToolsParams{})
		}
	}
	if err != nil {
		return fmt.Errorf("mcp: health probe failed: %w", c.transport.connectionError(err))
	}
	return nil
}
