package forward

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApplyDefaults(t *testing.T) {
	c := Config{Host: "devbox"}
	c.ApplyDefaults()
	if c.Port != 22 {
		t.Errorf("ssh port default = %d, want 22", c.Port)
	}
	if c.LocalAddr != "127.0.0.1:39787" {
		t.Errorf("localAddr default = %q, want 127.0.0.1:39787", c.LocalAddr)
	}
	if c.RemoteAddr != "127.0.0.1:39787" {
		t.Errorf("remoteAddr default = %q, want 127.0.0.1:39787", c.RemoteAddr)
	}
	if c.KeepAliveSec != 30 {
		t.Errorf("keepAliveSec default = %d, want 30", c.KeepAliveSec)
	}
	if c.User == "" {
		t.Error("user default should be non-empty")
	}
}

func TestExpandUser(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir on this platform")
	}
	if got, want := expandUser("~/.ssh/id_ed25519"), filepath.Join(home, ".ssh", "id_ed25519"); got != want {
		t.Errorf("expandUser(~/...) = %q, want %q", got, want)
	}
	if got := expandUser("/abs/path"); got != "/abs/path" {
		t.Errorf("absolute path should be unchanged, got %q", got)
	}
	if got := expandUser(""); got != "" {
		t.Errorf("empty should stay empty, got %q", got)
	}
}

func TestAuthMethodsMissingIdentity(t *testing.T) {
	if _, err := authMethods(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error for a missing identity file")
	}
}

func TestAuthMethodsWithIdentity(t *testing.T) {
	path := writeTestKey(t)
	methods, err := authMethods(path)
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	if len(methods) == 0 {
		t.Fatal("expected a public-key auth method from the identity file")
	}
}

func TestClientConfigNoAuth(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	if _, err := clientConfig(Config{User: "me", KnownHostsFile: writeEmptyKnownHosts(t)}); err == nil {
		t.Fatal("expected error when no auth methods are available")
	}
}

func TestHostKeyCallbackRequiresFile(t *testing.T) {
	if _, err := hostKeyCallback(""); err == nil {
		t.Fatal("expected error when known_hosts file is empty")
	}
}

// TestServeStopsOnContextCancel verifies the supervisor honors context
// cancellation instead of retrying forever.
func TestServeStopsOnContextCancel(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	cfg := Config{
		Host:           "127.0.0.1",
		Port:           1, // nothing listens; connect fails fast
		User:           "nobody",
		KnownHostsFile: writeEmptyKnownHosts(t),
		LocalAddr:      "127.0.0.1:0",
		RemoteAddr:     "127.0.0.1:39787",
		KeepAliveSec:   1,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Serve(ctx, cfg)
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}

func writeTestKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeEmptyKnownHosts(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
