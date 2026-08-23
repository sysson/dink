#!/usr/bin/env bash
set -euo pipefail

sudo apt-get update -y
sudo apt-get install -y --no-install-recommends ca-certificates curl openssl

go install github.com/bufbuild/buf/cmd/buf@latest

if ! command -v golangci-lint >/dev/null 2>&1; then
  curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.12.2
fi

CLUSTER_NAME="dink-dev"

minikube start -p "${CLUSTER_NAME}"

CERTS_DIR=".devcontainer/certs"
CA_CERT="${CERTS_DIR}/ca.crt"
CA_KEY="${CERTS_DIR}/ca.key"
SERVER_CERT="${CERTS_DIR}/server.crt"
SERVER_KEY="${CERTS_DIR}/server.key"
CLIENT_NAMES=(testing staging)

if [[ -f "${SERVER_CERT}" && -f "${SERVER_KEY}" && -f "${CA_CERT}" ]]; then
  echo "Dev TLS material already present in ${CERTS_DIR}."
else
  mkdir -p "${CERTS_DIR}"
  WORKDIR=$(mktemp -d)
  trap 'rm -rf "${WORKDIR}"' EXIT

  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
    -keyout "${CA_KEY}" -out "${CA_CERT}" -days 3650 -subj "/CN=dink-dev CA"

  cat > "${WORKDIR}/server-ext.cnf" <<EOF
subjectAltName = DNS:localhost,DNS:dink,IP:127.0.0.1,DNS:host.docker.internal
EOF
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
    -keyout "${SERVER_KEY}" -out "${WORKDIR}/server.csr" -subj "/CN=dink"
  openssl x509 -req -in "${WORKDIR}/server.csr" -CA "${CA_CERT}" -CAkey "${CA_KEY}" -CAcreateserial \
    -out "${SERVER_CERT}" -days 397 -extfile "${WORKDIR}/server-ext.cnf"

  for name in "${CLIENT_NAMES[@]}"; do
    openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
      -keyout "${CERTS_DIR}/client-${name}.key" -out "${WORKDIR}/client-${name}.csr" \
      -subj "/O=${name}/CN=dink-client-${name}"
    openssl x509 -req -in "${WORKDIR}/client-${name}.csr" -CA "${CA_CERT}" -CAkey "${CA_KEY}" -CAcreateserial \
      -out "${CERTS_DIR}/client-${name}.crt" -days 90
  done

  echo "Generated dev CA, server cert, and client certs (${CLIENT_NAMES[*]}) in ${CERTS_DIR}."
fi
