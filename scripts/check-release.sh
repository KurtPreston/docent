#!/usr/bin/env bash
# Verify every supported release archive contains both entry points and tools.
set -euo pipefail

dir="${1:-dist}"
for os in linux darwin; do
  for arch in amd64 arm64; do
    archive="$dir/docent_${os}_${arch}.tar.gz"
    contents="$(tar -tzf "$archive")"
    for binary in docent docentd docent-tunnel docent-reporter docent-setup; do
      if ! grep -Fxq "$binary" <<<"$contents"; then
        echo "$archive is missing $binary" >&2
        exit 1
      fi
    done
    echo "verified $archive"
  done
done
