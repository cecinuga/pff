import type { ReactNode } from "react";
import type { Freshness } from "../lib/types";

export function Spinner({ label = "Loading" }: { label?: string }) {
  return (
    <div role="status" className="flex items-center gap-2 py-6 text-sm text-ink-2">
      <span className="h-4 w-4 animate-spin rounded-full border-2 border-line border-t-accent" aria-hidden />
      {label}…
    </div>
  );
}

export function ErrorBox({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div role="alert" className="card flex flex-wrap items-center justify-between gap-3 border-critical/40 p-4 text-sm">
      <span className="flex items-center gap-2">
        <StatusIcon state="critical" />
        {message}
      </span>
      {onRetry && (
        <button className="btn-ghost" onClick={onRetry}>
          Retry
        </button>
      )}
    </div>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="card flex flex-col items-center gap-2 p-8 text-center">
      <p className="font-medium">{title}</p>
      {children && <div className="text-sm text-ink-2">{children}</div>}
    </div>
  );
}

/** Shows backend warnings about incomplete or stale bank data. */
export function FreshnessBanner({ freshness }: { freshness?: Freshness | null }) {
  if (!freshness || freshness.complete || freshness.warnings.length === 0) return null;
  return (
    <div role="status" className="card flex items-start gap-2 border-warning/50 p-3 text-sm">
      <StatusIcon state="warning" />
      <div>
        <p className="font-medium">Some data may be out of date</p>
        <ul className="mt-1 list-disc pl-4 text-ink-2">
          {freshness.warnings.map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      </div>
    </div>
  );
}

/** Status glyph: status colors never carry meaning alone, so each has a shape. */
export function StatusIcon({ state }: { state: "good" | "warning" | "critical" }) {
  const color = state === "good" ? "text-good" : state === "warning" ? "text-warning" : "text-critical";
  return (
    <svg viewBox="0 0 16 16" className={`h-4 w-4 shrink-0 ${color}`} aria-hidden>
      {state === "good" && <path d="M3 8.5l3 3 7-7" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />}
      {state === "warning" && (
        <>
          <path d="M8 1.5l7 13H1z" fill="currentColor" />
          <path d="M8 6v4M8 12v.5" stroke="rgb(var(--raised))" strokeWidth="1.6" strokeLinecap="round" />
        </>
      )}
      {state === "critical" && (
        <>
          <circle cx="8" cy="8" r="7" fill="currentColor" />
          <path d="M5.5 5.5l5 5M10.5 5.5l-5 5" stroke="rgb(var(--raised))" strokeWidth="1.6" strokeLinecap="round" />
        </>
      )}
    </svg>
  );
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: string; actions?: ReactNode }) {
  return (
    <div className="mb-5 flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="mt-0.5 text-sm text-ink-2">{subtitle}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

export function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  return (
    <div className="fixed inset-0 z-40 flex items-end justify-center bg-black/40 p-0 sm:items-center sm:p-4" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="card max-h-[92vh] w-full overflow-y-auto rounded-b-none p-5 sm:max-w-lg sm:rounded-xl"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => e.key === "Escape" && onClose()}
      >
        <div className="mb-4 flex items-center justify-between">
          <h2 className="text-lg font-semibold">{title}</h2>
          <button className="btn-ghost px-2 py-1" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}
