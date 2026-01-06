package acppb

//go:generate protoc -I/tmp/protoc/include -I. --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative types.proto acp.proto
