# Installation and operations

See the [README quick start](../README.md#install) for downloading Dinkle,
installing a release, and connecting Docker.

## Cluster requirements

- Kubernetes and a kubectl context with permission to install CRDs, namespaces,
  cluster RBAC, workloads, and Secrets.
- A default StorageClass for Dinki's registry PVC. Tenant volumes also depend
  on cluster storage.
- A CNI enforcing NetworkPolicies for tenant network isolation. Host-network
  containers bypass NetworkPolicies.
- Metrics Server for nonzero container statistics.
- Node access to GHCR and the other registries referenced by the manifests,
  or suitable image-pull credentials.
- Containerd configured with `/etc/containerd/certs.d` as its registry
  `config_path`.

The `dinki-node` DaemonSet configures node registry trust for `dinki.io`. It uses
host networking, runs as root, and mounts `/etc/containerd/certs.d`. The supplied
BuildKit backend runs privileged. Review this host access and the cluster RBAC
against your threat model before installing.

The manifests keep API and registry Services internal. Arrange private API
access using a load balancer or TCP/TLS-passthrough route to
`dink-system/dink:2376`. The client TLS connection must reach Dink unchanged.

## Release selection

Releases supporting the installer contain `install.yaml` and
`install.yaml.sha256`. Dink and Dinki references are pinned to the multi-platform
image digests built from the release commit. Historical releases without these
assets require their documented manual installation procedure.

`dinkle install` defaults to its binary's release, never `latest` or `main`.
Use `--version vX.Y.Z` to select another release; development builds require an
explicit version. The checksum detects corruption, not a compromised publisher.

## Certificates and installation

Installation creates missing certificates for Dink, Dinki, and BuildKit, applies
the manifest with kubectl, and waits for all four workloads. The packaged
namespaces are `dink-system` and `dink-default`; certificate DNS names assume
`cluster.local`. Custom namespaces and cluster DNS domains require a customized
installation.

`--kubeconfig` selects the same configuration for certificate operations and
kubectl; otherwise the ambient configuration is used. `--timeout` bounds the
complete operation and defaults to five minutes.

`--yes` acknowledges applying cluster-wide and privileged resources. The CA
private key is cached in `--certsDir` and stored in a cluster Secret. Restrict
access to both.

Rerunning installation preserves valid existing certificates and rejects a
conflicting local CA. Additional `--dnsName` values affect newly created Dink
certificates; existing certificates must be reissued explicitly with
`dinkle server issue`.

Create an independent client certificate for each client/operator:

```sh
dinkle client create team-a <client-name>
```

Distribute only that client's private key, certificate, and CA certificate.
Never distribute the CA private key to Docker clients.

## Upgrades and customization

Download the selected release's CLI and run `dinkle upgrade --yes`, or explicitly
select a release:

```sh
dinkle upgrade --yes --version <release-tag> --timeout 10m
```

Upgrades preserve CA and TLS Secrets. Missing, expired, or inconsistent
certificates cause an error; repair them using the CA/server commands first.

Install and upgrade apply the standard release configuration. They do not merge
custom ConfigMap/RBAC/workload edits, prune obsolete resources, perform automatic
rollback, or manage external exposure. For customized installations, download
and verify the release manifest, customize it, and use your normal deployment
tooling. Applying resources can partially succeed before an error; inspect the
failure before retrying.

## Configuration and persistence

- [API configuration](../deploy/config.json) controls TLS, logging, namespace
  defaults, BuildKit, node placement, and container resource defaults.
- [Registry configuration](../deploy/dinki-config.json) controls TLS, logging,
  storage, and internal API identity. `storage.path` selects the blob directory;
  `metadata.path` selects the bbolt database.
- [Deployment manifests](../deploy/) describe the standard cluster resources.
- Run `dinkle --help` for certificate, tenant, plugin, and secret operations.

The supplied Dink and Dinki Deployments each use one replica. Dinki uses a
`ReadWriteOnce` PVC and `Recreate` updates. Plan backups and recovery for registry
data; the manifests do not provide high availability or a backup policy.

PVC capacity is not a measurement of bytes used. API and host-specific
limitations are documented in the [translation contract](docker-translation-contract.md)
and [endpoint tracker](docker-api-conformance.md).
