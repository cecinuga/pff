import { useState, type FormEvent } from "react";
import { api, ApiError } from "../lib/api";
import { useAuth } from "../lib/auth";
import { SecurityPanel } from "./Settings";

function Shell({ title, children }: { title: string; children: React.ReactNode }) {
  const { signOut, me } = useAuth();
  return (
    <div className="flex min-h-screen items-center justify-center p-4">
      <div className="card w-full max-w-md p-6">
        <h1 className="text-xl font-semibold">{title}</h1>
        <div className="mt-4">{children}</div>
        <div className="mt-6 flex items-center justify-between border-t border-line pt-4 text-xs text-ink-2">
          <span className="truncate">{me?.user.email}</span>
          <button className="text-accent hover:underline" onClick={() => void signOut()}>
            Sign out
          </button>
        </div>
      </div>
    </div>
  );
}

export function NotWhitelisted() {
  const { signOut } = useAuth();
  return (
    <div className="flex min-h-screen items-center justify-center p-4">
      <div className="card w-full max-w-md p-6 text-center">
        <h1 className="text-xl font-semibold">Access not allowed</h1>
        <p className="mt-2 text-sm text-ink-2">This Google account is not on the PFF allow-list. Ask the administrator to add your e-mail address.</p>
        <button className="btn-ghost mt-5" onClick={() => void signOut()}>
          Use another account
        </button>
      </div>
    </div>
  );
}

/** The session must pass the e-mail one-time code (backend-managed factor). */
export function EmailMfaGate() {
  const { refreshMe, me } = useAuth();
  const [challenge, setChallenge] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const send = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await api.post<{ challenge_id: string }>("/auth/mfa/email/challenge", { purpose: "login" });
      setChallenge(r.challenge_id);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const verify = async (e: FormEvent) => {
    e.preventDefault();
    if (!challenge) return;
    setBusy(true);
    setError(null);
    try {
      await api.post("/auth/mfa/email/verify", { challenge_id: challenge, code: code.trim() });
      await refreshMe();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Shell title="Verify it's you">
      <p className="text-sm text-ink-2">
        Your account uses e-mail as a second factor. We will send a 6-digit code to <span className="font-medium text-ink">{me?.user.email}</span>.
      </p>
      {!challenge ? (
        <button className="btn-primary mt-4 w-full" onClick={() => void send()} disabled={busy}>
          Send code
        </button>
      ) : (
        <form onSubmit={verify} className="mt-4 space-y-3">
          <label className="label" htmlFor="email-code">
            Code
          </label>
          <input id="email-code" className="input num tracking-widest" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} required />
          <button className="btn-primary w-full" type="submit" disabled={busy || code.trim().length !== 6}>
            Verify
          </button>
          <button type="button" className="btn-ghost w-full" onClick={() => void send()} disabled={busy}>
            Send a new code
          </button>
        </form>
      )}
      {error && <p className="mt-3 text-sm text-critical">{error}</p>}
    </Shell>
  );
}

/** Policy requires a second factor and none is enrolled yet. */
export function EnrollmentGate() {
  return (
    <Shell title="Add a second factor">
      <p className="mb-4 text-sm text-ink-2">PFF requires two-factor authentication. Add one factor to continue.</p>
      <SecurityPanel gate />
    </Shell>
  );
}
