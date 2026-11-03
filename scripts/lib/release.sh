# Download a prebuilt docent release and install its binaries. Sourced by
# install-docent-linux.sh and install-docent-macos.sh, which define log(),
# run(), and DRY_RUN. Mirrors `docentd update` (libs/selfupdate): resolve the
# tag from the /releases/latest redirect, download docent_<os>_<arch>.tar.gz,
# and verify it against checksums.txt.
#
# Environment:
#   DOCENT_REPO           owner/repo to install from (default: KurtPreston/docent)
#   DOCENT_DOWNLOAD_BASE  release host, for a mirror (default: https://github.com)

DOCENT_RELEASE_DIR=""  # staging dir holding the extracted binaries
DOCENT_RELEASE_TAG=""  # the tag that was downloaded

# Prefer curl, fall back to wget. _rel_fetch_to URL FILE; _rel_location URL
# prints the first Location a redirect points at.
if command -v curl >/dev/null 2>&1; then
  _rel_fetch_to() { curl -fsSL "$1" -o "$2"; }
  _rel_location() { curl -sSI "$1" | tr -d '\r' | awk 'tolower($1) == "location:" { print $2; exit }'; }
elif command -v wget >/dev/null 2>&1; then
  _rel_fetch_to() { wget -qO "$2" "$1"; }
  _rel_location() {
    wget -S --spider "$1" 2>&1 | tr -d '\r' |
      awk 'tolower($1) == "location:" { print $2; exit }'
  }
else
  _rel_fetch_to() { echo "need curl or wget to download docent" >&2; return 1; }
  _rel_location() { return 1; }
fi

# _rel_platform prints the os_arch suffix GoReleaser uses in archive names.
_rel_platform() {
  local os arch
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) echo "no docent release for $(uname -s); use --from-source" >&2; return 1 ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) echo "no docent release for $(uname -m); use --from-source" >&2; return 1 ;;
  esac
  printf '%s_%s' "$os" "$arch"
}

_rel_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "need sha256sum or shasum to verify docent" >&2
    return 1
  fi
}

# docent_release_fetch [TAG] downloads and unpacks a release (the latest when
# TAG is empty) into DOCENT_RELEASE_DIR. Later calls reuse the first download.
docent_release_fetch() {
  [ -n "$DOCENT_RELEASE_TAG" ] && return 0
  local repo="${DOCENT_REPO:-KurtPreston/docent}"
  local host="${DOCENT_DOWNLOAD_BASE:-https://github.com}"
  local tag="${1:-}" platform loc
  host="${host%/}"
  platform="$(_rel_platform)" || return 1

  # Resolve the latest tag from the redirect, not the rate-limited API
  if [ -z "$tag" ]; then
    loc="$(_rel_location "$host/$repo/releases/latest" || true)"
    tag="$(printf '%s\n' "$loc" | sed -n 's#.*/releases/tag/\([^/?]*\).*#\1#p')"
    [ -n "$tag" ] || { echo "could not find a docent release in $repo (pass --version, or --from-source)" >&2; return 1; }
  fi
  local archive="docent_${platform}.tar.gz" base="$host/$repo/releases/download/$tag"
  if [ "$DRY_RUN" -eq 1 ]; then
    log "[dry-run] would download $base/$archive and verify it against checksums.txt"
    DOCENT_RELEASE_TAG="$tag"
    return 0
  fi

  # Failed probes may retry; reuse their staging directory, not their result.
  if [ -z "$DOCENT_RELEASE_DIR" ]; then
    DOCENT_RELEASE_DIR="$(mktemp -d)" || return 1
    trap 'rm -rf "$DOCENT_RELEASE_DIR"' EXIT
  fi
  log "downloading docent $tag ($platform)"
  _rel_fetch_to "$base/$archive" "$DOCENT_RELEASE_DIR/$archive" \
    || { echo "could not download $base/$archive" >&2; return 1; }

  # Verification is mandatory, including when a probe calls us in an if/||.
  local want got
  _rel_fetch_to "$base/checksums.txt" "$DOCENT_RELEASE_DIR/checksums.txt" \
    || { echo "could not download checksums.txt for $tag" >&2; return 1; }
  want="$(awk -v f="$archive" '$2 == f || $2 == "*" f { print tolower($1); exit }' "$DOCENT_RELEASE_DIR/checksums.txt")" || return 1
  [ -n "$want" ] || { echo "no checksum listed for $archive" >&2; return 1; }
  got="$(_rel_sha256 "$DOCENT_RELEASE_DIR/$archive")" || return 1
  if [ "$want" != "$got" ]; then
    echo "checksum mismatch for $archive (expected $want, got $got)" >&2
    return 1
  fi
  # Extract only release binaries; a retry must not reuse a partial extraction.
  local name
  for name in docentd docent-tunnel docent-reporter docent-setup; do
    rm -f "$DOCENT_RELEASE_DIR/$name" || return 1
  done
  tar -xzf "$DOCENT_RELEASE_DIR/$archive" -C "$DOCENT_RELEASE_DIR" \
    docentd docent-tunnel docent-reporter docent-setup || return 1
  for name in docentd docent-tunnel docent-reporter docent-setup; do
    if [ ! -f "$DOCENT_RELEASE_DIR/$name" ] || [ ! -s "$DOCENT_RELEASE_DIR/$name" ] || [ ! -x "$DOCENT_RELEASE_DIR/$name" ] || [ -L "$DOCENT_RELEASE_DIR/$name" ]; then
      echo "release $tag has no usable $name binary" >&2
      return 1
    fi
  done
  DOCENT_RELEASE_TAG="$tag"
}

_rel_install_binary() {
  local bindir="$1" name="$2" tmp
  tmp="$(mktemp "$bindir/.$name.XXXXXX")" || return 1
  if ! cp "$DOCENT_RELEASE_DIR/$name" "$tmp" \
    || ! chmod 0755 "$tmp" \
    || ! mv -f "$tmp" "$bindir/$name"; then
    rm -f "$tmp"
    echo "could not install $name in $bindir" >&2
    return 1
  fi
}

# docent_release_install BIN_DIR NAME... installs binaries from the fetched
# release. Each is copied beside its target and renamed over it, so a running
# binary is never written in place.
docent_release_install() {
  local bindir="$1" name; shift
  run mkdir -p "$bindir" || return 1
  if [ "$DRY_RUN" -eq 0 ]; then
    [ -n "$DOCENT_RELEASE_TAG" ] || { echo "no verified release downloaded" >&2; return 1; }
    for name in "$@"; do
      [ -f "$DOCENT_RELEASE_DIR/$name" ] || { echo "release has no $name binary" >&2; return 1; }
      if [ -e "$bindir/$name" ] && [ ! -f "$bindir/$name" ]; then
        echo "cannot replace $bindir/$name: not a regular file" >&2
        return 1
      fi
    done
  fi
  for name in "$@"; do
    if [ "$DRY_RUN" -eq 1 ]; then
      run install -m 0755 "<staging>/$name" "$bindir/$name"
      continue
    fi
    _rel_install_binary "$bindir" "$name" || return 1
    log "installed $name $DOCENT_RELEASE_TAG -> $bindir/$name"
  done
}
