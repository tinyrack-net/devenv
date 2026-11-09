import { useEffect, useRef, useState } from "react";
import RFB from "@novnc/novnc";
import { ClipboardPaste, Eye, Expand, Keyboard, MousePointer2, RotateCw, Scaling, SlidersHorizontal } from "lucide-react";
import { wsURL } from "../lib/api";
import { cx } from "../components/ui";
import { Menu } from "../components/Menu";

type Scale = "fit" | "remote" | "native";

const keys: { label: string; keysym: number; code: string; combo?: number[] }[] = [
  { label: "Esc", keysym: 0xff1b, code: "Escape" },
  { label: "Tab", keysym: 0xff09, code: "Tab" },
  { label: "Super", keysym: 0xffeb, code: "MetaLeft" },
  { label: "Alt+Tab", keysym: 0xff09, code: "Tab", combo: [0xffe9] },
  { label: "Ctrl+C", keysym: 0x63, code: "KeyC", combo: [0xffe3] },
  { label: "Ctrl+V", keysym: 0x76, code: "KeyV", combo: [0xffe3] },
  { label: "Ctrl+Alt+T", keysym: 0x74, code: "KeyT", combo: [0xffe3, 0xffe9] },
  { label: "F11", keysym: 0xffc8, code: "F11" },
];

const modCode: Record<number, string> = { 0xffe3: "ControlLeft", 0xffe9: "AltLeft", 0xffeb: "MetaLeft" };

/**
 * noVNC viewer tunneled through the devenv server. The same VNC session is
 * shared with agents using the desktop tools; "watch" mode (view only) lets a
 * person follow an agent without interfering.
 */
export function DesktopView({ env, active, ready }: { env: string; active: boolean; ready: boolean }) {
  const screen = useRef<HTMLDivElement>(null);
  const wrap = useRef<HTMLDivElement>(null);
  const rfbRef = useRef<RFB | null>(null);
  const [status, setStatus] = useState<"connecting" | "connected" | "disconnected">("connecting");
  const [attempt, setAttempt] = useState(0);
  const [viewOnly, setViewOnly] = useState(false);
  const [scale, setScale] = useState<Scale>(() => (localStorage.getItem("devenv.vnc.scale") as Scale) || "remote");
  const [quality, setQuality] = useState(() => Number(localStorage.getItem("devenv.vnc.quality") ?? 6));
  const [remoteClip, setRemoteClip] = useState("");

  useEffect(() => {
    if (!screen.current || !ready) return;
    setStatus("connecting");
    const rfb = new RFB(screen.current, wsURL(`/environments/${encodeURIComponent(env)}/vnc`));
    rfb.focusOnClick = true;
    rfb.background = "transparent";
    rfbRef.current = rfb;
    const onConnect = () => {
      setStatus("connected");
      if (active) rfb.focus();
    };
    let retry: ReturnType<typeof setTimeout> | undefined;
    const onDisconnect = (e: Event) => {
      setStatus("disconnected");
      const clean = (e as CustomEvent<{ clean: boolean }>).detail?.clean;
      if (!clean) retry = setTimeout(() => setAttempt((a) => a + 1), 3000);
    };
    const onCredentials = () => rfb.sendCredentials({ password: "" });
    const onClipboard = (e: Event) => setRemoteClip((e as CustomEvent<{ text: string }>).detail.text);
    rfb.addEventListener("connect", onConnect);
    rfb.addEventListener("disconnect", onDisconnect);
    rfb.addEventListener("credentialsrequired", onCredentials);
    rfb.addEventListener("clipboard", onClipboard);
    return () => {
      clearTimeout(retry);
      rfb.removeEventListener("connect", onConnect);
      rfb.removeEventListener("disconnect", onDisconnect);
      rfb.removeEventListener("credentialsrequired", onCredentials);
      rfb.removeEventListener("clipboard", onClipboard);
      rfb.disconnect();
      rfbRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [env, attempt, ready]);

  useEffect(() => {
    const r = rfbRef.current;
    if (!r) return;
    r.viewOnly = viewOnly;
    r.scaleViewport = scale === "fit";
    r.resizeSession = scale === "remote";
    r.clipViewport = scale === "native";
    r.dragViewport = false;
    r.qualityLevel = quality;
    r.compressionLevel = 2;
    localStorage.setItem("devenv.vnc.scale", scale);
    localStorage.setItem("devenv.vnc.quality", String(quality));
  }, [viewOnly, scale, quality, status]);

  useEffect(() => {
    if (active && status === "connected") rfbRef.current?.focus();
  }, [active, status]);

  // Remote clipboard → local clipboard when the window has focus.
  useEffect(() => {
    if (remoteClip && document.hasFocus()) navigator.clipboard.writeText(remoteClip).catch(() => {});
  }, [remoteClip]);

  const paste = async () => {
    try {
      const text = await navigator.clipboard.readText();
      rfbRef.current?.clipboardPasteFrom(text);
    } catch {
      const text = prompt("Text to send to the remote clipboard:");
      if (text) rfbRef.current?.clipboardPasteFrom(text);
    }
  };
  const sendKey = (k: (typeof keys)[number]) => {
    const r = rfbRef.current;
    if (!r) return;
    for (const m of k.combo ?? []) r.sendKey(m, modCode[m], true);
    r.sendKey(k.keysym, k.code, true);
    r.sendKey(k.keysym, k.code, false);
    for (const m of (k.combo ?? []).slice().reverse()) r.sendKey(m, modCode[m], false);
    r.focus();
  };

  return (
    <div className="flex h-full w-full flex-col" ref={wrap}>
      <div className="no-scrollbar flex shrink-0 items-center gap-1 overflow-x-auto overflow-y-hidden border-b border-base-300 bg-base-100 px-2 py-1 text-xs">
        <span className={cx("status shrink-0", status === "connected" ? "status-success" : status === "connecting" ? "status-info animate-pulse" : "status-error")} />
        <span className="hidden text-base-content/60 sm:inline">{status}</span>
        <div className="flex-1" />
        <button className={cx("btn btn-ghost btn-xs", viewOnly && "btn-active")} onClick={() => setViewOnly((v) => !v)} title="Watch only: input is not sent (follow an agent without interfering)">
          {viewOnly ? <Eye className="size-3.5" /> : <MousePointer2 className="size-3.5" />}
          <span className="hidden sm:inline">{viewOnly ? "Watching" : "Control"}</span>
        </button>
        <Menu
          label="Scaling"
          triggerClassName="btn btn-ghost btn-xs"
          className="w-56"
          trigger={
            <>
              <Scaling className="size-3.5" /> <span className="hidden sm:inline">{scale === "remote" ? "Resize remote" : scale === "fit" ? "Fit" : "1:1"}</span>
            </>
          }
        >
          {(close) => (
            <>
              {([
                ["remote", "Resize remote to window"],
                ["fit", "Scale to fit"],
                ["native", "Original size (scroll)"],
              ] as [Scale, string][]).map(([v, label]) => (
                <li key={v}>
                  <button className={cx(scale === v && "menu-active")} onClick={() => { close(); setScale(v); }}>
                    {label}
                  </button>
                </li>
              ))}
            </>
          )}
        </Menu>
        <Menu label="Quality" triggerClassName="btn btn-ghost btn-xs" className="w-56 p-3!" trigger={<SlidersHorizontal className="size-3.5" />}>
          {() => (
            <li className="block">
              <div className="mb-1 text-xs">Image quality · {quality}</div>
              <input type="range" min={0} max={9} value={quality} className="range range-xs" onChange={(e) => setQuality(Number(e.target.value))} />
              <div className="mt-1 text-xs text-base-content/50">Lower is faster on slow links.</div>
            </li>
          )}
        </Menu>
        <Menu label="Send keys" triggerClassName="btn btn-ghost btn-xs" className="w-44" trigger={<Keyboard className="size-3.5" />}>
          {(close) => (
            <>
              {keys.map((k) => (
                <li key={k.label}>
                  <button onClick={() => { close(); sendKey(k); }}>{k.label}</button>
                </li>
              ))}
              <li>
                <button onClick={() => { close(); rfbRef.current?.sendCtrlAltDel(); }}>Ctrl+Alt+Del</button>
              </li>
            </>
          )}
        </Menu>
        <button className="btn btn-ghost btn-xs" onClick={paste} title="Paste local clipboard into the desktop">
          <ClipboardPaste className="size-3.5" />
        </button>
        <button className="btn btn-ghost btn-xs" onClick={() => wrap.current?.requestFullscreen()} title="Fullscreen">
          <Expand className="size-3.5" />
        </button>
        {status === "disconnected" && (
          <button className="btn btn-ghost btn-xs" onClick={() => setAttempt((a) => a + 1)}>
            <RotateCw className="size-3.5" /> Reconnect
          </button>
        )}
      </div>
      <div className="relative min-h-0 flex-1 overflow-hidden">
        <div className={cx("vnc-host", scale === "native" && "vnc-native")} ref={screen} />
        {!ready && <div className="absolute inset-0 flex items-center justify-center text-sm text-base-content/60">Waiting for the environment…</div>}
      </div>
    </div>
  );
}
