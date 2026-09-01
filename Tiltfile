# Dev loop for dink: builds the binary on the host, bakes it into a thin image,
# deploys to the local minikube cluster and port-forwards the docker API.
#
#   tilt up          # start the loop
#   tilt down        # tear everything down

allow_k8s_contexts('dink-dev')

IMAGE='ghcr.io/sysson/dink'

# The Secret is a prerequisite for the Deployment: the pod blocks on mounting it.
local_resource(
    'certs',
    cmd='make certs',
    deps=['dink/pkg/certs'],
    labels=['setup'],
)

custom_build(
    IMAGE,
    'make image-minikube REF=$EXPECTED_REF',
    deps=['Dockerfile', 'cmd', 'dink', 'sdk', 'go.mod', 'go.sum'],
    skips_local_docker=True,
)

# configMapGenerator reads config.json, but kustomize() does not report it as a dep.
watch_file('deploy/config.json')

k8s_yaml(kustomize('deploy'))

k8s_resource(
    'dink',
    port_forwards='2376:2376',
    resource_deps=['certs'],
    labels=['app'],
)
