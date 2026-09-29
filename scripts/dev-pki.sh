#!/usr/bin/env bash
# Local development CA and separate service identities; never overwrites keys.
set -euo pipefail
umask 077
if [[ $# -ne 1 ]]; then
  printf 'Usage: %s NEW_OUTPUT_DIRECTORY\n' "$0" >&2
  exit 2
fi
# EASYGO_PKI_DAYS sets both CA and service certificate lifetime. Without it the
# CA lasts 30 days and service certificates 7, which suits throwaway dev state.
ca_days=30
leaf_days=7
if [[ -n "${EASYGO_PKI_DAYS:-}" ]]; then
  if [[ ! "$EASYGO_PKI_DAYS" =~ ^[1-9][0-9]{0,4}$ ]]; then
    printf 'EASYGO_PKI_DAYS must be a whole number of days (1-99999)\n' >&2
    exit 2
  fi
  ca_days="$EASYGO_PKI_DAYS"
  leaf_days="$EASYGO_PKI_DAYS"
fi
out="$(realpath -m -- "$1")"
if [[ -e "$out" ]]; then
  printf 'Refusing to overwrite existing certificate directory: %s\n' "$out" >&2
  exit 1
fi
mkdir -p -- "$(dirname -- "$out")"
mkdir -- "$out"
mkdir -- "$out/.ca" "$out/public"
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$out/.ca/ca.key"
openssl req -x509 -new -sha256 -days "$ca_days" -key "$out/.ca/ca.key" \
  -subj '/CN=EasyGo Local Development CA' \
  -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
  -addext 'keyUsage=critical,keyCertSign,cRLSign' \
  -out "$out/public/ca.crt"
for identity in ai-gateway agent-loop workshop client; do
  dir="$out/$identity"
  mkdir -- "$dir"
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$dir/tls.key"
  openssl req -new -sha256 -key "$dir/tls.key" -subj "/CN=$identity" -out "$dir/request.csr"
  usage='serverAuth,clientAuth'
  if [[ "$identity" == client ]]; then usage='clientAuth'; fi
  cat > "$dir/extensions.cnf" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=$usage
subjectAltName=DNS:$identity,DNS:localhost,IP:127.0.0.1,IP:::1
EOF
  openssl x509 -req -sha256 -days "$leaf_days" -in "$dir/request.csr" \
    -CA "$out/public/ca.crt" -CAkey "$out/.ca/ca.key" \
    -CAserial "$out/.ca/serial" -CAcreateserial \
    -extfile "$dir/extensions.cnf" -out "$dir/tls.crt" 2>/dev/null
  cp -- "$dir/tls.crt" "$out/public/$identity.crt"
done
printf 'Development certificates created in %s\n' "$out"
printf 'Mount only each service identity directory and the public trust directory; keep .ca private.\n'
