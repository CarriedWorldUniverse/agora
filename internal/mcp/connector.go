package mcp

import (
	"context"
	"fmt"
)

// StdioConnector is the production Connector for stdio-transport servers —
// the seam Manager documented as "another unit's concern". Non-stdio
// transports return a clear unsupported error (streamable_http is a later
// unit).
type StdioConnector struct{}

func (StdioConnector) Connect(ctx context.Context, cfg ServerConfig) (Client, error) {
	switch cfg.Transport {
	case TransportStdio, "":
		return newStdioClient(ctx, cfg)
	default:
		return nil, fmt.Errorf("mcp: transport %q not supported by StdioConnector (stdio only; streamable_http is a later unit)", cfg.Transport)
	}
}
