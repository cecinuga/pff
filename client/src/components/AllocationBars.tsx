import { useState } from "react";
import type { Allocation } from "../lib/types";
import { money, percent } from "../lib/format";

/**
 * Allocation of a domain by metric class: one horizontal bar per class in a
 * single accent hue (one measure across categories, so no categorical colors).
 * Labels and values are text, so the list doubles as the table view.
 * Bar lengths are the backend's percentages.
 */
export function AllocationBars({ allocation, onSelect }: { allocation: Allocation; onSelect?: (cls: string) => void }) {
  const [hover, setHover] = useState<string | null>(null);
  if (allocation.items.length === 0) {
    return <p className="py-4 text-sm text-ink-2">No transactions in this period.</p>;
  }
  return (
    <ul className="space-y-2.5" aria-label={`Allocation of ${allocation.domain_label}`}>
      {allocation.items.map((it) => (
        <li key={it.class}>
          <button
            type="button"
            className="group w-full text-left"
            onClick={() => onSelect?.(it.class)}
            onMouseEnter={() => setHover(it.class)}
            onMouseLeave={() => setHover(null)}
            onFocus={() => setHover(it.class)}
            onBlur={() => setHover(null)}
            title={`${it.label}: ${money(it.total, allocation.currency)} (${percent(it.percentage)})`}
          >
            <div className="mb-1 flex items-baseline justify-between gap-3 text-sm">
              <span className={`truncate ${hover === it.class ? "text-ink" : "text-ink-2"}`}>{it.label}</span>
              <span className="num shrink-0">
                <span className="font-medium">{money(it.total, allocation.currency)}</span>
                <span className="ml-2 inline-block w-14 text-right text-ink-3">{percent(it.percentage)}</span>
              </span>
            </div>
            <div className="h-2.5 w-full rounded-full bg-sunken">
              <div
                className={`h-2.5 rounded-full bg-accent transition-[filter] ${hover === it.class ? "brightness-110" : ""}`}
                style={{ width: `${Math.max(Number(it.percentage), 0.5)}%` }}
              />
            </div>
          </button>
        </li>
      ))}
    </ul>
  );
}
