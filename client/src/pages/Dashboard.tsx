import { Link } from "react-router-dom";
import { api } from "../lib/api";
import { useResource } from "../lib/hooks";
import type { Connection, Freshness, MetricsResult, RangeDTO } from "../lib/types";
import { money } from "../lib/format";
import { MetricTiles } from "../components/MetricTiles";
import { RangePicker, rangeQuery, useRangeParams } from "../components/RangePicker";
import { Empty, ErrorBox, FreshnessBanner, PageHeader, Spinner } from "../components/ui";

interface Overview {
  range: RangeDTO;
  domains: MetricsResult[];
  freshness: Freshness;
}

/** Overview: every metric of every domain for the selected date range. */
export function DashboardPage() {
  const [range, setRange] = useRangeParams();
  const q = rangeQuery(range);
  const conns = useResource((s) => api.get<{ connections: Connection[] }>("/connections", undefined, s), []);
  const overview = useResource((s) => api.get<Overview>("/metrics", q, s), [JSON.stringify(q)]);

  const accounts = (conns.data?.connections ?? []).filter((c) => c.status === "active").flatMap((c) => c.accounts);
  const noBanks = conns.data && conns.data.connections.every((c) => c.accounts.length === 0);

  return (
    <div className="space-y-5">
      <PageHeader title="Overview" subtitle="Mean, total and max-min of each metric domain" actions={<RangePicker value={range} onChange={setRange} resolved={overview.data?.range} />} />

      {noBanks && (
        <Empty title="Connect your bank to get started">
          <Link to="/connections" className="btn-primary mt-2">
            Connect a bank
          </Link>
        </Empty>
      )}

      {accounts.length > 0 && (
        <div className="flex gap-3 overflow-x-auto pb-1">
          {accounts.map((a) => (
            <div key={a.id} className="card min-w-[180px] px-4 py-3">
              <p className="truncate text-xs text-ink-2">{a.name || a.iban}</p>
              <p className="num text-lg font-semibold">{a.balance ? money(a.balance, a.balance_currency || a.currency) : "—"}</p>
            </div>
          ))}
        </div>
      )}

      <FreshnessBanner freshness={overview.data?.freshness} />
      {overview.error && <ErrorBox message={overview.error} onRetry={overview.reload} />}
      {overview.loading && !overview.data && <Spinner label="Loading metrics" />}

      {overview.data && (
        <div className={`grid gap-4 md:grid-cols-2 ${overview.loading ? "opacity-60" : ""}`}>
          {overview.data.domains.map((m) => (
            <Link key={m.domain} to={`/domains/${m.domain}?${new URLSearchParams(q as Record<string, string>).toString()}`} className="card block p-5 transition hover:border-accent/50">
              <div className="mb-4 flex items-baseline justify-between gap-2">
                <h2 className="font-semibold">{m.domain_label}</h2>
                <span className="text-xs text-ink-3">{m.overall.count} transactions</span>
              </div>
              <MetricTiles stats={m.overall} currency={m.currency} compact />
              {m.classes.length > 0 && (
                <p className="mt-4 truncate text-xs text-ink-2">
                  Largest: <span className="font-medium text-ink">{m.classes[0]!.label}</span> · {money(m.classes[0]!.stats.total, m.currency)} ({m.classes[0]!.share}%)
                </p>
              )}
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
