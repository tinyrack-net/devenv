import { useEffect, useRef, useState } from "react";
import { Terminal as XTerm, type ITheme } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { SearchAddon } from "@xterm/addon-search";
import { ClipboardAddon } from "@xterm/addon-clipboard";
import "@xterm/xterm/css/xterm.css";
import { RotateCw, Search, X } from "lucide-react";
import { wsURL, type PtySession } from "../lib/api";
import { isDark, usePrefs } from "../lib/prefs";
import { useIsTouch } from "../lib/media";
import { cx } from "../components/ui";

const darkTheme: ITheme = {
  background: "#16181d",
  foreground: "#d7dae0",
  cursor: "#d7dae0",
  selectionBackground: "#3e4452",
  black: "#1e2127",
  red: "#e06c75",
  green: "#98c379",
  yellow: "#d19a66",
  blue: "#61afef",
  magenta: "#c678dd",
  cyan: "#56b6c2",
  white: "#abb2bf",
  brightBlack: "#5c6370",
  brightRed: "#e06c75",
  brightGreen: "#98c379",
  brightYellow: "#d19a66",
  brightBlue: "#61afef",
  brightMagenta: "#c678dd",
  brightCyan: "#56b6c2",
  brightWhite: "#ffffff",
};
const lightTheme: ITheme = {
  background: "#fafafa",
  foreground: "#383a42",
  cursor: "#383a42",
  selectionBackground: "#d0d4dc",
  black: "#383a42",
  red: "#e45649",
  green: "#50a14f",
  yellow: "#c18401",
  blue: "#4078f2",
  magenta: "#a626a4",
  cyan: "#0184bc",
  white: "#a0a1a7",
  brightBlack: "#696c77",
  brightRed: "#e45649",
  brightGreen: "#50a14f",
  brightYellow: "#c18401",
  brightBlue: "#4078f2",
  brightMagenta: "#a626a4",
  brightCyan: "#0184bc",
  brightWhite: "#ffffff",
};

export type TermStatus = "connecting" | "open" | "reconnecting" | "exited" | "error";

/**
 * A terminal attached to a persistent session in the environment. The
 * session survives reloads and disconnects; the view reattaches
 * automatically. `sessionId` empty creates a new session; onSession reports
 * its id so the tab can remember it.
 */
export function TerminalView({
  env,
  sessionId,
  create,
  command,
  name,
  active,
  ready,
  onSession,
  onStatus,
}: {
  env: string;
  sessionId?: string;
  create?: boolean;
  command?: string;
  name?: string;
  active: boolean;
  ready: boolean;
  onSession: (s: PtySession) => void;
  onStatus?: (s: TermStatus, message?: string) => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const termRef = useRef<XTerm | null>(null);
  // Sends raw input to the session (used by the touch key bar).
  const sendRef = useRef<(d: string) => void>(() => {});
  const touch = useIsTouch();
  const [ctrl, setCtrl] = useState(false);
  const ctrlRef = useRef(false);
  ctrlRef.current = ctrl;
  const fitRef = useRef<FitAddon | null>(null);
  const searchRef = useRef<SearchAddon | null>(null);
  const [prefs] = usePrefs();
  const [status, setStatus] = useState<TermStatus>("connecting");
  const [message, setMessage] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchText, setSearchText] = useState("");
  // The session id after the first attach; reconnects reuse it.
  const sid = useRef(sessionId);
  const cbs = useRef({ onSession, onStatus });
  cbs.current = { onSession, onStatus };

  const report = (s: TermStatus, msg = "") => {
    setStatus(s);
    setMessage(msg);
    cbs.current.onStatus?.(s, msg);
  };

  // Create xterm once.
  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const term = new XTerm({
      cursorBlink: true,
      allowProposedApi: true,
      fontFamily: prefs.terminalFontFamily,
      fontSize: prefs.terminalFontSize,
      scrollback: prefs.terminalScrollback,
      theme: (prefs.terminalTheme === "auto" ? isDark() : prefs.terminalTheme === "dark") ? darkTheme : lightTheme,
      macOptionIsMeta: true,
    });
    const fit = new FitAddon();
    const search = new SearchAddon();
    term.loadAddon(fit);
    term.loadAddon(search);
    term.loadAddon(new WebLinksAddon());
    term.loadAddon(new ClipboardAddon());
    term.open(el);
    termRef.current = term;
    fitRef.current = fit;
    searchRef.current = search;
    term.attachCustomKeyEventHandler((e) => {
      // Ctrl/Cmd+Shift+F opens search; Ctrl+Shift+C/V copy and paste.
      if (e.type !== "keydown") return true;
      if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key.toLowerCase() === "f") {
        setSearchOpen(true);
        return false;
      }
      if (e.ctrlKey && e.shiftKey && e.key.toLowerCase() === "c") {
        const sel = term.getSelection();
        if (sel) navigator.clipboard.writeText(sel);
        return false;
      }
      return true;
    });
    const ro = new ResizeObserver(() => {
      try {
        if (el.offsetWidth > 0 && el.offsetHeight > 0) fit.fit();
      } catch {
        /* hidden */
      }
    });
    ro.observe(el);
    return () => {
      ro.disconnect();
      term.dispose();
      termRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Apply preference changes live.
  useEffect(() => {
    const t = termRef.current;
    if (!t) return;
    t.options.fontSize = prefs.terminalFontSize;
    t.options.fontFamily = prefs.terminalFontFamily;
    t.options.theme = (prefs.terminalTheme === "auto" ? isDark() : prefs.terminalTheme === "dark") ? darkTheme : lightTheme;
    try {
      fitRef.current?.fit();
    } catch {
      /* hidden */
    }
  }, [prefs.terminalFontSize, prefs.terminalFontFamily, prefs.terminalTheme, prefs.theme]);

  // Connect (and reconnect) the WebSocket.
  useEffect(() => {
    const term = termRef.current;
    if (!term || !ready) return;
    try {
      fitRef.current?.fit();
    } catch {
      /* hidden */
    }
    const q = new URLSearchParams({ cols: String(term.cols), rows: String(term.rows) });
    if (sid.current) q.set("session", sid.current);
    else {
      q.set("new", "1");
      if (command) q.set("command", command);
      if (name) q.set("name", name);
    }
    if (create && sid.current && attempt === 0) q.set("new", "1");
    const ws = new WebSocket(wsURL(`/environments/${encodeURIComponent(env)}/terminal?${q}`));
    ws.binaryType = "arraybuffer";
    const enc = new TextEncoder();
    let exited = false;
    let errored = false;
    let attached = false;
    report(attempt === 0 ? "connecting" : "reconnecting");

    ws.onopen = () => {
      if (!sid.current) report("open");
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") {
        const msg = JSON.parse(ev.data);
        if (msg.type === "session") {
          const s = msg.session as PtySession;
          // A replayed scrollback follows; start from a clean screen.
          if (attempt > 0 || sid.current === s.id) term.reset();
          sid.current = s.id;
          attached = true;
          cbs.current.onSession(s);
          report("open");
        } else if (msg.type === "exit") {
          exited = true;
          term.writeln(`\r\n\x1b[90m[process exited with code ${msg.code}]\x1b[0m`);
          report("exited", `exited with code ${msg.code}`);
        } else if (msg.type === "error") {
          errored = true;
          // A vanished session (environment restarted): start a fresh one.
          if (String(msg.message).includes("not found") && sid.current) {
            sid.current = undefined;
            term.writeln(`\r\n\x1b[90m[session ended; starting a new shell]\x1b[0m`);
            setTimeout(() => setAttempt((a) => a + 1), 300);
            return;
          }
          term.writeln(`\r\n\x1b[31m${msg.message}\x1b[0m`);
          report("error", msg.message);
        }
        return;
      }
      term.write(new Uint8Array(ev.data as ArrayBuffer));
    };
    let retry: ReturnType<typeof setTimeout> | undefined;
    ws.onclose = () => {
      if (exited || errored) return;
      // Persistent sessions survive; reconnect with backoff.
      report("reconnecting", attached ? "connection lost" : "");
      retry = setTimeout(() => setAttempt((a) => a + 1), Math.min(1000 * 2 ** Math.min(attempt, 4), 10000));
    };

    const send = (d: string) => ws.readyState === WebSocket.OPEN && ws.send(enc.encode(d));
    sendRef.current = (d) => void send(d);
    const data = term.onData((d) => {
      // A sticky Ctrl from the touch key bar applies to the next letter.
      if (ctrlRef.current && d.length === 1 && /[a-z@\[\]\\^_ ]/i.test(d)) {
        setCtrl(false);
        const c = d.toUpperCase().charCodeAt(0);
        d = d === " " ? "\x00" : String.fromCharCode(c & 0x1f);
      }
      send(d);
    });
    const binary = term.onBinary((d) => {
      if (ws.readyState !== WebSocket.OPEN) return;
      const buf = new Uint8Array(d.length);
      for (let i = 0; i < d.length; i++) buf[i] = d.charCodeAt(i) & 0xff;
      ws.send(buf);
    });
    const resize = term.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "resize", cols, rows }));
    });
    return () => {
      clearTimeout(retry);
      data.dispose();
      binary.dispose();
      resize.dispose();
      ws.onclose = null;
      ws.close();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [env, attempt, ready]);

  // Focus and refit when the tab becomes visible.
  useEffect(() => {
    if (!active) return;
    const t = setTimeout(() => {
      try {
        fitRef.current?.fit();
      } catch {
        /* hidden */
      }
      termRef.current?.focus();
    }, 30);
    return () => clearTimeout(t);
  }, [active]);

  return (
    <div className="relative h-full w-full" style={{ background: (prefs.terminalTheme === "auto" ? isDark() : prefs.terminalTheme === "dark") ? darkTheme.background : lightTheme.background }}>
      <div className={cx("xterm-host", touch && "bottom-10!")} ref={host} />
      {touch && (
        <div className="absolute inset-x-0 bottom-0 z-10 flex h-10 items-center gap-1 overflow-x-auto border-t border-base-300 bg-base-200 px-1">
          {[
            ["Esc", "\x1b"],
            ["Tab", "\t"],
            ["Ctrl", ""],
            ["↑", "\x1b[A"],
            ["↓", "\x1b[B"],
            ["←", "\x1b[D"],
            ["→", "\x1b[C"],
            ["^C", "\x03"],
            ["^D", "\x04"],
            ["|", "|"],
            ["~", "~"],
            ["/", "/"],
            ["-", "-"],
          ].map(([label, seq]) => (
            <button
              key={label}
              className={cx("btn btn-sm shrink-0 font-mono", label === "Ctrl" && ctrl && "btn-primary")}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => {
                if (label === "Ctrl") setCtrl((c) => !c);
                else sendRef.current(seq);
                termRef.current?.focus();
              }}
            >
              {label}
            </button>
          ))}
          <button
            className="btn btn-sm shrink-0"
            onMouseDown={(e) => e.preventDefault()}
            onClick={async () => {
              try {
                const t = await navigator.clipboard.readText();
                if (t) sendRef.current(t);
              } catch {
                /* permission denied */
              }
            }}
          >
            Paste
          </button>
        </div>
      )}
      {searchOpen && (
        <div className="absolute top-2 right-4 z-10 flex items-center gap-1 rounded-field bg-base-100 p-1 shadow">
          <Search className="ml-1 size-3.5 opacity-60" />
          <input
            autoFocus
            className="input input-xs w-48"
            value={searchText}
            onChange={(e) => {
              setSearchText(e.target.value);
              searchRef.current?.findNext(e.target.value, { incremental: true });
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") (e.shiftKey ? searchRef.current?.findPrevious(searchText) : searchRef.current?.findNext(searchText));
              if (e.key === "Escape") {
                setSearchOpen(false);
                termRef.current?.focus();
              }
            }}
          />
          <button className="btn btn-ghost btn-xs btn-square" onClick={() => setSearchOpen(false)}>
            <X className="size-3.5" />
          </button>
        </div>
      )}
      {(status === "reconnecting" || status === "connecting") && attempt > 0 && (
        <div className="absolute top-2 left-1/2 z-10 -translate-x-1/2">
          <span className="badge badge-warning gap-1">
            <span className="loading loading-dots loading-xs" /> reconnecting{message ? ` (${message})` : ""}
          </span>
        </div>
      )}
      {(status === "exited" || status === "error") && (
        <div className="absolute right-4 bottom-4 z-10">
          <button
            className="btn btn-sm btn-primary"
            onClick={() => {
              sid.current = undefined;
              termRef.current?.reset();
              setAttempt((a) => a + 1);
            }}
          >
            <RotateCw className="size-4" /> New shell
          </button>
        </div>
      )}
    </div>
  );
}
