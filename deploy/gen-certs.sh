#!/usr/bin/env bash
#
# Generates a self-signed CA, a dink server certificate and a client
# certificate, then stores them in a Kubernetes Secret for the dink Deployment
# to mount. The client bundle is also written in the layout docker(1) expects
# via DOCKER_CERT_PATH.
#
# The CA private key is deliberately never uploaded: the cluster only ever holds
# the server keypair and the public ca.crt used to verify docker clients.
#
# Usage:
#   deploy/gen-certs.sh                  # generate (if missing) and apply the Secret
#   FORCE=1 deploy/gen-certs.sh          # regenerate everything
#   APPLY=0 deploy/gen-certs.sh          # generate locally only, do not touch the cluster
#   KEY_TYPE=ed25519 deploy/gen-certs.sh # ecdsa (default) | ed25519 | rsa
#   EXTRA_SANS="DNS:dink.example.com,IP:10.0.0.5" deploy/gen-certs.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

NAMESPACE="${NAMESPACE:-dink-system}"
SECRET_NAME="${SECRET_NAME:-dink-tls}"
SERVICE_NAME="${SERVICE_NAME:-dink}"
CLUSTER_DOMAIN="${CLUSTER_DOMAIN:-cluster.local}"
OUT_DIR="${OUT_DIR:-${REPO_ROOT}/.certs}"
CA_DAYS="${CA_DAYS:-3650}"
DAYS="${DAYS:-365}"
FORCE="${FORCE:-0}"
APPLY="${APPLY:-1}"
KEY_TYPE="${KEY_TYPE:-ecdsa}"
RSA_BITS="${RSA_BITS:-3072}"
EXTRA_SANS="${EXTRA_SANS:-}"

log() { printf '==> %s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

command -v openssl >/dev/null || die "openssl not found on PATH"

# Ed25519 carries its own hash, and openssl rejects an explicit -sha256 with it.
case "${KEY_TYPE}" in
ecdsa)
	KEY_ARGS=(-algorithm EC -pkeyopt ec_paramgen_curve:P-256)
	DIGEST=(-sha256)
	LEAF_KU="critical,digitalSignature"
	;;
ed25519)
	KEY_ARGS=(-algorithm ED25519)
	DIGEST=()
	LEAF_KU="critical,digitalSignature"
	;;
rsa)
	KEY_ARGS=(-algorithm RSA -pkeyopt "rsa_keygen_bits:${RSA_BITS}")
	DIGEST=(-sha256)
	LEAF_KU="critical,digitalSignature,keyEncipherment"
	;;
*)
	die "unsupported KEY_TYPE ${KEY_TYPE}: must be ecdsa, ed25519 or rsa"
	;;
esac

genkey() { openssl genpkey "${KEY_ARGS[@]}" -out "$1" 2>/dev/null; }

SANS="DNS:${SERVICE_NAME}"
SANS+=",DNS:${SERVICE_NAME}.${NAMESPACE}"
SANS+=",DNS:${SERVICE_NAME}.${NAMESPACE}.svc"
SANS+=",DNS:${SERVICE_NAME}.${NAMESPACE}.svc.${CLUSTER_DOMAIN}"
SANS+=",DNS:localhost,IP:127.0.0.1"
[[ -n "${EXTRA_SANS}" ]] && SANS+=",${EXTRA_SANS}"

if [[ "${FORCE}" == "1" ]]; then
	log "FORCE=1, removing ${OUT_DIR}"
	rm -rf "${OUT_DIR}"
fi

mkdir -p "${OUT_DIR}/docker"
chmod 700 "${OUT_DIR}" "${OUT_DIR}/docker"

# --- CA ----------------------------------------------------------------------
if [[ ! -f "${OUT_DIR}/ca.crt" || ! -f "${OUT_DIR}/ca.key" ]]; then
	log "generating CA (${CA_DAYS}d, ${KEY_TYPE})"
	# Extensions go through -extensions rather than -addext: openssl already
	# applies a default basicConstraints to self-signed certs, and the duplicate
	# makes the certificate unusable as an issuer.
	CA_EXT="$(mktemp)"
	trap 'rm -f "${CA_EXT}"' EXIT
	cat >"${CA_EXT}" <<-'EOF'
		[req]
		distinguished_name = dn
		[dn]
		[ca_ext]
		basicConstraints = critical,CA:TRUE,pathlen:0
		keyUsage = critical,keyCertSign,cRLSign
		subjectKeyIdentifier = hash
	EOF

	genkey "${OUT_DIR}/ca.key"
	openssl req -x509 -new "${DIGEST[@]}" -days "${CA_DAYS}" \
		-key "${OUT_DIR}/ca.key" \
		-subj "/O=dink/CN=dink-ca" \
		-config "${CA_EXT}" -extensions ca_ext \
		-out "${OUT_DIR}/ca.crt"
else
	log "reusing existing CA in ${OUT_DIR}"
fi

# issue <name> <key> <cert> <subject> <extendedKeyUsage> <subjectAltName>
issue() {
	local name="$1" key="$2" crt="$3" subj="$4" eku="$5" san="$6"
	local csr="${OUT_DIR}/${name}.csr"

	log "issuing ${name} certificate (${DAYS}d)"
	genkey "${key}"
	openssl req -new -key "${key}" -subj "${subj}" -out "${csr}"
	openssl x509 -req -in "${csr}" "${DIGEST[@]}" -days "${DAYS}" \
		-CA "${OUT_DIR}/ca.crt" -CAkey "${OUT_DIR}/ca.key" -CAcreateserial \
		-extfile <(printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=%s\nextendedKeyUsage=%s\nsubjectAltName=%s\n' "${LEAF_KU}" "${eku}" "${san}") \
		-out "${crt}" 2>/dev/null
	rm -f "${csr}"
}

# --- server ------------------------------------------------------------------
if [[ ! -f "${OUT_DIR}/tls.crt" || ! -f "${OUT_DIR}/tls.key" ]]; then
	issue server "${OUT_DIR}/tls.key" "${OUT_DIR}/tls.crt" \
		"/O=dink/CN=${SERVICE_NAME}.${NAMESPACE}.svc" serverAuth "${SANS}"
else
	log "reusing existing server certificate in ${OUT_DIR}"
fi

# --- client ------------------------------------------------------------------
if [[ ! -f "${OUT_DIR}/client.crt" || ! -f "${OUT_DIR}/client.key" ]]; then
	issue client "${OUT_DIR}/client.key" "${OUT_DIR}/client.crt" \
		"/O=dink/CN=dink-client" clientAuth "DNS:dink-client"
else
	log "reusing existing client certificate in ${OUT_DIR}"
fi

# docker looks for exactly these filenames under DOCKER_CERT_PATH.
cp "${OUT_DIR}/ca.crt" "${OUT_DIR}/docker/ca.pem"
cp "${OUT_DIR}/client.crt" "${OUT_DIR}/docker/cert.pem"
cp "${OUT_DIR}/client.key" "${OUT_DIR}/docker/key.pem"

chmod 600 "${OUT_DIR}"/*.key "${OUT_DIR}/docker/key.pem"
chmod 644 "${OUT_DIR}"/*.crt "${OUT_DIR}/docker/ca.pem" "${OUT_DIR}/docker/cert.pem"

if [[ "${APPLY}" != "1" ]]; then
	log "APPLY=0, skipping Secret creation"
	exit 0
fi

command -v kubectl >/dev/null || die "kubectl not found on PATH (re-run with APPLY=0 to only generate files)"

log "ensuring namespace ${NAMESPACE}"
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

log "applying secret ${NAMESPACE}/${SECRET_NAME}"
kubectl create secret generic "${SECRET_NAME}" \
	--namespace "${NAMESPACE}" \
	--from-file=tls.crt="${OUT_DIR}/tls.crt" \
	--from-file=tls.key="${OUT_DIR}/tls.key" \
	--from-file=ca.crt="${OUT_DIR}/ca.crt" \
	--dry-run=client -o yaml |
	kubectl label --local -f - -o yaml \
		app.kubernetes.io/name=dink \
		app.kubernetes.io/component=tls |
	kubectl apply -f - >/dev/null

cat >&2 <<EOF

done. certificates in ${OUT_DIR}, secret ${NAMESPACE}/${SECRET_NAME} applied.

to talk to dink from this machine:

  kubectl -n ${NAMESPACE} port-forward svc/${SERVICE_NAME} 2376:2376 &
  export DOCKER_HOST=tcp://localhost:2376
  export DOCKER_TLS_VERIFY=1
  export DOCKER_CERT_PATH=${OUT_DIR}/docker
  docker info
EOF
