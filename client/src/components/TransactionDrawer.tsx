import { useState } from "react";
import { api } from "../lib/api";
import { useResource } from "../lib/hooks";
import type { DomainInfo, TransactionDetail } from "../lib/types";
import { date, dateTime, maskIban, money } from "../lib/format";
import { ErrorBox, Modal, Spinner } from "./ui";

/**
 * Complete detail of one history entry (FunctionalSpec 2.1): every field PFF
 * stores, the metric domains/classes it counts in, and the raw bank record.
 */
export function TransactionDrawer({ id, categories, onClose, onChanged }: {
  id: string;
  categories: DomainInfo | undefined;
  onClose: () => void;
  onChanged: () => void;
}) {
  const res = useResource((s) => api.get<{ transaction: TransactionDetail }>(`/transactions/${id}`, undefined, s), [id]);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const tx = res.data?.transaction;

  const setCategory = async (category: string) => {
    setSaving(true);
    setErr(null);
    try {
      await api.put(`/transactions/${id}/category`, { category });
      res.reload();
      onChanged();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  const spendingCategory = tx?.memberships.find((m) => m.domain === "spending_category");

  return (
    <Modal title="Transaction detail" onClose={onClose}>
      {res.loading && !tx && <Spinner />}
      {res.error && <ErrorBox message={res.error} onRetry={res.reload} />}
      {tx && (
        <div className="space-y-5 text-sm">
          <div>
            <p className="num text-2xl font-semibold">
              {tx.direction === "DBIT" ? "−" : "+"}
              {money(tx.amount, tx.currency)}
            </p>
            <p className="text-ink-2">{tx.counterparty || tx.description || "—"}</p>
          </div>

          <dl className="grid grid-cols-[auto,1fr] gap-x-4 gap-y-1.5">
            <Row k="Booking date" v={date(tx.booking_date)} />
            <Row k="Value date" v={date(tx.value_date)} />
            {tx.transaction_date && <Row k="Transaction date" v={date(tx.transaction_date)} />}
            <Row k="Direction" v={tx.direction === "DBIT" ? "Outgoing (debit)" : "Incoming (credit)"} />
            <Row k="Account" v={`${tx.account_name || "—"}${tx.account_iban ? ` · ${maskIban(tx.account_iban)}` : ""}`} />
            <Row k="Counterparty" v={tx.counterparty || "—"} />
            {tx.counterparty_iban && <Row k="Counterparty IBAN" v={tx.counterparty_iban} mono />}
            <Row k="Description" v={tx.description || "—"} />
            {tx.bank_tx_description && <Row k="Bank type" v={tx.bank_tx_description} />}
            {(tx.bank_tx_code || tx.bank_tx_sub_code) && <Row k="Bank code" v={[tx.bank_tx_code, tx.bank_tx_sub_code].filter(Boolean).join(" / ")} mono />}
            {tx.mcc && <Row k="Merchant category (MCC)" v={tx.mcc} mono />}
            {tx.balance_after && <Row k="Balance after" v={money(tx.balance_after, tx.currency)} />}
            {tx.entry_reference && <Row k="Bank reference" v={tx.entry_reference} mono />}
            <Row k="Fetched from bank" v={dateTime(tx.fetched_at)} />
          </dl>

          <div>
            <h3 className="mb-2 text-xs font-medium uppercase tracking-wide text-ink-3">Counts in</h3>
            <ul className="flex flex-wrap gap-1.5">
              {tx.memberships.map((m) => (
                <li key={m.domain} className="rounded-full border border-line px-2.5 py-0.5 text-xs">
                  {m.domain_label}: <span className="font-medium">{m.class_label}</span>
                </li>
              ))}
            </ul>
          </div>

          {spendingCategory && categories && (
            <div>
              <label className="label" htmlFor="category">
                Spending category {tx.category_override ? "(set by you)" : "(automatic)"}
              </label>
              <div className="flex gap-2">
                <select
                  id="category"
                  className="input"
                  disabled={saving}
                  value={spendingCategory.class}
                  onChange={(e) => void setCategory(e.target.value)}
                >
                  {categories.classes.map((c) => (
                    <option key={c.class} value={c.class}>
                      {c.label}
                    </option>
                  ))}
                </select>
                {tx.category_override && (
                  <button className="btn-ghost shrink-0" disabled={saving} onClick={() => void setCategory("")}>
                    Reset
                  </button>
                )}
              </div>
              {err && <p className="mt-1 text-xs text-critical">{err}</p>}
            </div>
          )}

          <details>
            <summary className="cursor-pointer text-xs text-ink-2">Raw bank record</summary>
            <pre className="mt-2 max-h-64 overflow-auto rounded-lg bg-sunken p-3 text-xs">{JSON.stringify(tx.raw, null, 2)}</pre>
          </details>
        </div>
      )}
    </Modal>
  );
}

function Row({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-ink-3">{k}</dt>
      <dd className={`min-w-0 break-words ${mono ? "font-mono text-xs leading-5" : ""}`}>{v}</dd>
    </>
  );
}
