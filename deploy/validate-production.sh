#!/bin/sh
set -eu

case "${ETH402_APP_IMAGE:-}" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *)
    echo "ETH402_APP_IMAGE must be an immutable repository@sha256 digest" >&2
    exit 1
    ;;
esac

digest=${ETH402_APP_IMAGE##*@sha256:}
case "$digest" in
  *[!0-9a-fA-F]*)
    echo "ETH402_APP_IMAGE digest must contain exactly 64 hexadecimal characters" >&2
    exit 1
    ;;
esac

if [ ! -f "${ETH402_APP_ENV_FILE:-}" ]; then
  echo "ETH402_APP_ENV_FILE must identify a readable host file" >&2
  exit 1
fi
if [ ! -f "${ETH402_MIGRATION_ENV_FILE:-}" ]; then
  echo "ETH402_MIGRATION_ENV_FILE must identify a readable host file" >&2
  exit 1
fi

docker compose -f deploy/compose.production.yaml config --quiet
docker run --rm --read-only --env-file "$ETH402_APP_ENV_FILE" \
  "$ETH402_APP_IMAGE" -check-config
docker run --rm -v "$PWD/deploy/Caddyfile.production:/etc/caddy/Caddyfile:ro" \
  caddy:2.10.0-alpine@sha256:ae4458638da8e1a91aafffb231c5f8778e964bca650c8a8cb23a7e8ac557aa3c \
  caddy validate --config /etc/caddy/Caddyfile
