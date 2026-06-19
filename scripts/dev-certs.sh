#!/usr/bin/env bash
set -euo pipefail

out_dir="${1:-./certs}"
mkdir -p "${out_dir}"

openssl genrsa -out "${out_dir}/ca.key" 4096
openssl req -x509 -new -nodes -key "${out_dir}/ca.key" -sha256 -days 3650 -out "${out_dir}/ca.crt" -subj "/CN=opsi-dev-ca"

openssl genrsa -out "${out_dir}/server.key" 2048
openssl req -new -key "${out_dir}/server.key" -out "${out_dir}/server.csr" -subj "/CN=localhost"
cat > "${out_dir}/server.ext" <<'EOF'
subjectAltName = DNS:localhost,IP:127.0.0.1
extendedKeyUsage = serverAuth
EOF
openssl x509 -req -in "${out_dir}/server.csr" -CA "${out_dir}/ca.crt" -CAkey "${out_dir}/ca.key" -CAcreateserial -out "${out_dir}/server.crt" -days 825 -sha256 -extfile "${out_dir}/server.ext"
openssl genrsa -out "${out_dir}/client.key" 2048
openssl req -new -key "${out_dir}/client.key" -out "${out_dir}/client.csr" -subj "/CN=opsi-cli"
cat > "${out_dir}/client.ext" <<'EOF'
extendedKeyUsage = clientAuth
EOF
openssl x509 -req -in "${out_dir}/client.csr" -CA "${out_dir}/ca.crt" -CAkey "${out_dir}/ca.key" -CAcreateserial -out "${out_dir}/client.crt" -days 825 -sha256 -extfile "${out_dir}/client.ext"

openssl x509 -in "${out_dir}/server.crt" -noout -fingerprint -sha256 | sed 's/^sha256 Fingerprint=//I' > "${out_dir}/server.sha256"

echo "created development certificates in ${out_dir}"
echo "server pin: $(cat "${out_dir}/server.sha256")"
