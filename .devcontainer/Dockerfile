FROM mcr.microsoft.com/devcontainers/base:trixie

ARG KIND_VERSION=0.33.0
ARG KUBECTL_VERSION=1.37.0
ARG BUF_VERSION=1.72.0
ARG TILT_VERSION=0.37.7

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git make \
    && rm -rf /var/lib/apt/lists/*

COPY install-tools.sh /tmp/install-tools.sh
RUN bash /tmp/install-tools.sh && rm /tmp/install-tools.sh
