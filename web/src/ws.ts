/** Builds a same-origin WebSocket URL for an API path. */
export function wsURL(path: string): string {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/api/v1${path}`;
}
