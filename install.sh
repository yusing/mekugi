#!/bin/sh
set -eu

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) mekugi_platform=linux_amd64 ;;
  Linux/aarch64|Linux/arm64) mekugi_platform=linux_arm64 ;;
  Darwin/arm64) mekugi_platform=darwin_arm64 ;;
  *) printf 'Unsupported platform; build Mekugi from source instead\n' >&2; exit 1 ;;
esac

mekugi_archive="mekugi_${mekugi_platform}.tar.gz"
mekugi_release_url=https://github.com/yusing/mekugi/releases/latest/download
mekugi_install_dir="${HOME:?HOME must be set}/.local/bin"
mekugi_install_tmp=$(mktemp -d)
trap 'rm -rf "$mekugi_install_tmp"' 0
trap 'exit 1' HUP INT TERM
cd "$mekugi_install_tmp"

printf 'Downloading %s\n' "$mekugi_archive"
curl -fsSL "$mekugi_release_url/$mekugi_archive" -o "$mekugi_archive"
curl -fsSL "$mekugi_release_url/SHA256SUMS" -o SHA256SUMS
awk -v archive="$mekugi_archive" '
  $2 == archive || $2 == "./" archive { print; found = 1 }
  END {
    if (!found) {
      print "Missing checksum for " archive > "/dev/stderr"
      exit 1
    }
  }
' SHA256SUMS > checksum

case "$mekugi_platform" in
  darwin_*) shasum -a 256 -c checksum ;;
  *) sha256sum -c checksum ;;
esac

tar -xzf "$mekugi_archive" mekugi mekugi-exec
mkdir -p "$mekugi_install_dir"
install -m 755 mekugi mekugi-exec "$mekugi_install_dir/"
printf 'Installed mekugi and mekugi-exec in %s\n' "$mekugi_install_dir"
printf 'Add that directory to PATH if needed\n'
