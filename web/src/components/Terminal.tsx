import { useEffect, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { wsURL } from "../ws";

type State = { kind: "connecting" } | { kind: "open" } | { kind: "closed"; message: string };

/** Interactive shell in an environment over WebSocket -> SSH. */
export function Terminal({ env, onClose }: { env: string; onClose?: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<State>({ kind: "connecting" });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const term = new XTerm({
      cursorBlink: true,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, "DejaVu Sans Mono", monospace',
      fontSize: 13,
      scrollback: 5000,
      theme: { background: "#0d1117" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    fit.fit();

    const ws = new WebSocket(wsURL(`/environments/${encodeURIComponent(env)}/terminal?cols=${term.cols}&rows=${term.rows}`));
    ws.binaryType = "arraybuffer";
    const enc = new TextEncoder();
    let closedMessage = "Disconnected";

    ws.onopen = () => {
      setState({ kind: "open" });
      term.focus();
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") {
        const msg = JSON.parse(ev.data);
        if (msg.type === "exit") closedMessage = `Shell exited with code ${msg.code}`;
        if (msg.type === "error") {
          closedMessage = msg.message;
          term.writeln(`\r\n\x1b[31m${msg.message}\x1b[0m`);
        }
        return;
      }
      term.write(new Uint8Array(ev.data as ArrayBuffer));
    };
    ws.onclose = () => setState({ kind: "closed", message: closedMessage });

    const data = term.onData((d) => ws.readyState === WebSocket.OPEN && ws.send(enc.encode(d)));
    const binary = term.onBinary((d) => {
      if (ws.readyState !== WebSocket.OPEN) return;
      const buf = new Uint8Array(d.length);
      for (let i = 0; i < d.length; i++) buf[i] = d.charCodeAt(i) & 0xff;
      ws.send(buf);
    });
    const resize = term.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "resize", cols, rows }));
    });
    const observer = new ResizeObserver(() => {
      try {
        fit.fit();
      } catch {
        /* element hidden */
      }
    });
    observer.observe(el);

    return () => {
      observer.disconnect();
      data.dispose();
      binary.dispose();
      resize.dispose();
      ws.close();
      term.dispose();
    };
  }, [env, attempt]);

  return (
    <div className="terminal-wrap">
      <div className="terminal-bar">
        <span className="small muted">
          {state.kind === "connecting" && "Connecting…"}
          {state.kind === "open" && "Connected"}
          {state.kind === "closed" && state.message}
        </span>
        <div className="spacer" />
        {state.kind === "closed" && (
          <button className="small" onClick={() => setAttempt((a) => a + 1)}>
            Reconnect
          </button>
        )}
        {onClose && (
          <button className="small" onClick={onClose}>
            Close
          </button>
        )}
      </div>
      <div className="terminal" ref={ref} />
    </div>
  );
}
