// Package forward maintains an SSH local-forward from the workstation to a dev
// box: it listens on a local loopback port and forwards each connection to a
// loopback port on the dev box over SSH. It lets the docent launcher/dashboard
// reach a loopback-only remote docentd without relying on an external SSH
// session (e.g. Cursor Remote-SSH) to carry the forward.
//
// This mirrors the pattern in the separate wsm project's reverse tunnel, but in
// the opposite direction (local listen, remote dial). It is an independent copy
// — docent has no build dependency on wsm.
package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Config describes how to dial the dev box and which ports to bridge.
type Config struct {
	Host           string // SSH host of the dev box (required)
	Port           int    // SSH port (default 22)
	User           string // SSH user (default: current OS user)
	IdentityFile   string // private key path; empty falls back to ssh-agent
	KnownHostsFile string // host-key verification (default ~/.ssh/known_hosts)
	LocalAddr      string // local listen address, e.g. 127.0.0.1:39787
	RemoteAddr     string // remote docentd address on the dev box, e.g. 127.0.0.1:39787
	KeepAliveSec   int    // SSH keepalive interval in seconds (default 30)
}

// ApplyDefaults fills unset fields and expands a leading ~ in the path fields.
func (c *Config) ApplyDefaults() {
	if c.Port == 0 {
		c.Port = 22
	}
	if c.User == "" {
		c.User = currentUsername()
	}
	if c.LocalAddr == "" {
		c.LocalAddr = "127.0.0.1:39787"
	}
	if c.RemoteAddr == "" {
		c.RemoteAddr = "127.0.0.1:39787"
	}
	if c.KnownHostsFile == "" {
		if home, err := os.UserHomeDir(); err == nil {
			c.KnownHostsFile = filepath.Join(home, ".ssh", "known_hosts")
		}
	}
	if c.KeepAliveSec == 0 {
		c.KeepAliveSec = 30
	}
	c.IdentityFile = expandUser(c.IdentityFile)
	c.KnownHostsFile = expandUser(c.KnownHostsFile)
}

// Serve maintains the forward until ctx is cancelled, reconnecting with
// exponential backoff. Serve blocks. A failed or dropped connection is logged
// and retried, never fatal.
func Serve(ctx context.Context, cfg Config) {
	const (
		minBackoff = time.Second
		maxBackoff = 30 * time.Second
		stableFor  = 30 * time.Second
	)
	backoff := minBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := connectAndServe(ctx, cfg)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) >= stableFor {
			backoff = minBackoff
		}
		if err != nil {
			log.Printf("docent-tunnel: %v; retrying in %s", err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func connectAndServe(ctx context.Context, cfg Config) error {
	clientCfg, err := clientConfig(cfg)
	if err != nil {
		return err
	}
	sshAddr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	client, err := ssh.Dial("tcp", sshAddr, clientCfg)
	if err != nil {
		return fmt.Errorf("dial %s: %w", sshAddr, err)
	}
	defer client.Close()

	ln, err := net.Listen("tcp", cfg.LocalAddr)
	if err != nil {
		return fmt.Errorf("local listen %s: %w", cfg.LocalAddr, err)
	}
	defer ln.Close()

	log.Printf("docent-tunnel: up — %s -> %s@%s -> %s", cfg.LocalAddr, cfg.User, sshAddr, cfg.RemoteAddr)

	acceptErr := make(chan error, 1)
	go func() {
		for {
			local, err := ln.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			go handleConn(client, local, cfg.RemoteAddr)
		}
	}()

	closed := make(chan struct{})
	go func() { _ = client.Wait(); close(closed) }()

	interval := time.Duration(cfg.KeepAliveSec) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = ln.Close()
			return ctx.Err()
		case err := <-acceptErr:
			return fmt.Errorf("accept stopped: %w", err)
		case <-closed:
			_ = ln.Close()
			return errors.New("ssh connection closed")
		case <-ticker.C:
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				_ = ln.Close()
				return fmt.Errorf("keepalive failed: %w", err)
			}
		}
	}
}

// handleConn bridges one accepted local connection to the remote address over
// the SSH client.
func handleConn(client *ssh.Client, local net.Conn, remoteAddr string) {
	defer local.Close()
	remote, err := client.Dial("tcp", remoteAddr)
	if err != nil {
		log.Printf("docent-tunnel: dial remote %s: %v", remoteAddr, err)
		return
	}
	defer remote.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done
}

func clientConfig(cfg Config) (*ssh.ClientConfig, error) {
	auths, err := authMethods(cfg.IdentityFile)
	if err != nil {
		return nil, err
	}
	if len(auths) == 0 {
		return nil, errors.New("no ssh auth available: set -identity or start an ssh-agent (SSH_AUTH_SOCK)")
	}
	hostKeys, err := hostKeyCallback(cfg.KnownHostsFile)
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auths,
		HostKeyCallback: hostKeys,
		Timeout:         15 * time.Second,
	}, nil
}

// authMethods builds the SSH auth methods: an explicit identity file when set,
// plus the ssh-agent when SSH_AUTH_SOCK points at a reachable agent. For a
// login-launched daemon an identity file is the more reliable choice.
func authMethods(identityFile string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if identityFile != "" {
		key, err := os.ReadFile(identityFile)
		if err != nil {
			return nil, fmt.Errorf("read identity %s: %w", identityFile, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse identity %s (encrypted keys need an ssh-agent): %w", identityFile, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}
	return methods, nil
}

// hostKeyCallback verifies the dev box against known_hosts. There is no
// insecure fallback: the dev box is already trusted there whenever Cursor has
// connected to it.
func hostKeyCallback(knownHostsFile string) (ssh.HostKeyCallback, error) {
	if knownHostsFile == "" {
		return nil, errors.New("known_hosts file is required for host key verification")
	}
	cb, err := knownhosts.New(knownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("known_hosts %s: %w", knownHostsFile, err)
	}
	return cb, nil
}

// currentUsername returns the local login name, stripping a Windows DOMAIN\
// prefix. Real setups should pass -user explicitly; this is only a fallback.
func currentUsername() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	name := u.Username
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// expandUser expands a leading ~ or ~/ (or ~\ on Windows) to the home dir.
func expandUser(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
