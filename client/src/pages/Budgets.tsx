import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api, ApiError } from "../lib/api";
import { useResource } from "../lib/hooks";
import { useMeta } from "../lib/meta";
import type { Budget, BudgetInput, Freshness, RangeCode } from "../lib/types";
import { date, money, percent } from "../lib/format";
import { pushEnabledHere } from "../lib/push";
import { Empty, ErrorBox, FreshnessBanner, Modal, PageHeader, Spinner, StatusIcon } from "../components/ui";

const PERIOD_LABEL: Record<RangeCode, string> = {
  "1d": "Daily",
  "1w": "Weekly (Mon–Sun)",
  "2w": "Fortnightly",
  "1m": "Monthly",
  "3m": "Quarterly",
  custom: "Custom dates",
};

export function BudgetsPage() {
  const res = useResource((s) => api.get<{ budgets: Budget[]; freshness: Freshness }>("/budgets", undefined, s), []);
  const [editing, setEditing] = useState<Budget | "new" | null>(null);

  return (
    <div className="space-y-5">
      <PageHeader
        title="Budgets"
        subtitle="Get a push notification when spending in a domain reaches your limit"
        actions={
          <button className="btn-primary" onClick={() => setEditing("new")}>
            New budget
          </button>
        }
      />
      {!pushEnabledHere() && (
        <p className="text-sm text-ink-2">
          Alerts always appear in the inbox. <Link to="/settings" className="text-accent hover:underline">Enable push notifications</Link> to get them on this device.
        </p>
      )}
      <FreshnessBanner freshness={res.data?.freshness} />
      {res.error && <ErrorBox message={res.error} onRetry={res.reload} />}
      {res.loading && !res.data && <Spinner />}
      {res.data && res.data.budgets.length === 0 && (
        <Empty title="No budgets yet">Create one to be alerted when you approach a limit.</Empty>
      )}
      <div className="grid gap-4 md:grid-cols-2">
        {res.data?.budgets.map((b) => <BudgetCard key={b.id} budget={b} onEdit={() => setEditing(b)} />)}
      </div>
      {editing && (
        <BudgetForm
          budget={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            res.reload();
          }}
        />
      )}
    </div>
  );
}

const STATE: Record<Budget["state"], { label: string; icon: "good" | "warning" | "critical"; bar: string }> = {
  ok: { label: "On track", icon: "good", bar: "bg-accent" },
  warning: { label: "Near limit", icon: "warning", bar: "bg-warning" },
  exceeded: { label: "Over limit", icon: "critical", bar: "bg-critical" },
};

/** Meter: the fill carries severity; the track is a light step of the accent ramp. */
function BudgetCard({ budget: b, onEdit }: { budget: Budget; onEdit: () => void }) {
  const st = STATE[b.state];
  return (
    <div className={`card p-5 ${b.enabled ? "" : "opacity-60"}`}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="truncate font-semibold">{b.name}</h2>
          <p className="truncate text-xs text-ink-2">
            {b.domain_label}
            {b.class_label && ` · ${b.class_label}`} · {PERIOD_LABEL[b.range]}
            {!b.enabled && " · paused"}
          </p>
        </div>
        <button className="btn-ghost px-2.5 py-1 text-xs" onClick={onEdit}>
          Edit
        </button>
      </div>
      <div className="mt-4 flex items-baseline justify-between gap-2">
        <p className="num text-xl font-semibold">{money(b.spent, b.currency)}</p>
        <p className="num text-sm text-ink-2">of {money(b.limit, b.currency)}</p>
      </div>
      <div
        className="mt-2 h-2.5 w-full rounded-full bg-accent-soft"
        role="meter"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Number(b.percentage)}
        aria-label={`${b.name}: ${percent(b.percentage)} used`}
      >
        <div className={`h-2.5 rounded-full ${st.bar}`} style={{ width: `${Math.min(Number(b.percentage), 100)}%` }} />
      </div>
      <div className="mt-2 flex items-center justify-between text-xs">
        <span className="flex items-center gap-1.5">
          <StatusIcon state={st.icon} />
          {st.label} · {percent(b.percentage)}
        </span>
        <span className="text-ink-3">
          {date(b.period.from, { day: "numeric", month: "short" })} – {date(b.period.to, { day: "numeric", month: "short" })}
        </span>
      </div>
      <p className="num mt-1 text-xs text-ink-3">
        {money(b.remaining, b.currency)} left · alert at {b.warn_percent}%
      </p>
    </div>
  );
}

function BudgetForm({ budget, onClose, onSaved }: { budget: Budget | null; onClose: () => void; onSaved: () => void }) {
  const meta = useMeta();
  const [form, setForm] = useState<BudgetInput>(
    budget
      ? {
          name: budget.name,
          domain: budget.domain,
          class: budget.class,
          range: budget.range,
          custom_from: budget.custom_from,
          custom_to: budget.custom_to,
          limit: budget.limit,
          warn_percent: budget.warn_percent,
          enabled: budget.enabled,
        }
      : { name: "", domain: "spending_category", class: "", range: "1m", limit: "", warn_percent: 80, enabled: true },
  );
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const domainInfo = meta.domains.find((d) => d.domain === form.domain);
  const set = <K extends keyof BudgetInput>(k: K, v: BudgetInput[K]) => setForm((f) => ({ ...f, [k]: v }));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (budget) await api.put(`/budgets/${budget.id}`, form);
      else await api.post("/budgets", form);
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!budget || !window.confirm(`Delete budget "${budget.name}"?`)) return;
    try {
      await api.del(`/budgets/${budget.id}`);
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  };

  return (
    <Modal title={budget ? "Edit budget" : "New budget"} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        <div>
          <label className="label" htmlFor="b-name">Name</label>
          <input id="b-name" className="input" maxLength={80} value={form.name} onChange={(e) => set("name", e.target.value)} placeholder="Groceries" required />
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <label className="label" htmlFor="b-domain">Metric domain</label>
            <select id="b-domain" className="input" value={form.domain} onChange={(e) => setForm((f) => ({ ...f, domain: e.target.value as BudgetInput["domain"], class: "" }))}>
              {meta.domains.map((d) => (
                <option key={d.domain} value={d.domain}>{d.label}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="label" htmlFor="b-class">Class (optional)</label>
            <select id="b-class" className="input" value={form.class} onChange={(e) => set("class", e.target.value)}>
              <option value="">Whole domain</option>
              {domainInfo?.classes.map((c) => (
                <option key={c.class} value={c.class}>{c.label}</option>
              ))}
            </select>
          </div>
        </div>
        <div>
          <label className="label" htmlFor="b-range">Period</label>
          <select id="b-range" className="input" value={form.range} onChange={(e) => set("range", e.target.value as RangeCode)}>
            {(Object.keys(PERIOD_LABEL) as RangeCode[]).map((r) => (
              <option key={r} value={r}>{PERIOD_LABEL[r]}</option>
            ))}
          </select>
        </div>
        {form.range === "custom" && (
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="label" htmlFor="b-from">From</label>
              <input id="b-from" type="date" className="input" value={form.custom_from ?? ""} onChange={(e) => set("custom_from", e.target.value)} required />
            </div>
            <div>
              <label className="label" htmlFor="b-to">To</label>
              <input id="b-to" type="date" className="input" value={form.custom_to ?? ""} onChange={(e) => set("custom_to", e.target.value)} required />
            </div>
          </div>
        )}
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="label" htmlFor="b-limit">Limit ({meta.currency})</label>
            <input id="b-limit" className="input num" inputMode="decimal" value={form.limit} onChange={(e) => set("limit", e.target.value.replace(",", "."))} placeholder="400.00" required />
          </div>
          <div>
            <label className="label" htmlFor="b-warn">Alert at (%)</label>
            <input id="b-warn" type="number" min={1} max={100} className="input num" value={form.warn_percent} onChange={(e) => set("warn_percent", Number(e.target.value))} required />
          </div>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={form.enabled} onChange={(e) => set("enabled", e.target.checked)} />
          Notifications enabled
        </label>
        {error && <p className="text-sm text-critical">{error}</p>}
        <div className="flex flex-wrap justify-between gap-2">
          {budget ? (
            <button type="button" className="btn-danger" onClick={() => void remove()}>Delete</button>
          ) : (
            <span />
          )}
          <div className="flex gap-2">
            <button type="button" className="btn-ghost" onClick={onClose}>Cancel</button>
            <button type="submit" className="btn-primary" disabled={busy}>{budget ? "Save" : "Create"}</button>
          </div>
        </div>
      </form>
    </Modal>
  );
}
