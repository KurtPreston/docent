// Command docent-tunnel maintains an SSH local-forward on the workstation so
// the docent launcher/dashboard can reach a loopback-only remote docentd
// without relying on an external SSH session (e.g. Cursor Remote-SSH).
//
// The host may be an ~/.ssh/config alias: unless -ssh-config=false, it is
// resolved through `ssh -G` (HostName, Port, User, IdentityFile,
// UserKnownHostsFile, ProxyCommand/ProxyJump) so the tunnel connects the same
// way `ssh <host>` does. Explicit flags override the resolved values.
//
// It listens on a local loopback port (default 127.0.0.1:39787) and forwards
// each connection to the dev box's docentd loopback port over SSH. Installers
// run it as a background, auto-restarting service (Scheduled Task / launchd).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/KurtPreston/docent/apps/docent-tunnel/internal/forward"
)

func main() {
	var cfg forward.Config
	var check, useSSHConfig, listHosts bool
	var token, checkPath string
	flag.StringVar(&cfg.Host, "host", env("DOCENT_TUNNEL_HOST", ""), "SSH host or ~/.ssh/config alias of the dev box running docentd (required)")
	flag.IntVar(&cfg.Port, "port", envInt("DOCENT_TUNNEL_SSH_PORT", 0), "SSH port on the dev box (default: ssh_config, else 22)")
	flag.StringVar(&cfg.User, "user", env("DOCENT_TUNNEL_USER", ""), "SSH user (default: ssh_config, else current OS user)")
	flag.StringVar(&cfg.IdentityFile, "identity", env("DOCENT_TUNNEL_IDENTITY", ""), "SSH private key path (default: ssh_config IdentityFile, else ssh-agent)")
	flag.StringVar(&cfg.KnownHostsFile, "known-hosts", env("DOCENT_TUNNEL_KNOWN_HOSTS", ""), "known_hosts file (default: ssh_config UserKnownHostsFile, else ~/.ssh/known_hosts)")
	flag.StringVar(&cfg.ProxyCommand, "proxy-command", env("DOCENT_TUNNEL_PROXY_COMMAND", ""), "OpenSSH-style ProxyCommand (%h %p %r %n tokens; default: ssh_config ProxyCommand/ProxyJump)")
	flag.BoolVar(&useSSHConfig, "ssh-config", envBool("DOCENT_TUNNEL_SSH_CONFIG", true), "resolve -host through `ssh -G` to honor ~/.ssh/config")
	flag.StringVar(&cfg.LocalAddr, "local", env("DOCENT_TUNNEL_LOCAL", "127.0.0.1:39787"), "local listen address")
	flag.StringVar(&cfg.RemoteAddr, "remote", env("DOCENT_TUNNEL_REMOTE", "127.0.0.1:39787"), "remote docentd address (dev-box loopback)")
	flag.IntVar(&cfg.KeepAliveSec, "keepalive", envInt("DOCENT_TUNNEL_KEEPALIVE", 30), "SSH keepalive interval in seconds")
	flag.BoolVar(&check, "check", false, "run a one-shot connectivity check (SSH + remote docentd) and exit non-zero on failure")
	flag.StringVar(&token, "token", env("DOCENT_TUNNEL_TOKEN", ""), "bearer token for the -check authed request (DOCENT_TUNNEL_TOKEN also works)")
	flag.StringVar(&checkPath, "check-path", "/api/config", "authed docentd path to probe during -check")
	flag.BoolVar(&listHosts, "list-hosts", false, "print the concrete Host aliases in ~/.ssh/config (alias<TAB>hostname per line) and exit")
	flag.Parse()

	// -list-hosts feeds the installers' SSH host picker.
	if listHosts {
		if err := printSSHHosts(); err != nil {
			log.Fatalf("docent-tunnel: list hosts: %v", err)
		}
		return
	}

	if cfg.Host == "" {
		log.Fatal("docent-tunnel: -host is required (the dev box running docentd; DOCENT_TUNNEL_HOST also works)")
	}

	// Honor ~/.ssh/config like `ssh <host>`; a missing ssh binary is not fatal.
	if useSSHConfig {
		alias := cfg.Host
		if err := forward.ResolveSSHConfig(context.Background(), &cfg); err != nil {
			log.Printf("docent-tunnel: not using ssh_config: %v", err)
		} else if cfg.Host != alias {
			log.Printf("docent-tunnel: resolved %s -> %s via ssh_config", alias, cfg.Host)
		}
	}
	cfg.ApplyDefaults()

	if check {
		if err := forward.Check(context.Background(), cfg, token, checkPath); err != nil {
			log.Printf("docent-tunnel: check failed: %v", err)
			// Exit 3 means "reached docentd, token rejected" so installers can ask for one.
			if errors.Is(err, forward.ErrUnauthorized) {
				os.Exit(3)
			}
			os.Exit(1)
		}
		log.Printf("docent-tunnel: check ok — %s@%s:%d reaches docentd at %s", cfg.User, cfg.Host, cfg.Port, cfg.RemoteAddr)
		return
	}

	// launchd stops services with SIGTERM; exit cleanly so the proxy is closed too.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("docent-tunnel: forwarding %s -> %s@%s:%d %s", cfg.LocalAddr, cfg.User, cfg.Host, cfg.Port, cfg.RemoteAddr)
	forward.Serve(ctx, cfg)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// printSSHHosts prints each concrete Host alias in ~/.ssh/config (and its
// Includes) as "alias<TAB>hostname".
func printSSHHosts() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	hosts, err := forward.ListSSHHosts(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return err
	}
	for _, h := range hosts {
		fmt.Printf("%s\t%s\n", h.Alias, h.HostName)
	}
	return nil
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
