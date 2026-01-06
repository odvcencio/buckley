package ipc

import (
	"context"
	"time"

	"nhooyr.io/websocket"
)

const (
	wsPingInterval = 20 * time.Second
	wsPingTimeout  = 5 * time.Second
)

func startWSPing(ctx context.Context, conn *websocket.Conn) {
	if conn == nil {
		return
	}
	ticker := time.NewTicker(wsPingInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, cancel := context.WithTimeout(ctx, wsPingTimeout)
				_ = conn.Ping(pingCtx)
				cancel()
			}
		}
	}()
}
