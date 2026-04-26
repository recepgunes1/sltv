// Package sltvclient is a thin wrapper around the auto-generated
// gRPC client for the SLTV service. It hides the dial mechanics
// (Unix sockets, mTLS) so callers only deal with the typed methods.
package sltvclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	pb "github.com/sltv/sltv/api/proto/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps a *grpc.ClientConn together with the generated stub.
type Client struct {
	conn *grpc.ClientConn
	pb.SltvClient
}

// Options configures Dial.
type Options struct {
	// Address is one of:
	//   - "unix:///run/sltv/sltvd.sock"
	//   - "host:port" (TCP, with TLS if TLS != nil)
	Address string
	// TLS, when non-nil, is used for TCP endpoints.
	TLS *tls.Config
	// DialTimeout, when non-zero, bounds the initial connection.
	DialTimeout time.Duration
}

// Dial connects to a sltvd endpoint.
func Dial(ctx context.Context, opts Options) (*Client, error) {
	if opts.Address == "" {
		return nil, errors.New("sltvclient: address is required")
	}
	dialOpts := []grpc.DialOption{}

	target := opts.Address
	switch {
	case strings.HasPrefix(opts.Address, "unix://"):
		u, err := url.Parse(opts.Address)
		if err != nil {
			return nil, fmt.Errorf("parse unix address: %w", err)
		}
		path := u.Path
		if path == "" {
			path = strings.TrimPrefix(opts.Address, "unix://")
		}
		dialOpts = append(dialOpts,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
				return net.Dial("unix", path)
			}),
		)
		target = "unix"
	case opts.TLS != nil:
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(opts.TLS)))
	default:
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	if opts.DialTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.DialTimeout)
		defer cancel()
	}
	_ = ctx

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", opts.Address, err)
	}
	return &Client{conn: conn, SltvClient: pb.NewSltvClient(conn)}, nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// LoadClientTLS reads a client cert/key plus a CA bundle into a
// *tls.Config suitable for use with mTLS server endpoints.
func LoadClientTLS(certPath, keyPath, caPath, serverName string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if certPath != "" || keyPath != "" {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load cert/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if caPath != "" {
		caBytes, err := os.ReadFile(caPath) //nolint:gosec
		if err != nil {
			return nil, fmt.Errorf("read ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, errors.New("ca file contained no PEM blocks")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}
