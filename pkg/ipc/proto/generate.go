package ipcpb

//go:generate protoc -I. --go_out=. --go_opt=paths=source_relative --connect-go_out=. --connect-go_opt=paths=source_relative ipc.proto
