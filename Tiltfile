# Dev loop for dink: builds the binary on the host, bakes it into a thin image,
# deploys to the local minikube cluster and port-forwards the docker API.
#
#   tilt up          # start the loop
#   tilt down        # tear everything down

allow_k8s_contexts('dink-dev')

IMAGE = 'dink'

# The Secret is a prerequisite for the Deployment: the pod blocks on mounting it.
local_resource(
    'certs',
    cmd='./deploy/gen-certs.sh',
    deps=['deploy/gen-certs.sh'],
    labels=['setup'],
)

local_resource(
    'compile',
    cmd='CGO_ENABLED=0 GOOS=linux go build -trimpath -o .tilt/dink ./cmd/dink',
    deps=['cmd', 'dink', 'sdk', 'go.mod', 'go.sum'],
    labels=['build'],
)

custom_build(
    IMAGE,
    './deploy/build-dev-image.sh "$EXPECTED_REF"',
    deps=['.tilt/dink', 'Dockerfile', 'deploy/build-dev-image.sh'],
    skips_local_docker=True,
)

k8s_yaml('deploy/dink.yaml')

k8s_resource(
    'dink',
    port_forwards='2376:2376',
    resource_deps=['certs', 'compile'],
    labels=['app'],
)
