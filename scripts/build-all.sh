#!/bin/sh
# Run under the matching SDK fakeroot for root ownership in APK metadata.
set -eu
: "${STAGING_DIR_HOST:?Set STAGING_DIR_HOST to SDK/staging_dir/host}"
cd "$(dirname "$0")/.."
for target in arm64:aarch64_cortex-a53 arm:arm_cortex-a7_neon-vfpv4 mips:mips_24kc mipsle:mipsel_24kc amd64:x86_64; do
    python3 scripts/build-release.py --arch "${target%%:*}" --openwrt-arch "${target#*:}" \
        --apk "$STAGING_DIR_HOST/bin/apk" --go "${DNS_SCOUT_GO:-go}"
done
