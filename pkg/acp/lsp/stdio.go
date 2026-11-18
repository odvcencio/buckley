package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/oklog/ulid/v2"
)

// ServeStdio processes editor requests and keeps reading while coordinator
// work is in progress so cancellation reaches the active RPC.
func (b *Bridge) ServeStdio(parent context.Context, reader io.Reader, writer io.Writer) error {
	ctx, stop := context.WithCancelCause(parent)
	var workers sync.WaitGroup
	defer func() {
		stop(nil)
		workers.Wait()
	}()
	buf := bufio.NewReader(reader)
	var writeMu sync.Mutex
	send := func(msg *JSONRPCMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if err := WriteMessage(writer, msg); err != nil {
			err = fmt.Errorf("failed to write response: %w", err)
			stop(err)
			return err
		}
		return nil
	}
	var pendingMu sync.Mutex
	pending := make(map[string]context.CancelFunc)
	first := make(chan struct{})
	close(first)
	previous := first

	for {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		msg, err := ReadMessage(buf)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("failed to read message: %w", err)
		}
		if msg.ID == nil {
			switch msg.Method {
			case "$/cancelRequest":
				var params CancelParams
				if json.Unmarshal(msg.Params, &params) == nil {
					pendingMu.Lock()
					if cancel := pending[editorRequestKey(params.ID)]; cancel != nil {
						cancel()
					}
					pendingMu.Unlock()
				}
				continue
			case "exit":
				return b.Exit(ctx)
			}
		}
		if msg.ID == nil || (msg.Method != "buckley/textQuery" && msg.Method != "buckley/streamQuery") {
			if response := b.HandleMessage(ctx, msg); response != nil {
				if err := send(response); err != nil {
					return err
				}
			}
			continue
		}

		key := editorRequestKey(*msg.ID)
		pendingMu.Lock()
		if pending[key] != nil {
			pendingMu.Unlock()
			if err := send(&JSONRPCMessage{ID: msg.ID, Error: &JSONRPCError{Code: InvalidRequest, Message: "request ID already pending"}}); err != nil {
				return err
			}
			continue
		}
		requestCtx, cancel := context.WithCancel(ctx)
		pending[key] = cancel
		pendingMu.Unlock()

		// Preserve request order for editor tasks sharing a workspace.
		ready, finished := previous, make(chan struct{})
		previous = finished
		workers.Go(func() {
			defer close(finished)
			defer cancel()
			select {
			case <-ready:
			case <-ctx.Done():
			}
			var response *JSONRPCMessage
			switch {
			case requestCtx.Err() != nil:
				response = &JSONRPCMessage{ID: msg.ID, Error: editorQueryError(requestCtx, requestCtx.Err())}
			case msg.Method == "buckley/streamQuery":
				response = b.serveStreamQuery(requestCtx, msg, send)
			default:
				response = b.HandleMessage(requestCtx, msg)
			}
			pendingMu.Lock()
			delete(pending, key)
			pendingMu.Unlock()
			_ = send(response)
		})
	}
}

func editorRequestKey(id json.RawMessage) string {
	var text string
	if strings.HasPrefix(string(id), `"`) && json.Unmarshal(id, &text) == nil {
		return "string:" + text
	}
	return strings.TrimSpace(string(id))
}

func (b *Bridge) serveStreamQuery(ctx context.Context, msg *JSONRPCMessage, send func(*JSONRPCMessage) error) *JSONRPCMessage {
	response := &JSONRPCMessage{ID: msg.ID}
	var params StreamQueryParams
	if json.Unmarshal(msg.Params, &params) != nil || strings.TrimSpace(params.Query) == "" {
		response.Error = &JSONRPCError{Code: InvalidParams, Message: "streamQuery requires a query"}
		return response
	}
	streamID := ulid.Make().String()
	_, err := b.handleStreamQuery(ctx, params.Query, streamID, func(chunk string, done bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, _ := json.Marshal(StreamChunkNotification{StreamID: streamID, RequestID: msg.ID, Chunk: chunk, Done: done})
		return send(&JSONRPCMessage{Method: "buckley/streamChunk", Params: data})
	})
	if err != nil {
		response.Error = editorQueryError(ctx, err)
		return response
	}
	response.Result, _ = json.Marshal(StreamQueryResult{StreamID: streamID, AgentID: b.config.AgentID})
	return response
}
