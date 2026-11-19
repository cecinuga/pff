import { useSearchParams } from "react-router-dom";
import type { RangeCode, RangeDTO } from "../lib/types";
import { date } from "../lib/format";

const RANGES: { code: RangeCode; label: string }[] = [
  { code: "1d", label: "1D" },
  { code: "1w", label: "1W" },
  { code: "2w", label: "2W" },
  { code: "1m", label: "1M" },
  { code: "3m", label: "3M" },
  { code: "custom", label: "Custom" },
];

export interface RangeParams {
  range: RangeCode;
  from?: string;
  to?: string;
}

/** The selected date range lives in the URL so it survives navigation and reloads. */
export function useRangeParams(): [RangeParams, (p: RangeParams) => void] {
  const [params, setParams] = useSearchParams();
  const range = (params.get("range") as RangeCode | null) ?? "1m";
  const value: RangeParams = { range, from: params.get("from") ?? undefined, to: params.get("to") ?? undefined };
  const set = (p: RangeParams) => {
    const next = new URLSearchParams(params);
    next.set("range", p.range);
    if (p.range === "custom" && p.from && p.to) {
      next.set("from", p.from);
      next.set("to", p.to);
    } else {
      next.delete("from");
      next.delete("to");
    }
    setParams(next, { replace: true });
  };
  return [value, set];
}

/** Query parameters for the API; a custom range without dates falls back to 1 month. */
export function rangeQuery(p: RangeParams): Record<string, string | undefined> {
  if (p.range === "custom") {
    return p.from && p.to ? { range: "custom", from: p.from, to: p.to } : { range: "1m" };
  }
  return { range: p.range };
}

export function RangePicker({ value, onChange, resolved }: { value: RangeParams; onChange: (p: RangeParams) => void; resolved?: RangeDTO }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <div role="radiogroup" aria-label="Date range" className="inline-flex rounded-lg border border-line bg-raised p-0.5">
        {RANGES.map((r) => (
          <button
            key={r.code}
            role="radio"
            aria-checked={value.range === r.code}
            onClick={() => onChange({ ...value, range: r.code })}
            className={`rounded-md px-2.5 py-1 text-xs font-medium transition ${
              value.range === r.code ? "bg-accent text-white" : "text-ink-2 hover:bg-sunken"
            }`}
          >
            {r.label}
          </button>
        ))}
      </div>
      {value.range === "custom" && (
        <div className="flex items-center gap-1.5">
          <input
            type="date"
            aria-label="From"
            className="input w-auto py-1"
            value={value.from ?? ""}
            onChange={(e) => onChange({ ...value, from: e.target.value })}
          />
          <span className="text-ink-3">–</span>
          <input
            type="date"
            aria-label="To"
            className="input w-auto py-1"
            value={value.to ?? ""}
            onChange={(e) => onChange({ ...value, to: e.target.value })}
          />
        </div>
      )}
      {resolved && (
        <span className="text-xs text-ink-3">
          {resolved.from === resolved.to ? date(resolved.from) : `${date(resolved.from)} – ${date(resolved.to)}`}
        </span>
      )}
    </div>
  );
}
