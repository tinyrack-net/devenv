# syntax=docker/dockerfile:1
# devenv desktop image: base image + XFCE + TigerVNC.
#
# VNC listens on 127.0.0.1:5901 without a password. It is only reachable
# through the SSH tunnel opened by the devenv server, which authenticates the
# user, so the desktop is never exposed on the pod network.
ARG BASE_IMAGE=ghcr.io/tinyrack-net/devenv-base:latest
FROM ${BASE_IMAGE}

ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      xfce4 xfce4-terminal xfce4-taskmanager mousepad ristretto thunar-archive-plugin \
      tigervnc-standalone-server tigervnc-tools dbus-x11 x11-xserver-utils xauth \
      fonts-dejavu fonts-noto-cjk fonts-noto-color-emoji \
      adwaita-icon-theme-full elementary-xfce-icon-theme \
      firefox-esr- \
    && rm -rf /var/lib/apt/lists/*

COPY desktop.sh /etc/devenv/entrypoint.d/50-desktop.sh
COPY devenv-desktop /usr/local/bin/devenv-desktop
RUN chmod 0755 /usr/local/bin/devenv-desktop
