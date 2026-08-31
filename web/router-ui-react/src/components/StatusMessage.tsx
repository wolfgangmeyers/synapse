import type { ReactElement } from "react";
export interface StatusMessageProps { children: string; tone?: "error" | "info"; role?: "status" | "alert" }
export function StatusMessage({ children, tone = "info", role = tone === "error" ? "alert" : "status" }: StatusMessageProps): ReactElement { return <p className={`status-message ${tone}`} role={role}>{children}</p>; }
