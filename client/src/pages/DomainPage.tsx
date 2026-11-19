import { useState } from "react";
import { NavLink, useParams, useSearchParams } from "react-router-dom";
import { api } from "../lib/api";
import { useResource } from "../lib/hooks";
import { useDomainInfo, useMeta } from "../lib/meta";
import type { Allocation, Domain, Freshness, MetricsResult, Series } from "../lib/types";
import { AllocationBars } from "../components/AllocationBars";
import { HistoryList } from "../components/History";
import { MetricTiles } from "../components/MetricTiles";
import { RangePicker, rangeQuery, useRangeParams } from "../components/RangePicker";
import { SeriesChart } from "../components/SeriesChart";
import { TransactionDrawer } from "../components/TransactionDrawer";
import { ErrorBox, FreshnessBanner, PageHeader, Spinner } from "../components/ui";

/** One metric domain: metrics, trend, allocation by class and history. */
export function DomainPage() {
  const meta = useMeta();
  const { domain = "spending" } = useParams<{ domain: Domain }>();
  const info = useDomainInfo(domain as Domain);
  const categories = useDomainInfo("spending_category");
  const [range, setRange] = useRangeParams();
  const [params, setParams] = useSearchParams();
  const cls = params.get("class") ?? "";
  const txId = params.get("tx");
  const [version, setVersion] = useState(0);
  const q = rangeQuery(range);
  const deps = [domain, JSON.stringify(q), version];

  const metrics = useResource((s) => api.get<{ metrics: MetricsResult; freshness: Freshness }>("/metrics", { domain, ...q }, s), deps);
  const alloc = useResource((s) => api.get<{ allocation: Allocation }>("/allocation", { domain, ...q }, s), deps);
  const series = useResource((s) => api.get<{ series: Series }>("/history/series", { domain, class: cls, ...q }, s), [...deps, cls]);

  const setParam = (k: string, v: string | null) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    setParams(next, { replace: k === "class" });
  };

  if (!info) return <ErrorBox message="Unknown metric domain" />;
  const keep = new URLSearchParams(q as Record<string, string>).toString();
  const classLabel = info.classes.find((c) => c.class === cls)?.label;

  return (
    <div className="space-y-5">
      <nav className="-mx-1 flex gap-1 overflow-x-auto pb-1" aria-label="Metric domains">
        {meta.domains.map((d) => (
          <NavLink
            key={d.domain}
            to={`/domains/${d.domain}?${keep}`}
            className={({ isActive }) =>
              `whitespace-nowrap rounded-full px-3 py-1.5 text-sm ${isActive ? "bg-ink text-surface" : "text-ink-2 hover:bg-sunken"}`
            }
          >
            {d.label}
          </NavLink>
        ))}
      </nav>

      <PageHeader title={info.label} subtitle={info.description} actions={<RangePicker value={range} onChange={setRange} resolved={metrics.data?.metrics.range} />} />

      <FreshnessBanner freshness={metrics.data?.freshness} />
      {metrics.error && <ErrorBox message={metrics.error} onRetry={metrics.reload} />}

      <section className="card p-5">
        {metrics.data ? (
          <MetricTiles stats={metrics.data.metrics.overall} currency={metrics.data.metrics.currency} />
        ) : (
          metrics.loading && <Spinner label="Loading metrics" />
        )}
      </section>

      <div className="grid gap-5 lg:grid-cols-5">
        <section className="card min-w-0 p-5 lg:col-span-3 lg:self-start">
          <div className="mb-3 flex items-center justify-between gap-2">
            <h2 className="font-semibold">
              {info.label} over time{classLabel ? ` · ${classLabel}` : ""}
            </h2>
            {cls && (
              <button className="text-xs text-accent hover:underline" onClick={() => setParam("class", null)}>
                Show all classes
              </button>
            )}
          </div>
          {series.data ? (
            <SeriesChart series={series.data.series} title={`${info.label} per ${series.data.series.bucket}`} />
          ) : series.error ? (
            <ErrorBox message={series.error} onRetry={series.reload} />
          ) : (
            <Spinner />
          )}
        </section>

        <section className="card min-w-0 p-5 lg:col-span-2">
          <h2 className="mb-3 font-semibold">Allocation by class</h2>
          {alloc.data ? (
            <AllocationBars allocation={alloc.data.allocation} onSelect={(c) => setParam("class", c === cls ? null : c)} />
          ) : alloc.error ? (
            <ErrorBox message={alloc.error} onRetry={alloc.reload} />
          ) : (
            <Spinner />
          )}
          {metrics.data && metrics.data.metrics.classes.length > 0 && (
            <details className="mt-4 text-xs">
              <summary className="cursor-pointer text-ink-2">Metrics per class</summary>
              <div className="mt-2 overflow-x-auto">
                <table className="w-full text-left">
                  <thead className="text-ink-3">
                    <tr>
                      <th className="py-1 font-normal">Class</th>
                      <th className="py-1 text-right font-normal">Total</th>
                      <th className="py-1 text-right font-normal">Mean</th>
                      <th className="py-1 text-right font-normal">Max</th>
                      <th className="py-1 text-right font-normal">Min</th>
                      <th className="py-1 text-right font-normal">#</th>
                    </tr>
                  </thead>
                  <tbody className="num">
                    {metrics.data.metrics.classes.map((c) => (
                      <tr key={c.class} className="border-t border-line">
                        <td className="py-1">{c.label}</td>
                        <td className="py-1 text-right">{c.stats.total}</td>
                        <td className="py-1 text-right">{c.stats.mean}</td>
                        <td className="py-1 text-right">{c.stats.max}</td>
                        <td className="py-1 text-right">{c.stats.min}</td>
                        <td className="py-1 text-right">{c.stats.count}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </details>
          )}
        </section>
      </div>

      <section className="card p-5">
        <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
          <h2 className="font-semibold">History{classLabel ? ` · ${classLabel}` : ""}</h2>
          <select className="input w-auto py-1 text-xs" value={cls} onChange={(e) => setParam("class", e.target.value || null)} aria-label="Filter by class">
            <option value="">All classes</option>
            {info.classes.map((c) => (
              <option key={c.class} value={c.class}>
                {c.label}
              </option>
            ))}
          </select>
        </div>
        <HistoryList key={version} domain={domain as Domain} query={q} cls={cls || undefined} onOpen={(id) => setParam("tx", id)} />
      </section>

      {txId && (
        <TransactionDrawer
          id={txId}
          categories={categories}
          onClose={() => setParam("tx", null)}
          onChanged={() => setVersion((v) => v + 1)}
        />
      )}
    </div>
  );
}
