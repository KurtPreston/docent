package forward

import (
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// dialProxy starts cfg.ProxyCommand (after OpenSSH token expansion) and returns
// its stdin/stdout as a net.Conn carrying the SSH transport, like OpenSSH's
// ProxyCommand. The proxy's stderr goes to ours so failures show in the log.
func dialProxy(cfg Config) (net.Conn, error) {
	cmd := proxyShellCommand(expandProxyTokens(cfg.ProxyCommand, cfg))
	cmd.Stderr = os.Stderr

	// Wire the pipes before starting so the conn owns both ends.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	addr := proxyAddr(net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	return &proxyConn{cmd: cmd, w: stdin, r: stdout, addr: addr}, nil
}

// expandProxyTokens expands the OpenSSH tokens docent-tunnel knows about:
// %h host, %p port, %r user, %n original host alias, %% literal percent.
func expandProxyTokens(s string, cfg Config) string {
	alias := cfg.HostAlias
	if alias == "" {
		alias = cfg.Host
	}
	repl := strings.NewReplacer(
		"%%", "%",
		"%h", cfg.Host,
		"%p", strconv.Itoa(cfg.Port),
		"%r", cfg.User,
		"%n", alias,
	)
	return repl.Replace(s)
}

// proxyConn adapts a proxy process's stdio to net.Conn. Deadlines are no-ops;
// the SSH layer does not rely on them.
type proxyConn struct {
	cmd  *exec.Cmd
	w    io.WriteCloser
	r    io.Reader
	addr proxyAddr
	once sync.Once
}

func (c *proxyConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *proxyConn) Write(p []byte) (int, error) { return c.w.Write(p) }

// Close ends the proxy process once: closing stdin lets it exit cleanly, and a
// kill makes sure it does.
func (c *proxyConn) Close() error {
	c.once.Do(func() {
		_ = c.w.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_ = c.cmd.Wait()
	})
	return nil
}

func (c *proxyConn) LocalAddr() net.Addr              { return proxyAddr("proxy") }
func (c *proxyConn) RemoteAddr() net.Addr             { return c.addr }
func (c *proxyConn) SetDeadline(time.Time) error      { return nil }
func (c *proxyConn) SetReadDeadline(time.Time) error  { return nil }
func (c *proxyConn) SetWriteDeadline(time.Time) error { return nil }

// proxyAddr reports the SSH "host:port" so known_hosts checks see the target.
type proxyAddr string

func (a proxyAddr) Network() string { return "proxy" }
func (a proxyAddr) String() string  { return string(a) }
