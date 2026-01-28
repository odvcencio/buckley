package servo

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	browserdpb "github.com/odvcencio/buckley/pkg/browser/adapters/servo/proto"
	"google.golang.org/protobuf/proto"
)

type client struct {
	conn net.Conn
	mu   sync.Mutex
}

func newClient(conn net.Conn) *client {
	return &client{conn: conn}
}

func (c *client) close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *client) send(ctx context.Context, req *browserdpb.Request) (*browserdpb.Response, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("browserd connection unavailable")
	}
	if req == nil {
		return nil, fmt.Errorf("request required")
	}
	if req.RequestId == "" {
		req.RequestId = uuid.NewString()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	env := &browserdpb.Envelope{
		Message: &browserdpb.Envelope_Request{Request: req},
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := applyDeadline(c.conn, ctx); err != nil {
		return nil, err
	}
	if err := writeEnvelope(c.conn, env); err != nil {
		return nil, err
	}
	for {
		respEnv, err := readEnvelope(c.conn)
		if err != nil {
			return nil, err
		}
		switch msg := respEnv.Message.(type) {
		case *browserdpb.Envelope_Response:
			return msg.Response, nil
		case *browserdpb.Envelope_Event:
			continue
		default:
			return nil, fmt.Errorf("unexpected browserd message")
		}
	}
}

func applyDeadline(conn net.Conn, ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		return conn.SetDeadline(deadline)
	}
	return conn.SetDeadline(time.Time{})
}

func writeEnvelope(conn net.Conn, env *browserdpb.Envelope) error {
	data, err := proto.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if len(data) > int(^uint32(0)) {
		return fmt.Errorf("message too large")
	}
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(data)))
	if _, err := conn.Write(lenBuf); err != nil {
		return err
	}
	_, err = conn.Write(data)
	return err
}

func readEnvelope(conn net.Conn) (*browserdpb.Envelope, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf)
	if length == 0 {
		return nil, fmt.Errorf("empty message")
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(conn, data); err != nil {
		return nil, err
	}
	env := &browserdpb.Envelope{}
	if err := proto.Unmarshal(data, env); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	return env, nil
}
