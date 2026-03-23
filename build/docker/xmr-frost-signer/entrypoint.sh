#!/bin/sh
set -eu

data_dir="${XMR_FROST_DATA_DIR:-/var/lib/xmr-frost}"

mkdir -p "$data_dir"
chown -R frost:frost "$data_dir"
chmod 700 "$data_dir"

exec runuser -u frost -- /usr/local/bin/xmr-frost-signer "$@"
