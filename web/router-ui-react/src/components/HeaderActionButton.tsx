import type { ButtonHTMLAttributes, ReactElement, ReactNode } from "react";

export type HeaderActionTone = "pink" | "teal" | "blue" | "green" | "danger" | "amber" | "red" | "default";

export interface HeaderActionButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "children"> {
  icon?: string | undefined;
  children?: ReactNode;
  active?: boolean | undefined;
  activeTone?: HeaderActionTone | undefined;
  badge?: ReactNode | undefined;
}

export function HeaderActionButton({
  icon,
  children,
  active = false,
  activeTone = "teal",
  badge,
  className = "",
  type = "button",
  ...buttonProps
}: HeaderActionButtonProps): ReactElement {
  const toneClass = active ? `header-action-button--active-${activeTone}` : "";
  const activeClass = active ? "active" : "";
  const combinedClassName = [
    "icon-btn-round",
    "header-action-button",
    activeClass,
    toneClass,
    className,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <button type={type} className={combinedClassName} {...buttonProps}>
      {icon ? <i className={icon} aria-hidden="true" /> : null}
      {children}
      {badge ? <span className="header-action-button-badge">{badge}</span> : null}
    </button>
  );
}
