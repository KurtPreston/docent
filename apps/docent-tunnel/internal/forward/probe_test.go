package forward

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestProbeUnauthorized checks that a 401 maps to ErrUnauthorized (the
// installers' "ask for a token" signal) and a matching token passes.
func TestProbeUnauthorized(t *testing.T) {
	// docentd stand-in that requires a bearer token
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	// Route the probe's fixed http://docentd host to the test server
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
		},
	}}

	// No token: unauthorized; right token: ok
	if err := probe(context.Background(), client, "/api/config", ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("probe without token = %v, want ErrUnauthorized", err)
	}
	if err := probe(context.Background(), client, "/api/config", "secret"); err != nil {
		t.Fatalf("probe with token = %v, want nil", err)
	}
}
