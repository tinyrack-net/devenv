# Sourced by devenv-entrypoint: start the desktop in the background as dev.
if [ "${DEVENV_DESKTOP:-true}" != "false" ]; then
  mkdir -p /tmp/.X11-unix && chmod 1777 /tmp/.X11-unix
  log "starting desktop on 127.0.0.1:5901"
  su -s /bin/bash dev -c 'nohup /usr/local/bin/devenv-desktop >/tmp/devenv-desktop.log 2>&1 &'
fi
