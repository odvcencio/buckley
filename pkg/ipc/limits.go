package ipc

const (
	maxConnectReadBytes    = 32 << 20
	maxConnectRequestBytes = 64 << 20

	maxEventStreamClients = 128
	maxPTYClients         = 8

	maxGRPCSubscribersTotal        = 256
	maxGRPCSubscribersPerPrincipal = 16

	maxWSReadBytesEventStream = 64 << 10
	maxWSReadBytesPTY         = 8 << 20
)
