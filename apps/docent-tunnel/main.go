// Command docent-tunnel maintains an SSH local-forward on the workstation so
// the docent launcher/dashboard can reach a loopback-only remote docentd
// without relying on an external SSH session (e.g. Cursor Remote-SSH).
//
// It listens on a local loopback port (default 127.0.0.1:39787) and forwards
// each connection to the dev box's docentd loopback port over SSH. Installers
// run it as a background, auto-restarting service (Scheduled Task / launchd).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"

	"github.com/KurtPreston/docent/apps/docent-tunnel/internal/forward"
)

func main() {
	var cfg forward.Config
	flag.StringVar(&cfg.Host, "host", env("DOCENT_TUNNEL_HOST", ""), "SSH host of the dev box running docentd (required)")
	flag.IntVar(&cfg.Port, "port", envInt("DOCENT_TUNNEL_SSH_PORT", 22), "SSH port on the dev box")
	flag.StringVar(&cfg.User, "user", env("DOCENT_TUNNEL_USER", ""), "SSH user (default: current OS user)")
	flag.StringVar(&cfg.IdentityFile, "identity", env("DOCENT_TUNNEL_IDENTITY", ""), "SSH private key path; empty falls back to ssh-agent")
	flag.StringVar(&cfg.KnownHostsFile, "known-hosts", env("DOCENT_TUNNEL_KNOWN_HOSTS", ""), "known_hosts file (default ~/.ssh/known_hosts)")
	flag.StringVar(&cfg.LocalAddr, "local", env("DOCENT_TUNNEL_LOCAL", "127.0.0.1:39787"), "local listen address")
	flag.StringVar(&cfg.RemoteAddr, "remote", env("DOCENT_TUNNEL_REMOTE", "127.0.0.1:39787"), "remote docentd address (dev-box loopback)")
	flag.IntVar(&cfg.KeepAliveSec, "keepalive", envInt("DOCENT_TUNNEL_KEEPALIVE", 30), "SSH keepalive interval in seconds")
	flag.Parse()

	if cfg.Host == "" {
		log.Fatal("docent-tunnel: -host is required (the dev box running docentd; DOCENT_TUNNEL_HOST also works)")
	}
	cfg.ApplyDefaults()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
