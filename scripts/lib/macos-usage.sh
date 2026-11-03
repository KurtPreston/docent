# Display the macOS installer's usage.

usage() {
  cat <<'EOF'
Usage: install-docent-macos.sh [options]

Installs docentd (optionally, locally) with a launchd LaunchAgent, the
Hammerspoon launcher, and Cursor hooks when Cursor.app is installed. The
window manager itself lives in the separate wsm project -- install it from
https://github.com/KurtPreston/wsm.

On first run, asks whether docentd runs on this Mac (local) or remotely.
Remote installs pick the SSH host from ~/.ssh/config (fzf when installed),
check that docentd answers there, and ask for a token only if it requires one.
Local installs download the latest docent release (docentd with the
dashboard embedded), run docent-setup when needed, and register its
LaunchAgent. Remote installs point hooks/launcher at the remote docentd URL
you provide, installing only docent-tunnel. On a re-run it first offers to reuse
the previous install's config (the local/remote choice, plus the remote
URL/token/tunnel host), so updating never forces you to re-pick the deployment
or retype credentials; DOCENTD_URL still overrides.

Go and Node are only needed with --from-source, which builds the binaries
(and the dashboard, embedded via -tags embed) from this checkout instead.
After installing a release, `docentd update` upgrades it in place.

Options:
  --hooks           Install Cursor hooks even if Cursor.app is not found
  --no-hooks        Skip ~/.cursor/hooks install
  --extension       Install the docent IDE extension into detected editors
  --no-extension    Skip the docent IDE extension (default is to ask on a TTY)
  --no-hammerspoon  Skip docent.lua install (~/.hammerspoon)
  --no-launchd      Skip launchd plist install (binaries only)
  --version TAG     Install this release (e.g. v0.1.0) instead of the latest
  --from-source     Build from this checkout (needs Go, and Node for local)
  --no-build        Install nothing (reuse existing binaries in BIN_DIR)
  --bin-dir PATH    Install binaries here (default: ~/.local/bin)
  --ssh-host HOST   SSH host for the docent-tunnel forward to a remote docentd
                    (default: picked from ~/.ssh/config). May be an
                    ~/.ssh/config alias: the tunnel honors its HostName, User,
                    IdentityFile, and ProxyCommand/ProxyJump (e.g. Coder).
  --ssh-identity P  SSH private key for the forward (default: the IdentityFile
                    from ~/.ssh/config, else ssh-agent)
  --no-tunnel       Don't set up the SSH forward; hit the remote URL directly
  --dry-run         Print actions without changing the system
  -h, --help        Show this help

Environment:
  DOCENT_BIN_DIR       Same as --bin-dir
  DOCENT_CONFIG_DIR    Config root (default: ~/.config/docent)
  DOCENTD_URL          Remote docentd base URL (implies remote; with --no-tunnel,
                       the URL to connect to directly)
  DOCENTD_TOKEN        Bearer token for remote docentd / hooks
  DOCENT_TUNNEL_HOST   Same as --ssh-host (implies remote mode)
  DOCENT_TUNNEL_IDENTITY  Same as --ssh-identity
  DOCENT_VERSION       Same as --version
  DOCENT_REPO          GitHub repo to download releases from (default: KurtPreston/docent)
  DOCENT_NODE          Node binary for the dashboard build (default: node; needs >= 18)
  DOCENT_NPM           npm binary (default: npm)
EOF
}
