import type { Environment, Info } from "./api";

/** How to reach an environment over SSH. */
interface SSHTarget {
  host: string;
  port: number;
  user: string;
  /** Direct (dedicated IP) or through the gateway. */
  direct: boolean;
}

export function sshTargets(env: Environment, info: Info | undefined): SSHTarget[] {
  const out: SSHTarget[] = [];
  if (env.ip) out.push({ host: env.ip, port: 22, user: "dev", direct: true });
  if (info?.ssh.enabled) {
    out.push({ host: info.ssh.host || window.location.hostname, port: info.ssh.port, user: env.name, direct: false });
  }
  return out;
}

export function sshCommand(t: SSHTarget): string {
  return `ssh ${t.port !== 22 ? `-p ${t.port} ` : ""}${t.user}@${t.host}`;
}

/** Host alias used in ~/.ssh/config and editor links. */
function hostAlias(env: Environment): string {
  return `devenv-${env.name}`;
}

export function sshConfig(env: Environment, t: SSHTarget): string {
  return [
    `Host ${hostAlias(env)}`,
    `  HostName ${t.host}`,
    `  User ${t.user}`,
    ...(t.port !== 22 ? [`  Port ${t.port}`] : []),
    `  ForwardAgent yes`,
    `  ServerAliveInterval 30`,
  ].join("\n");
}

export function knownHostsLine(info: Info | undefined, t: SSHTarget): string | undefined {
  if (!info?.ssh.hostKey || t.direct) return undefined;
  const host = t.port === 22 ? t.host : `[${t.host}]:${t.port}`;
  return `${host} ${info.ssh.hostKey}`;
}

/** Editor deep links via Remote-SSH (needs the host alias in ~/.ssh/config). */
export function editorLinks(env: Environment): { name: string; url: string }[] {
  const alias = hostAlias(env);
  return [
    { name: "VS Code", url: `vscode://vscode-remote/ssh-remote+${alias}/home/dev?windowId=_blank` },
    { name: "Cursor", url: `cursor://vscode-remote/ssh-remote+${alias}/home/dev?windowId=_blank` },
    { name: "Windsurf", url: `windsurf://vscode-remote/ssh-remote+${alias}/home/dev?windowId=_blank` },
  ];
}

export function portForward(t: SSHTarget, port: number): string {
  return `ssh -N -L ${port}:localhost:${port} ${t.port !== 22 ? `-p ${t.port} ` : ""}${t.user}@${t.host}`;
}
