import { useSyncExternalStore } from "react";

/** Tracks a CSS media query. */
export function useMedia(query: string): boolean {
  return useSyncExternalStore(
    (cb) => {
      const m = window.matchMedia(query);
      m.addEventListener("change", cb);
      return () => m.removeEventListener("change", cb);
    },
    () => window.matchMedia(query).matches,
  );
}

/** Phones and narrow windows (below Tailwind's md breakpoint). */
export function useIsMobile(): boolean {
  return useMedia("(max-width: 767px)");
}

/** Touch devices without a precise pointer. */
export function useIsTouch(): boolean {
  return useMedia("(pointer: coarse)");
}
