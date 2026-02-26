package server

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// statusError wraps grpc status construction for server handlers.
func statusError(code codes.Code, msg string) error {
	return status.Error(code, msg)
}
