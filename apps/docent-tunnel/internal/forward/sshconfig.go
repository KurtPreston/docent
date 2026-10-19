package forward

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ResolveSSHConfig fills unset Config fields from `ssh -G <host>`, so the tunnel
// connects the same way `ssh <host>` does: HostName, Port, User, IdentityFile,
// UserKnownHostsFile, and ProxyCommand (or ProxyJump). Values already set on cfg
// (explicit flags/env) win. Call it before ApplyDefaults.
func ResolveSSHConfig(ctx context.Context, cfg *Config) error {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}

	// `ssh -G` prints the fully evaluated config (Host/Match blocks applied).
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, sshBin, "-G", cfg.Host).Output()
	if err != nil {
		return fmt.Errorf("ssh -G %s: %w", cfg.Host, err)
	}
	applySSHConfig(cfg, parseSSHConfig(out))
	return nil
}

// parseSSHConfig parses `ssh -G` output ("key value..." per line, lowercase
// keys) into an ordered list of values per key.
func parseSSHConfig(out []byte) map[string][]string {
	kv := map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		key, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok {
			continue
		}
		key = strings.ToLower(key)
		kv[key] = append(kv[key], strings.TrimSpace(val))
	}
	return kv
}

// applySSHConfig merges parsed `ssh -G` values into the unset fields of cfg.
func applySSHConfig(cfg *Config, kv map[string][]string) {
	first := func(key string) string {
		if v := kv[key]; len(v) > 0 {
			return v[0]
		}
		return ""
	}

	// Swap the alias for its real HostName, remembering the alias for %n.
	if cfg.HostAlias == "" {
		cfg.HostAlias = cfg.Host
	}
	if h := first("hostname"); h != "" {
		cfg.Host = h
	}
	if cfg.Port == 0 {
		if p, err := strconv.Atoi(first("port")); err == nil {
			cfg.Port = p
		}
	}
	if cfg.User == "" {
		cfg.User = first("user")
	}

	// Pick the first identity / known_hosts file that actually exists.
	if cfg.IdentityFile == "" {
		cfg.IdentityFile = firstExisting(kv["identityfile"])
	}
	if cfg.KnownHostsFile == "" {
		cfg.KnownHostsFile = firstExisting(kv["userknownhostsfile"])
	}

	// ProxyCommand wins over ProxyJump, matching OpenSSH; "none" disables both.
	if cfg.ProxyCommand == "" {
		if pc := first("proxycommand"); pc != "" && pc != "none" {
			cfg.ProxyCommand = pc
		} else if pj := first("proxyjump"); pj != "" && pj != "none" {
			cfg.ProxyCommand = proxyJumpCommand(pj)
		}
	}
}

// proxyJumpCommand turns a ProxyJump spec ("a" or "a,b,c") into the equivalent
// ProxyCommand: `ssh [-J a,b] -W "[%h]:%p" c`. The spawned ssh reads ssh_config,
// so jump-host aliases resolve normally.
func proxyJumpCommand(spec string) string {
	hops := strings.Split(spec, ",")
	last := hops[len(hops)-1]
	cmd := "ssh"
	if len(hops) > 1 {
		cmd += " -J " + strings.Join(hops[:len(hops)-1], ",")
	}
	return cmd + ` -W "[%h]:%p" ` + last
}

// firstExisting returns the first path (after ~ expansion) that exists as a
// regular file, skipping /dev/null and "none".
func firstExisting(paths []string) string {
	for _, list := range paths {
		for _, p := range strings.Fields(list) {
			if p == "none" || p == os.DevNull {
				continue
			}
			p = expandUser(p)
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				return p
			}
		}
	}
	return ""
}
