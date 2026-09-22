#!/bin/sh
# DNS Scout installer for OpenWrt. Does not change DNS Scout settings.
set -eu
umask 077
repo='DarkSidr/dns-scout'
tag=''
base=''
local_dir=''
dry_run=0
update_index=0
usage() {
    cat <<'HELP'
DNS Scout installer
  sh install.sh [--version 0.2.0-r1] [--repo OWNER/REPO]
                [--release-url https://host/release] [--local-dir DIR]
                [--dry-run] [--update-index]
Default: latest GitHub release of DarkSidr/dns-scout.
--local-dir installs an offline release containing release.json, SHA256SUMS and packages.
--dry-run verifies files and prints the package, without installing it.
HELP
}
die() { printf 'DNS Scout: %s\n' "$*" >&2; exit 1; }
need_value() { [ "$#" -ge 2 ] && [ -n "$2" ] || die "Missing value for $1"; }
while [ "$#" -gt 0 ]; do
    case "$1" in
        --version) need_value "$@"; tag=$2; shift 2;;
        --repo) need_value "$@"; repo=$2; shift 2;;
        --release-url) need_value "$@"; base=$2; shift 2;;
        --local-dir) need_value "$@"; local_dir=$2; shift 2;;
        --dry-run) dry_run=1; shift;;
        --update-index) update_index=1; shift;;
        --help|-h) usage; exit 0;;
        *) die "Unknown option: $1";;
    esac
done
[ -r /etc/openwrt_release ] || die 'This installer requires OpenWrt.'
[ "$dry_run" = 1 ] || [ "$(id -u)" = 0 ] || die 'Run as root.'
command -v jsonfilter >/dev/null 2>&1 || die 'jsonfilter is required.'
command -v sha256sum >/dev/null 2>&1 || die 'sha256sum is required.'
arch=$(sed -n "s/^DISTRIB_ARCH='\([^']*\)'/\1/p" /etc/openwrt_release)
case "$arch" in
    aarch64_cortex-a53|arm_cortex-a7_neon-vfpv4|mips_24kc|mipsel_24kc|x86_64) ;;
    *) die "No prebuilt package for architecture: $arch. Build with the matching OpenWrt SDK.";;
esac
if command -v apk >/dev/null 2>&1; then manager=apk
elif command -v opkg >/dev/null 2>&1; then manager=opkg
else die 'Neither apk nor opkg was found.'; fi
if [ -n "$local_dir" ]; then
    [ -z "$base" ] && [ -z "$tag" ] || die '--local-dir cannot be combined with --version/--release-url.'
    local_dir=$(cd "$local_dir" && pwd) || die 'Local release directory not found.'
else
    if [ -z "$base" ]; then
        printf '%s\n' "$repo" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || die 'Invalid repository.'
        if [ -n "$tag" ]; then
            printf '%s\n' "$tag" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+-r[0-9]+$' || die 'Version must have the form 0.2.0-r1.'
            base="https://github.com/$repo/releases/download/v$tag"
        else base="https://github.com/$repo/releases/latest/download"; fi
    fi
    case "$base" in https://*) ;; *) die 'Release URL must use HTTPS.';; esac
    base=${base%/}
fi
work=$(mktemp -d /tmp/dns-scout-install.XXXXXX) || die 'Cannot create temporary directory.'
cleanup() { rm -rf "$work"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
fetch() {
    name=$1
    if [ -n "$local_dir" ]; then
        cp "$local_dir/$name" "$work/$name" || die "Missing local file: $name"
    elif command -v curl >/dev/null 2>&1; then
        curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
            --connect-timeout 10 --max-time 120 --retry 2 "$base/$name" -o "$work/$name" || die "Download failed: $name"
    elif command -v wget >/dev/null 2>&1; then
        wget -T 120 -O "$work/$name" "$base/$name" || die "Download failed: $name (check CA certificates)"
    else die 'Install curl or wget first.'; fi
    [ -s "$work/$name" ] || die "Empty file: $name"
}
verify() {
    file=$1
    expected=$(awk -v f="$file" '$2==f {print $1}' "$work/SHA256SUMS")
    [ "${#expected}" = 64 ] || die "Missing or duplicate checksum for $file"
    case "$expected" in *[!0-9a-f]*) die "Invalid checksum for $file";; esac
    actual=$(sha256sum "$work/$file" | awk '{print $1}')
    [ "$actual" = "$expected" ] || die "Checksum mismatch: $file; installation stopped."
}
fetch release.json
fetch SHA256SUMS
verify release.json
version=$(jsonfilter -i "$work/release.json" -e '@.version')
printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+-r[0-9]+$' || die 'Invalid release version.'
[ -z "$tag" ] || [ "$tag" = "$version" ] || die 'Requested version does not match release metadata.'
if [ "$manager" = apk ]; then package="luci-app-dns-scout-$version.$arch.apk"
else package="luci-app-dns-scout_${version}_${arch}.ipk"; fi
fetch "$package"
verify "$package"
printf 'DNS Scout %s; architecture %s; package %s\n' "$version" "$arch" "$package"
[ "$dry_run" = 0 ] || { printf 'Checksums OK. Dry run: no changes made.\n'; exit 0; }
# Do not overwrite an active test or interrupted DNS transaction during an upgrade.
[ ! -f /etc/dns-scout/transaction/pending ] || die 'Run dns-scout recover before upgrading.'
if command -v dns-scout >/dev/null 2>&1; then
    running=$(printf '{}' | dns-scout rpc call status | jsonfilter -e '@.job.running')
    [ "$running" != true ] || die 'Wait for the current DNS Scout operation to finish.'
fi
if [ "$update_index" = 1 ]; then "$manager" update; fi
if [ "$manager" = apk ]; then apk add --allow-untrusted "$work/$package"
else opkg install "$work/$package"; fi
installed=$(dns-scout version)
[ "$installed" = "$version" ] || die "Installed binary version mismatch: $installed"
printf 'Installed DNS Scout %s. Open LuCI → Services → DNS Scout.\n' "$version"
