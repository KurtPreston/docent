package forward

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestApplySSHConfigFillsUnsetFields(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	known := filepath.Join(dir, "known_hosts")
	for _, p := range []string{key, known} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// ssh -G lists defaults first; missing files and /dev/null are skipped.
	out := []byte("user kurt\nhostname main.ws.coder\nport 2222\n" +
		"identityfile " + filepath.Join(dir, "id_rsa") + "\n" +
		"identityfile " + key + "\n" +
		"userknownhostsfile /dev/null " + known + "\n" +
		"proxycommand /usr/local/bin/coder ssh --stdio %h\n")
	cfg := Config{Host: "workspace"}
	applySSHConfig(&cfg, parseSSHConfig(out))

	want := Config{
		Host: "main.ws.coder", HostAlias: "workspace", Port: 2222, User: "kurt",
		IdentityFile: key, KnownHostsFile: known,
		ProxyCommand: "/usr/local/bin/coder ssh --stdio %h",
	}
	if cfg != want {
		t.Errorf("applySSHConfig:\n got %+v\nwant %+v", cfg, want)
	}
}

func TestApplySSHConfigKeepsExplicitValues(t *testing.T) {
	out := []byte("hostname real\nport 2222\nuser other\nproxycommand nc %h %p\n")
	cfg := Config{Host: "alias", Port: 22, User: "me", ProxyCommand: "explicit"}
	applySSHConfig(&cfg, parseSSHConfig(out))
	if cfg.Port != 22 || cfg.User != "me" || cfg.ProxyCommand != "explicit" {
		t.Errorf("explicit values were overridden: %+v", cfg)
	}
}

func TestApplySSHConfigProxyJump(t *testing.T) {
	cases := map[string]string{
		"proxycommand none\nproxyjump bastion\n": `ssh -W "[%h]:%p" bastion`,
		"proxyjump a,b,c\n":                      `ssh -J a,b -W "[%h]:%p" c`,
		"proxyjump none\n":                       "",
	}
	for in, want := range cases {
		cfg := Config{Host: "h"}
		applySSHConfig(&cfg, parseSSHConfig([]byte(in)))
		if cfg.ProxyCommand != want {
			t.Errorf("%q: ProxyCommand = %q, want %q", in, cfg.ProxyCommand, want)
		}
	}
}

func TestExpandProxyTokens(t *testing.T) {
	cfg := Config{Host: "real.host", HostAlias: "alias", Port: 22, User: "me"}
	got := expandProxyTokens("x %h:%p %r %n 100%% %%h", cfg)
	if want := "x real.host:22 me alias 100% %h"; got != want {
		t.Errorf("expandProxyTokens = %q, want %q", got, want)
	}
}

// TestDialProxyStdio checks the proxy's stdio is wired as a conn by using `cat`
// as an echoing ProxyCommand.
func TestDialProxyStdio(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh and cat")
	}
	conn, err := dialProxy(Config{Host: "h", Port: 22, ProxyCommand: "cat"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Bytes written to the proxy's stdin come back on its stdout.
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Errorf("echo = %q, want ping", buf)
	}
	if got := conn.RemoteAddr().String(); got != "h:22" {
		t.Errorf("RemoteAddr = %q, want h:22", got)
	}
}
