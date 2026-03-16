package consts

const (
	// MaxGRPCMessageSize is the maximum send/receive message size for gRPC
	// plugin communication. The default of 4MB is too small for large IaC projects.
	MaxGRPCMessageSize = 64 * 1024 * 1024 // 64MB
)