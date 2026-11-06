# syntax=docker/dockerfile:1
# devenv base image: Ubuntu + OpenSSH + common developer tools.
#
# Image contract (see docs/images.md):
#   - user "dev" (uid/gid 1000) with passwordless sudo
#   - /home/dev is a persistent volume, populated from /etc/skel on first start
#   - sshd on port 22, keys from /etc/devenv/ssh
#   - processes started by /usr/local/bin/devenv-entrypoint
FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      openssh-server sudo ca-certificates curl wget git vim nano less \
      bash-completion locales tzdata tini procps iproute2 iputils-ping \
      dnsutils netcat-openbsd htop tmux unzip zip xz-utils jq build-essential \
      python3 python3-pip python3-venv openssh-client gnupg lsb-release \
    && locale-gen en_US.UTF-8 \
    && rm -rf /var/lib/apt/lists/*

ENV LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8

# Ubuntu images ship user "ubuntu" with uid 1000; replace it with "dev".
RUN userdel -r ubuntu 2>/dev/null || true \
    && groupadd -g 1000 dev \
    && useradd -m -u 1000 -g 1000 -s /bin/bash dev \
    && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev \
    && chmod 0440 /etc/sudoers.d/dev \
    && passwd -d dev \
    && cp -a /home/dev/. /etc/skel/ 2>/dev/null || true

RUN mkdir -p /run/sshd /etc/devenv/entrypoint.d \
    && rm -f /etc/ssh/ssh_host_*
COPY sshd_config /etc/ssh/sshd_config.d/devenv.conf
COPY devenv-entrypoint devenv-authorized-keys /usr/local/bin/
RUN chmod 0755 /usr/local/bin/devenv-entrypoint /usr/local/bin/devenv-authorized-keys

EXPOSE 22
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/devenv-entrypoint"]
