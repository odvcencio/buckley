// Package grpcsdk hosts the lightweight gRPC bridge for Buckley's SDK.  It is
// intentionally minimal so downstream services can expose Buckley without
// waiting on a finalized protobuf schema; once the API stabilizes we can swap
// the JSON codec for generated stubs without changing callers.
package grpcsdk
