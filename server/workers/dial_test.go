// AREA: workers · DIAL · TEST
// Pure unit tests for remote-dial URL parsing and the bearer creds.
// No real network — these cover the policy bits (loopback gate,
// scheme handling, header shape) that we can break without noticing.

package workers

import (
	"context"
	"strings"
	"testing"
)

func TestParseRemoteURL_GrpcsTLS(t *testing.T) {
	target, useTLS, err := parseRemoteURL("grpcs://worker.example.com:7843")
	if err != nil {
		t.Fatalf("parseRemoteURL: %v", err)
	}
	if !useTLS {
		t.Error("grpcs:// should select TLS")
	}
	if target != "dns:///worker.example.com:7843" {
		t.Errorf("target = %q, want dns:///worker.example.com:7843", target)
	}
}

func TestParseRemoteURL_TcpLoopbackAllowed(t *testing.T) {
	cases := []string{
		"tcp://localhost:7843",
		"tcp://127.0.0.1:7843",
		"tcp://[::1]:7843",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, useTLS, err := parseRemoteURL(raw)
			if err != nil {
				t.Fatalf("parseRemoteURL: %v", err)
			}
			if useTLS {
				t.Error("tcp:// should be cleartext")
			}
		})
	}
}

func TestParseRemoteURL_TcpNonLoopbackRejected(t *testing.T) {
	// REASON: the most important policy in this file. Cleartext to
	// anything but loopback would leak the bearer token.
	_, _, err := parseRemoteURL("tcp://worker.example.com:7843")
	if err == nil {
		t.Fatal("tcp:// to non-loopback host must be rejected")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("error = %v, want mention of loopback policy", err)
	}
}

func TestParseRemoteURL_BadInputs(t *testing.T) {
	cases := map[string]string{
		"missing scheme": "worker.example.com:7843",
		"missing port":   "grpcs://worker.example.com",
		"unknown scheme": "http://worker.example.com:7843",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseRemoteURL(raw); err == nil {
				t.Fatalf("%q should be rejected", raw)
			}
		})
	}
}

func TestBearerToken_HeaderShape(t *testing.T) {
	b := newBearerToken("s3cret")
	md, err := b.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := md["authorization"]; got != "Bearer s3cret" {
		t.Errorf("authorization = %q, want %q", got, "Bearer s3cret")
	}
	if b.RequireTransportSecurity() {
		t.Error("RequireTransportSecurity must be false to allow loopback cleartext")
	}
}

func TestDialRemote_RejectsEmpty(t *testing.T) {
	if _, err := DialRemote(context.Background(), nil); err == nil {
		t.Fatal("nil endpoint must error")
	}
	if _, err := DialRemote(context.Background(), &RemoteEndpoint{}); err == nil {
		t.Fatal("empty URL must error")
	}
}
