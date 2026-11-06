import { useEffect, useRef, useState } from "react";
import RFB from "@novnc/novnc";
import { wsURL } from "../ws";

/** noVNC desktop viewer tunneled through the devenv server. */
export function VncViewer({ env, onClose }: { env: string; onClose?: () => void }) {
  const screen = useRef<HTMLDivElement>(null);
  const wrap = useRef<HTMLDivElement>(null);
  const rfbRef = useRef<RFB | null>(null);
  const [status, setStatus] = useState("Connecting…");
  const [connected, setConnected] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [viewOnly, setViewOnly] = useState(false);

  useEffect(() => {
    if (!screen.current) return;
    setStatus("Connecting…");
    const rfb = new RFB(screen.current, wsURL(`/environments/${encodeURIComponent(env)}/vnc`));
    rfb.scaleViewport = true;
    rfb.resizeSession = true;
    rfb.focusOnClick = true;
    rfbRef.current = rfb;
    const onConnect = () => {
      setConnected(true);
      setStatus("Connected");
      rfb.focus();
    };
    const onDisconnect = (e: Event) => {
      setConnected(false);
      const clean = (e as CustomEvent<{ clean: boolean }>).detail?.clean;
      setStatus(clean ? "Disconnected" : "Connection lost. Is the desktop running?");
    };
    // The image runs VNC without a password; the tunnel is authenticated.
    const onCredentials = () => rfb.sendCredentials({ password: "" });
    rfb.addEventListener("connect", onConnect);
    rfb.addEventListener("disconnect", onDisconnect);
    rfb.addEventListener("credentialsrequired", onCredentials);
    return () => {
      rfb.removeEventListener("connect", onConnect);
      rfb.removeEventListener("disconnect", onDisconnect);
      rfb.removeEventListener("credentialsrequired", onCredentials);
      rfb.disconnect();
      rfbRef.current = null;
    };
  }, [env, attempt]);

  useEffect(() => {
    if (rfbRef.current) rfbRef.current.viewOnly = viewOnly;
  }, [viewOnly]);

  const pasteClipboard = async () => {
    try {
      const text = await navigator.clipboard.readText();
      rfbRef.current?.clipboardPasteFrom(text);
    } catch {
      const text = prompt("Text to send to the remote clipboard:");
      if (text) rfbRef.current?.clipboardPasteFrom(text);
    }
  };

  return (
    <div className="vnc-wrap" ref={wrap}>
      <div className="terminal-bar">
        <span className="small muted">{status}</span>
        <div className="spacer" />
        {connected && (
          <>
            <label className="checkbox small">
              <input type="checkbox" checked={viewOnly} onChange={(e) => setViewOnly(e.target.checked)} /> View only
            </label>
            <button className="small" onClick={pasteClipboard}>
              Paste clipboard
            </button>
            <button className="small" onClick={() => rfbRef.current?.sendCtrlAltDel()}>
              Ctrl+Alt+Del
            </button>
            <button className="small" onClick={() => wrap.current?.requestFullscreen()}>
              Fullscreen
            </button>
          </>
        )}
        {!connected && (
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
      <div className="vnc-screen" ref={screen} />
    </div>
  );
}
