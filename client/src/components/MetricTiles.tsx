import type { Stats } from "../lib/types";
import { money } from "../lib/format";

/**
 * The FunctionalSpec metrics as stat tiles: Total (headline), Mean and
 * Max-Min, plus the transaction count they were computed from.
 */
export function MetricTiles({ stats, currency, compact = false }: { stats: Stats; currency: string; compact?: boolean }) {
  const empty = stats.count === 0;
  return (
    <dl className={`grid gap-3 ${compact ? "grid-cols-3" : "grid-cols-2 sm:grid-cols-4"}`}>
      <Tile label="Total" value={empty ? "—" : money(stats.total, currency)} hero={!compact} />
      <Tile label="Mean" value={empty ? "—" : money(stats.mean, currency)} hint="per transaction" />
      <Tile
        label="Max-Min"
        value={empty ? "—" : `${money(stats.max, currency)}`}
        hint={empty ? undefined : `min ${money(stats.min, currency)}`}
      />
      {!compact && <Tile label="Transactions" value={String(stats.count)} />}
    </dl>
  );
}

function Tile({ label, value, hint, hero }: { label: string; value: string; hint?: string; hero?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-ink-2">{label}</dt>
      <dd className={`num truncate font-semibold ${hero ? "text-2xl" : "text-lg"}`} title={value}>
        {value}
      </dd>
      {hint && <dd className="num truncate text-xs text-ink-3">{hint}</dd>}
    </div>
  );
}
