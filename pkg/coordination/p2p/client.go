package p2p

import (
	"context"
	"fmt"

	"github.com/odvcencio/buckley/pkg/coordination/reliability"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Connection represents a P2P connection interface
type Connection interface {
	SendMessage(ctx context.Context, data []byte) error
	Close() error
}

// P2PClient manages peer-to-peer connections with circuit breaker protection
type P2PClient struct {
	endpoint string
	breaker  *reliability.CircuitBreaker
	conn     Connection
}

// NewP2PClient creates a new P2P client with circuit breaker
func NewP2PClient(endpoint string, breaker *reliability.CircuitBreaker) *P2PClient {
	return &P2PClient{
		endpoint: endpoint,
		breaker:  breaker,
	}
}

// Connect establishes a connection to the P2P endpoint
func (c *P2PClient) Connect(ctx context.Context) error {
	// Establish gRPC connection
	grpcConn, err := grpc.DialContext(ctx, c.endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", c.endpoint, err)
	}

	// Wrap in our connection interface
	c.conn = &grpcConnection{conn: grpcConn}
	return nil
}

// SendMessage sends data through the circuit breaker
func (c *P2PClient) SendMessage(ctx context.Context, data []byte) error {
	if c.conn == nil {
		return fmt.Errorf("not connected")
	}

	// Execute through circuit breaker
	return c.breaker.Execute(func() error {
		return c.conn.SendMessage(ctx, data)
	})
}

// Close closes the connection
func (c *P2PClient) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// grpcConnection wraps a gRPC connection
type grpcConnection struct {
	conn *grpc.ClientConn
}

// SendMessage sends a message over the gRPC connection
func (g *grpcConnection) SendMessage(ctx context.Context, data []byte) error {
	// In a real implementation, this would call a gRPC method
	// For now, we'll just return nil as this is a skeleton
	return nil
}

// Close closes the gRPC connection
func (g *grpcConnection) Close() error {
	return g.conn.Close()
}
