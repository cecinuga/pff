import { useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { api, ApiError } from "../lib/api";
import { useResource } from "../lib/hooks";
import type { Bank, Connection } from "../lib/types";
import { date, dateTime, maskIban, money } from "../lib/format";
import { Empty, ErrorBox, PageHeader, Spinner, StatusIcon } from "../components/ui";

const STATUS: Record<Connection["status"], { label: string; icon: "good" | "warning" | "critical" }> = {
  active: { label: "Active", icon: "good" },
  pending: { label: "Waiting for bank authorisation", icon: "warning" },
  expired: { label: "Consent expired", icon: "warning" },
  revoked: { label: "Disconnected", icon: "critical" },
  failed: { label: "Authorisation failed", icon: "critical" },
};

export function ConnectionsPage() {
  const res = useResource((s) => api.get<{ connections: Connection[] }>("/connections", undefined, s), []);
  const [params] = useSearchParams();
  const justConnected = params.get("connected") === "1";

  const revoke = async (c: Connection) => {
    if (!window.confirm(`Disconnect ${c.aspsp_name}? Data already imported is kept.`)) return;
    await api.del(`/connections/${c.id}`).catch(() => undefined);
    res.reload();
  };

  return (
    <div className="space-y-5">
      <PageHeader title="Bank connections" subtitle="PFF reads your accounts through a PSD2 consent you can revoke at any time" />
      <ConfiguredBanks connections={res.data?.connections ?? []} />
      {justConnected && (
        <div role="status" className="card flex items-center gap-2 border-good/40 p-3 text-sm">
          <StatusIcon state="good" /> Bank connected. Your transaction history is being imported in the background.
        </div>
      )}
      {res.error && <ErrorBox message={res.error} onRetry={res.reload} />}
      {res.loading && !res.data && <Spinner />}
      {res.data && res.data.connections.length === 0 && <Empty title="No banks connected yet" />}
      <div className="space-y-4">
        {res.data?.connections.map((c) => {
          const st = STATUS[c.status];
          return (
            <section key={c.id} className="card p-5">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <h2 className="font-semibold">{c.aspsp_name}</h2>
                  <p className="mt-0.5 flex items-center gap-1.5 text-xs text-ink-2">
                    <StatusIcon state={st.icon} />
                    {st.label}
                    {c.valid_until && c.status === "active" && ` · valid until ${date(c.valid_until)}`}
                  </p>
                  {c.last_error && c.status === "failed" && <p className="mt-1 text-xs text-ink-3">{c.last_error}</p>}
                </div>
                {c.status === "active" && (
                  <button className="btn-danger" onClick={() => void revoke(c)}>Disconnect</button>
                )}
              </div>
              {c.accounts.length > 0 && (
                <ul className="mt-4 divide-y divide-line">
                  {c.accounts.map((a) => (
                    <li key={a.id} className="flex items-center justify-between gap-3 py-2 text-sm">
                      <span className="min-w-0">
                        <span className="block truncate">{a.name || "Account"}</span>
                        <span className="block text-xs text-ink-3">
                          {maskIban(a.iban)} · {a.cash_account_type}
                        </span>
                      </span>
                      <span className="text-right">
                        <span className="num block font-medium">{a.balance ? money(a.balance, a.balance_currency || a.currency) : "—"}</span>
                        {a.balance_at && <span className="block text-xs text-ink-3">{dateTime(a.balance_at)}</span>}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          );
        })}
      </div>
    </div>
  );
}

/**
 * The banks PFF may connect come from the server configuration (PFF_BANKS),
 * so the user only chooses when to (re)connect, never which bank.
 */
function ConfiguredBanks({ connections }: { connections: Connection[] }) {
  const banks = useResource((s) => api.get<{ banks: Bank[] }>("/banks", undefined, s), []);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const connect = async (b: Bank) => {
    setBusy(b.name);
    setError(null);
    try {
      const r = await api.post<{ redirect_url: string }>("/connections", { aspsp_name: b.name, aspsp_country: b.country });
      // Leave PFF to authenticate at the bank (SCA); it redirects back to /connections/callback.
      window.location.assign(r.redirect_url);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
      setBusy(null);
    }
  };

  const isActive = (b: Bank) => connections.some((c) => c.status === "active" && c.aspsp_name === b.name && c.aspsp_country === b.country);

  return (
    <section className="card p-5">
      <h2 className="font-semibold">Your banks</h2>
      {banks.error && <ErrorBox message={banks.error} onRetry={banks.reload} />}
      {banks.loading && <Spinner />}
      <ul className="mt-3 divide-y divide-line">
        {banks.data?.banks.map((b) => (
          <li key={`${b.country}:${b.name}`} className="flex items-center gap-3 py-2.5 text-sm">
            {b.logo ? <img src={b.logo} alt="" className="h-7 w-7 rounded object-contain" /> : <span className="h-7 w-7 rounded bg-sunken" />}
            <span className="min-w-0 flex-1">
              <span className="block truncate">{b.name}</span>
              <span className="block text-xs text-ink-3">
                {b.country}
                {!b.available && " · not offered by the banking provider, check PFF_BANKS"}
              </span>
            </span>
            <button
              className={isActive(b) ? "btn-ghost" : "btn-primary"}
              disabled={!b.available || busy !== null}
              onClick={() => void connect(b)}
            >
              {busy === b.name ? "Redirecting…" : isActive(b) ? "Reconnect" : "Connect"}
            </button>
          </li>
        ))}
      </ul>
      {error && <p className="mt-2 text-sm text-critical">{error}</p>}
      <p className="mt-3 text-xs text-ink-3">You authenticate at your bank. PFF never sees your bank credentials.</p>
    </section>
  );
}

/** Landing page of the bank redirect: completes the consent with the backend. */
export function ConnectionCallbackPage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const sent = useRef(false);

  useEffect(() => {
    if (sent.current) return; // the code is single-use: never post it twice
    sent.current = true;
    api
      .post("/connections/callback", {
        code: params.get("code") ?? "",
        state: params.get("state") ?? "",
        error: params.get("error") ?? "",
        error_description: params.get("error_description") ?? "",
      })
      .then(() => navigate("/connections?connected=1", { replace: true }))
      .catch((e) => setError(e instanceof ApiError ? e.message : String(e)));
  }, [params, navigate]);

  return (
    <div className="mx-auto max-w-md py-10">
      {error ? (
        <div className="space-y-4">
          <ErrorBox message={error} />
          <button className="btn-ghost" onClick={() => navigate("/connections", { replace: true })}>Back to bank connections</button>
        </div>
      ) : (
        <Spinner label="Finishing bank connection" />
      )}
    </div>
  );
}
