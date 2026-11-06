# syntax=docker/dockerfile:1
# devenv agent image: base image + common toolchains, so AI agents (and
# people) do not spend minutes installing them in every environment.
#
#   Go, Node.js (corepack: pnpm/yarn), Python with uv and the GitHub CLI.
# Toolchains live outside /home/dev so they are part of the image (not the
# persistent volume) and are upgraded with the image.
ARG BASE_IMAGE=ghcr.io/tinyrack-net/devenv-base:latest
FROM ${BASE_IMAGE}

ARG TARGETARCH
ARG GO_VERSION=1.27.3
ARG NODE_VERSION=26.13.0
ARG UV_VERSION=0.13.7
ARG GH_VERSION=2.103.3
ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
      sqlite3 pkg-config libssl-dev make cmake shellcheck \
    && rm -rf /var/lib/apt/lists/*

# Go
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xz

# Node.js (+ corepack for pnpm and yarn)
RUN case "${TARGETARCH}" in amd64) a=x64 ;; arm64) a=arm64 ;; esac \
    && curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${a}.tar.xz" \
       | tar -C /usr/local --strip-components=1 --no-same-owner --no-overwrite-dir -xJ \
    && npm install -g corepack@latest >/dev/null 2>&1 \
    && corepack enable

# uv (Python package and project manager)
RUN case "${TARGETARCH}" in amd64) a=x86_64 ;; arm64) a=aarch64 ;; esac \
    && curl -fsSL "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-${a}-unknown-linux-gnu.tar.gz" \
       | tar -C /usr/local/bin --strip-components=1 --no-same-owner --no-overwrite-dir -xz

# GitHub CLI
RUN curl -fsSL "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" \
       | tar -C /usr/local --strip-components=1 --no-same-owner --no-overwrite-dir -xz --wildcards '*/bin/gh'

# sshd refuses AuthorizedKeysCommand when its directory is writable by
# others or not owned by root; make sure archives did not change that.
RUN chown root:root /usr/local /usr/local/bin && chmod 0755 /usr/local /usr/local/bin

# PATH for login shells (bash -lc used by exec) and for the dev user's tools.
RUN printf '%s\n' \
      'export PATH="/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin:$PATH"' \
      'export COREPACK_ENABLE_DOWNLOAD_PROMPT=0' \
      > /etc/profile.d/devenv-toolchains.sh
ENV PATH="/usr/local/go/bin:${PATH}"
