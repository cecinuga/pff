import { useState, type FormEvent } from "react";
import { TotpMultiFactorGenerator } from "firebase/auth";
import { useAuth } from "../lib/auth";
import {
  firebaseMessage,
  isMfaRequiredError,
  resolveWithSms,
  resolveWithTotp,
  resolverFor,
  sendSignInSms,
  type MultiFactorResolver,
} from "../lib/mfa";

export function LoginPage() {
  const { config, signInWithGoogle, signInDev, firebaseAuth } = useAuth();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [resolver, setResolver] = useState<MultiFactorResolver | null>(null);

  const google = async () => {
    setBusy(true);
    setError(null);
    try {
      await signInWithGoogle();
    } catch (e) {
      if (isMfaRequiredError(e) && firebaseAuth) setResolver(resolverFor(firebaseAuth, e));
      else setError(firebaseMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center p-4">
      <div className="card w-full max-w-sm p-6">
        <h1 className="text-2xl font-semibold tracking-tight">
          PFF<span className="text-accent">.</span>
        </h1>
        <p className="mt-1 text-sm text-ink-2">Personal Finance Fotonica — your spending, income and budgets in one place.</p>

        <div className="mt-6">
          {resolver ? (
            <SecondFactor resolver={resolver} onCancel={() => setResolver(null)} />
          ) : config?.auth_mode === "dev" ? (
            <DevLogin onSubmit={signInDev} />
          ) : (
            <button className="btn-primary w-full" onClick={() => void google()} disabled={busy}>
              <svg viewBox="0 0 24 24" className="h-4 w-4" aria-hidden>
                <path fill="currentColor" d="M21.6 12.2c0-.7-.1-1.4-.2-2H12v3.8h5.4a4.6 4.6 0 01-2 3v2.5h3.2c1.9-1.7 3-4.3 3-7.3zM12 22c2.7 0 5-.9 6.6-2.4l-3.2-2.5c-.9.6-2 1-3.4 1-2.6 0-4.8-1.8-5.6-4.1H3.1v2.6A10 10 0 0012 22zM6.4 14a6 6 0 010-3.9V7.5H3.1a10 10 0 000 9zM12 6c1.5 0 2.8.5 3.8 1.5l2.9-2.9A10 10 0 003.1 7.5L6.4 10C7.2 7.8 9.4 6 12 6z" />
              </svg>
              Sign in with Google
            </button>
          )}
          {error && (
            <p role="alert" className="mt-3 text-sm text-critical">
              {error}
            </p>
          )}
        </div>
        <p className="mt-6 text-xs text-ink-3">Access is limited to invited accounts.</p>
      </div>
    </div>
  );
}

function DevLogin({ onSubmit }: { onSubmit: (email: string) => Promise<void> }) {
  const [email, setEmail] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (email.includes("@")) void onSubmit(email);
  };
  return (
    <form onSubmit={submit} className="space-y-3">
      <div className="rounded-lg border border-warning/50 p-2 text-xs text-ink-2">
        Development mode: no Google sign-in. Use an address listed in allowed-emails.txt.
      </div>
      <label className="label" htmlFor="dev-email">
        E-mail
      </label>
      <input id="dev-email" type="email" className="input" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" required />
      <button className="btn-primary w-full" type="submit">
        Sign in (dev)
      </button>
    </form>
  );
}

/** Completes a Firebase sign-in that requires TOTP or SMS. */
function SecondFactor({ resolver, onCancel }: { resolver: MultiFactorResolver; onCancel: () => void }) {
  const { firebaseAuth } = useAuth();
  const [hintIndex, setHintIndex] = useState(0);
  const [code, setCode] = useState("");
  const [verificationId, setVerificationId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const hint = resolver.hints[hintIndex];
  const isTotp = hint?.factorId === TotpMultiFactorGenerator.FACTOR_ID;

  const sendSms = async () => {
    if (!firebaseAuth) return;
    setBusy(true);
    setError(null);
    try {
      setVerificationId(await sendSignInSms(firebaseAuth, resolver, hintIndex, "recaptcha-signin"));
    } catch (e) {
      setError(firebaseMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const verify = async (e: FormEvent) => {
    e.preventDefault();
    if (!hint) return;
    setBusy(true);
    setError(null);
    try {
      if (isTotp) await resolveWithTotp(resolver, hint.uid, code.trim());
      else if (verificationId) await resolveWithSms(resolver, verificationId, code.trim());
    } catch (err) {
      setError(firebaseMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={verify} className="space-y-3">
      <p className="text-sm font-medium">Second factor required</p>
      {resolver.hints.length > 1 && (
        <select className="input" value={hintIndex} onChange={(e) => { setHintIndex(Number(e.target.value)); setVerificationId(null); setCode(""); }}>
          {resolver.hints.map((h, i) => (
            <option key={h.uid} value={i}>
              {h.displayName || (h.factorId === "totp" ? "Authenticator app" : "Phone")}
            </option>
          ))}
        </select>
      )}
      {!isTotp && !verificationId ? (
        <button type="button" className="btn-primary w-full" onClick={() => void sendSms()} disabled={busy}>
          Send SMS code
        </button>
      ) : (
        <>
          <label className="label" htmlFor="mfa-code">
            {isTotp ? "Code from your authenticator app" : "Code sent by SMS"}
          </label>
          <input id="mfa-code" className="input num tracking-widest" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} required />
          <button className="btn-primary w-full" type="submit" disabled={busy || code.trim().length < 6}>
            Verify
          </button>
        </>
      )}
      <button type="button" className="btn-ghost w-full" onClick={onCancel}>
        Cancel
      </button>
      {error && <p className="text-sm text-critical">{error}</p>}
      <div id="recaptcha-signin" />
    </form>
  );
}
