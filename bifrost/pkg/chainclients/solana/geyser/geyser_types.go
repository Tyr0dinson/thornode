package geyser

import (
	"context"
)

// auth implements the credentials.PerRPCCredentials interface to support x-token authentication for grpc requests.
type tokenAuth struct {
	token string
}

func (a tokenAuth) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{
		"x-token": a.token,
	}, nil
}

func (tokenAuth) RequireTransportSecurity() bool {
	return false
}
