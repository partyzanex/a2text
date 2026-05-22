#!/usr/bin/env bash
#
# gen-certs.sh — generate the mTLS material a2textd / a2text need for
# loopback gRPC. Produces a self-signed root CA, a server certificate
# (subject a2textd, SAN 127.0.0.1 / ::1 / localhost) and a client
# certificate (subject a2text-ui). All artifacts land in ./certs/ by
# default; override with $CERT_DIR.
#
# Intended for local dev only. The CA private key is written to disk
# in plaintext — do not check it in, do not ship it. Production
# deployments must wire this material through a real PKI.
#
# Usage:
#   scripts/gen-certs.sh              # writes ./certs/
#   CERT_DIR=/tmp/a2text-certs scripts/gen-certs.sh
#
# Requires: openssl >= 1.1.1 (uses -addext).

set -euo pipefail

# -------- defaults ---------------------------------------------------------

CERT_DIR="${CERT_DIR:-./certs}"

CA_DAYS="${CA_DAYS:-3650}"      # 10 years — dev CA, rotate by deleting the dir.
LEAF_DAYS="${LEAF_DAYS:-365}"   # 1 year for the server and client certs.
KEY_BITS="${KEY_BITS:-3072}"    # RSA 3072: TLS 1.3 happy, faster than 4096.

CA_SUBJ="${CA_SUBJ:-/CN=a2text-dev-ca/O=a2text}"
SERVER_SUBJ="${SERVER_SUBJ:-/CN=a2textd/O=a2text}"
CLIENT_SUBJ="${CLIENT_SUBJ:-/CN=a2text-ui/O=a2text}"

SERVER_SAN="${SERVER_SAN:-DNS:localhost,IP:127.0.0.1,IP:::1}"

# -------- helpers ----------------------------------------------------------

log() { printf '[gen-certs] %s\n' "$*" >&2; }

require_openssl() {
    if ! command -v openssl >/dev/null 2>&1; then
        log "openssl is not on PATH"; exit 1
    fi
}

# write_or_skip <path> <description>
# Returns 1 (skipped) if the file already exists so the caller can
# avoid clobbering hand-rotated material.
write_or_skip() {
    if [[ -f "$1" ]]; then
        log "skip: $2 already exists ($1)"
        return 1
    fi
    return 0
}

# -------- main -------------------------------------------------------------

require_openssl

mkdir -p "$CERT_DIR"
chmod 700 "$CERT_DIR"

CA_KEY="$CERT_DIR/ca.key"
CA_CRT="$CERT_DIR/ca.crt"
SRV_KEY="$CERT_DIR/server.key"
SRV_CSR="$CERT_DIR/server.csr"
SRV_CRT="$CERT_DIR/server.crt"
CLI_KEY="$CERT_DIR/client.key"
CLI_CSR="$CERT_DIR/client.csr"
CLI_CRT="$CERT_DIR/client.crt"

# ---- CA -------------------------------------------------------------------

if write_or_skip "$CA_KEY" "CA key"; then
    log "generating root CA private key ($KEY_BITS bit RSA)"
    openssl genrsa -out "$CA_KEY" "$KEY_BITS" >/dev/null 2>&1
    chmod 600 "$CA_KEY"
fi

if write_or_skip "$CA_CRT" "CA certificate"; then
    log "self-signing root CA certificate ($CA_DAYS days)"
    openssl req -x509 -new -nodes -key "$CA_KEY" \
        -sha256 -days "$CA_DAYS" -subj "$CA_SUBJ" \
        -addext "basicConstraints=critical,CA:TRUE" \
        -addext "keyUsage=critical,keyCertSign,cRLSign" \
        -out "$CA_CRT"
fi

# ---- server ---------------------------------------------------------------

if write_or_skip "$SRV_KEY" "server key"; then
    log "generating server key"
    openssl genrsa -out "$SRV_KEY" "$KEY_BITS" >/dev/null 2>&1
    chmod 600 "$SRV_KEY"
fi

if write_or_skip "$SRV_CRT" "server certificate"; then
    log "issuing server CSR / certificate ($LEAF_DAYS days, SAN=$SERVER_SAN)"
    openssl req -new -key "$SRV_KEY" -subj "$SERVER_SUBJ" -out "$SRV_CSR"

    # extfile must live for the signing call only; clean up after.
    EXTFILE="$(mktemp)"
    {
        echo "basicConstraints=CA:FALSE"
        echo "keyUsage=critical,digitalSignature,keyEncipherment"
        echo "extendedKeyUsage=serverAuth"
        echo "subjectAltName=$SERVER_SAN"
    } > "$EXTFILE"

    openssl x509 -req -in "$SRV_CSR" \
        -CA "$CA_CRT" -CAkey "$CA_KEY" -CAcreateserial \
        -days "$LEAF_DAYS" -sha256 -extfile "$EXTFILE" \
        -out "$SRV_CRT"

    rm -f "$EXTFILE" "$SRV_CSR"
fi

# ---- client ---------------------------------------------------------------

if write_or_skip "$CLI_KEY" "client key"; then
    log "generating client key"
    openssl genrsa -out "$CLI_KEY" "$KEY_BITS" >/dev/null 2>&1
    chmod 600 "$CLI_KEY"
fi

if write_or_skip "$CLI_CRT" "client certificate"; then
    log "issuing client CSR / certificate ($LEAF_DAYS days)"
    openssl req -new -key "$CLI_KEY" -subj "$CLIENT_SUBJ" -out "$CLI_CSR"

    EXTFILE="$(mktemp)"
    {
        echo "basicConstraints=CA:FALSE"
        echo "keyUsage=critical,digitalSignature,keyEncipherment"
        echo "extendedKeyUsage=clientAuth"
    } > "$EXTFILE"

    openssl x509 -req -in "$CLI_CSR" \
        -CA "$CA_CRT" -CAkey "$CA_KEY" -CAcreateserial \
        -days "$LEAF_DAYS" -sha256 -extfile "$EXTFILE" \
        -out "$CLI_CRT"

    rm -f "$EXTFILE" "$CLI_CSR"
fi

# remove the openssl serial side-file; not needed once leaves are signed.
rm -f "$CERT_DIR/ca.srl"

log "done. material written to $CERT_DIR"
log ""
log "run daemon with mTLS:"
log "  a2textd \\"
log "    --cert      $SRV_CRT \\"
log "    --key       $SRV_KEY \\"
log "    --client-ca $CA_CRT"
log ""
log "client material:"
log "  cert: $CLI_CRT"
log "  key:  $CLI_KEY"
log "  ca:   $CA_CRT  (also the server's trust anchor)"
