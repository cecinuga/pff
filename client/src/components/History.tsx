import { useEffect, useState } from "react";
import { api } from "../lib/api";
import type { Domain, HistoryEntry, HistoryPage } from "../lib/types";
import { date, money } from "../lib/format";
import { ErrorBox, Spinner } from "./ui";

/** Paged financial history of a domain (newest first). */
export function HistoryList({
  domain,
  query,
  cls,
  onOpen,
}: {
  domain: Domain;
  query: Record<string, string | undefined>;
  cls?: string;
  onOpen: (id: string) => void;
}) {
  const [entries, setEntries] = useState<HistoryEntry[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const key = JSON.stringify({ domain, query, cls });

  const load = async (after?: string) => {
    setLoading(true);
    setError(null);
    try {
      const res = await api.get<{ history: HistoryPage }>("/history", { domain, class: cls, cursor: after, limit: 50, ...query });
      setEntries((prev) => (after ? [...prev, ...res.history.entries] : res.history.entries));
      setCursor(res.history.next_cursor);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    setEntries([]);
    setCursor(undefined);
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  if (error && entries.length === 0) return <ErrorBox message={error} onRetry={() => void load()} />;

  return (
    <div>
      {entries.length === 0 && !loading && <p className="py-4 text-sm text-ink-2">No transactions in this period.</p>}
      <ul className="divide-y divide-line">
        {entries.map((e) => (
          <li key={e.id}>
            <button
              className="flex w-full items-center gap-3 px-1 py-2.5 text-left hover:bg-sunken focus:bg-sunken focus:outline-none"
              onClick={() => onOpen(e.id)}
            >
              <span className="w-14 shrink-0 text-xs text-ink-3">{date(e.date, { day: "numeric", month: "short" })}</span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm">{e.counterparty || e.description || "—"}</span>
                <span className="block truncate text-xs text-ink-3">
                  {e.class_label}
                  {e.account_name && ` · ${e.account_name}`}
                </span>
              </span>
              <span className="num shrink-0 text-sm font-medium">
                {e.direction === "DBIT" ? "−" : "+"}
                {money(e.amount, e.currency)}
              </span>
            </button>
          </li>
        ))}
      </ul>
      {loading && <Spinner />}
      {error && entries.length > 0 && <p className="text-sm text-critical">{error}</p>}
      {cursor && !loading && (
        <button className="btn-ghost mt-3 w-full" onClick={() => void load(cursor)}>
          Load more
        </button>
      )}
    </div>
  );
}
