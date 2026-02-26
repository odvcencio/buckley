package browserdpb

//go:generate protoc -I/tmp/protoc/include -I. --go_out=. --go_opt=paths=source_relative browserd.proto
