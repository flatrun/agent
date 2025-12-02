#!/bin/sh
set -e

CERTS_DIR="${1:-/certs}"
DOMAIN="${2:-localhost}"

mkdir -p "$CERTS_DIR/live/$DOMAIN"

# Generate self-signed certificate for the domain
openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
    -keyout "$CERTS_DIR/live/$DOMAIN/privkey.pem" \
    -out "$CERTS_DIR/live/$DOMAIN/fullchain.pem" \
    -subj "/CN=$DOMAIN" \
    -addext "subjectAltName=DNS:$DOMAIN,DNS:*.$DOMAIN"

echo "Generated certificate for $DOMAIN"
