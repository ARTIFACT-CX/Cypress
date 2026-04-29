// AREA: workers · DIAL · REMOTE
// Remote-worker flavor of a Handle. Dials a worker that someone else
// already started (a container on RunPod, a BYO GPU box, etc.) over
// gRPC + TLS, authenticating with a bearer token. No subprocess to
// reap; the returned *Grpc just owns the network connection.
//
// URL schemes accepted:
//   - grpcs://host:port — TLS with system roots (production default).
//   - tcp://host:port   — cleartext, refused unless the host resolves
//     to a loopback address (the SSH-tunnel escape hatch).
//
// Cleartext to a non-loopback target is refused on purpose: the bearer
// token would otherwise traverse the network in the clear.

package workers

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// RemoteEndpoint describes one external worker the local Cypress
// server should dial instead of spawning a subprocess. Built by the
// composition root from env vars (CYPRESS_REMOTE_URL / _TOKEN).
type RemoteEndpoint struct {
	// URL is the worker's listen address with scheme. See file header
	// for accepted forms.
	URL string
	// Token is the shared secret the remote worker's auth interceptor
	// validates. Sent as `Authorization: Bearer <token>` on every RPC.
	Token string
}

// DialRemote opens a gRPC channel to a remote worker and waits for
// the Handshake. Returns a *Grpc that satisfies Handle, identical
// in shape to a SpawnLocal result — the rest of the system can't
// tell the difference.
func DialRemote(ctx context.Context, ep *RemoteEndpoint) (*Grpc, error) {
	if ep == nil || ep.URL == "" {
		return nil, errors.New("DialRemote: URL is required")
	}

	target, useTLS, err := parseRemoteURL(ep.URL)
	if err != nil {
		return nil, err
	}

	var opts []grpc.DialOption
	if useTLS {
		// REASON: system roots are the right default. Self-signed certs
		// for hobbyist deployments can be supported later via a CA
		// bundle env var; not in scope for v0.1.
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	if ep.Token != "" {
		// REASON: RequireTransportSecurity=false because we deliberately
		// allow cleartext on loopback (SSH tunnels). The loopback check
		// in parseRemoteURL is what gates cleartext, not gRPC.
		opts = append(opts, grpc.WithPerRPCCredentials(newBearerToken(ep.Token)))
	}

	return dialGRPC(ctx, target, opts, nil, "")
}

// parseRemoteURL validates the scheme + host and returns the gRPC
// dial target plus whether TLS should be used. The dial target uses
// `dns:///` so gRPC's name resolver does the lookup (not us).
func parseRemoteURL(raw string) (target string, useTLS bool, err error) {
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", false, fmt.Errorf("parse remote URL %q: %w", raw, perr)
	}
	host := u.Hostname()
	port := u.Port()
	if host == "" || port == "" {
		return "", false, fmt.Errorf("remote URL %q: host:port required", raw)
	}

	switch strings.ToLower(u.Scheme) {
	case "grpcs":
		return "dns:///" + net.JoinHostPort(host, port), true, nil
	case "tcp":
		// SAFETY: refuse cleartext to anything that isn't loopback so
		// the bearer token never goes out unencrypted by accident.
		if !isLoopbackHost(host) {
			return "", false, fmt.Errorf("remote URL %q: cleartext tcp:// only allowed for loopback hosts; use grpcs://", raw)
		}
		return "dns:///" + net.JoinHostPort(host, port), false, nil
	default:
		return "", false, fmt.Errorf("remote URL %q: unsupported scheme %q (use grpcs:// or tcp://)", raw, u.Scheme)
	}
}

// isLoopbackHost returns true if `host` is "localhost" or any literal
// IP that sits in the loopback range. We look up nothing — DNS-named
// hosts other than "localhost" are treated as remote on purpose, since
// the user can't prove they don't resolve to a public address later.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// bearerToken implements credentials.PerRPCCredentials. The header
// map is built once at construction so each RPC just returns the
// same reference — gRPC treats it as read-only.
type bearerToken struct{ header map[string]string }

func newBearerToken(token string) bearerToken {
	return bearerToken{header: map[string]string{"authorization": "Bearer " + token}}
}

func (b bearerToken) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return b.header, nil
}

// RequireTransportSecurity returns false so we can attach the token on
// loopback cleartext (SSH tunnel). The loopback gate in parseRemoteURL
// is the actual policy; gRPC's check would be too strict for our case.
func (b bearerToken) RequireTransportSecurity() bool { return false }
