// Minimal typings for the parts of noVNC used by the console.
declare module "@novnc/novnc" {
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, urlOrChannel: string | WebSocket, options?: { credentials?: { password?: string } });
    scaleViewport: boolean;
    resizeSession: boolean;
    clipViewport: boolean;
    viewOnly: boolean;
    focusOnClick: boolean;
    showDotCursor: boolean;
    disconnect(): void;
    sendCredentials(credentials: { password?: string; username?: string }): void;
    sendCtrlAltDel(): void;
    clipboardPasteFrom(text: string): void;
    focus(): void;
  }
}
