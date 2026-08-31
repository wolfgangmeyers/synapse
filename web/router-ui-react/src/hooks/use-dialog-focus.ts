import { useCallback, useEffect, useRef, type RefObject } from "react";

export interface DialogFocusHook<T extends HTMLElement = HTMLElement> { dialogRef: RefObject<T | null>; capture(): void; restore(): void }
export function useDialogFocus<T extends HTMLElement = HTMLElement>(open: boolean): DialogFocusHook<T> {
  const dialogRef = useRef<T | null>(null); const returnFocus = useRef<HTMLElement | null>(null);
  const capture = useCallback(() => { const current = document.activeElement; returnFocus.current = current instanceof HTMLElement ? current : null; }, []);
  const restore = useCallback(() => { const element = returnFocus.current; if (element?.isConnected) element.focus(); returnFocus.current = null; }, []);
  useEffect(() => { if (!open) restore(); }, [open, restore]);
  return { dialogRef, capture, restore };
}
