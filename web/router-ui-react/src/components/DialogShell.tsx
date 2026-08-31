import { useEffect, useRef, type ReactNode } from "react";

export interface DialogShellProps {
  open: boolean;
  title: string;
  description?: string;
  children: ReactNode;
  onClose: () => void;
  onCancel?: () => void;
  className?: string;
  closeLabel?: string;
  returnFocus?: HTMLElement | null;
  footer?: ReactNode;
  titleId?: string;
}

/** Native dialog semantics with a small focus restoration contract shared by controls. */
export function DialogShell({
  open,
  title,
  description,
  children,
  onClose,
  onCancel,
  className = "message-dialog",
  closeLabel = "Close dialog",
  returnFocus,
  footer,
  titleId,
}: DialogShellProps) {
  const headingId = titleId ?? `${title.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}-title`;
  const dialogRef = useRef<HTMLDialogElement>(null);
  const wasOpen = useRef(false);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (open && !wasOpen.current) {
      if (!dialog.open) {
        if (typeof dialog.showModal === "function") dialog.showModal();
        else dialog.setAttribute("open", "");
      }
      requestAnimationFrame(() => {
        const first = dialog.querySelector<HTMLElement>("input, textarea, button, [tabindex='0']");
        first?.focus();
      });
    } else if (!open && wasOpen.current && dialog.open) {
      if (typeof dialog.close === "function") dialog.close();
      else dialog.removeAttribute("open");
      requestAnimationFrame(() => returnFocus?.focus());
    }
    wasOpen.current = open;
  }, [open]);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const cancel = (event: Event) => {
      event.preventDefault();
      onCancel?.();
      if (!onCancel) onClose();
    };
    const close = () => {
      requestAnimationFrame(() => returnFocus?.focus());
    };
    dialog.addEventListener("cancel", cancel);
    dialog.addEventListener("close", close);
    return () => {
      dialog.removeEventListener("cancel", cancel);
      dialog.removeEventListener("close", close);
    };
  }, [onCancel, onClose, open, returnFocus]);

  return (
    <dialog ref={dialogRef} className={className} aria-labelledby={headingId}>
      <div className="dialog-shell">
        <header>
          <div>
            <h2 id={headingId}>{title}</h2>
            {description ? <p>{description}</p> : null}
          </div>
          <button type="button" aria-label={closeLabel} onClick={onClose}>×</button>
        </header>
        {children}
        {footer ? <footer>{footer}</footer> : null}
      </div>
    </dialog>
  );
}
