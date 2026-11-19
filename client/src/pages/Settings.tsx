import { useEffect, useState, type FormEvent } from "react";
import QRCode from "qrcode";
import { api, ApiError } from "../lib/api";
import { useAuth } from "../lib/auth";
import {
  enrolledFactors,
  finishSmsEnrollment,
  finishTotpEnrollment,
  firebaseMessage,
  resetRecaptcha,
  startSmsEnrollment,
  startTotpEnrollment,
  unenroll,
  type TotpSecret,
} from "../lib/mfa";
import { disablePush, enablePush, pushEnabledHere, pushPermission, pushSupported } from "../lib/push";
import { PageHeader } from "../components/ui";

export function SettingsPage() {
  const { me, config } = useAuth();
  return (
    <div className="space-y-6">
      <PageHeader title="Settings" />
      <section className="card p-5">
        <h2 className="font-semibold">Account</h2>
        <dl className="mt-3 grid grid-cols-[auto,1fr] gap-x-6 gap-y-1 text-sm">
          <dt className="text-ink-3">E-mail</dt>
          <dd>{me?.user.email}</dd>
          <dt className="text-ink-3">Second factor this session</dt>
          <dd>{me?.mfa.method ? labelFor(me.mfa.method) : "none"}</dd>
          <dt className="text-ink-3">Sign-in</dt>
          <dd>{config?.auth_mode === "dev" ? "Development (no Google)" : "Google"}</dd>
        </dl>
      </section>
      <section className="card p-5">
        <h2 className="font-semibold">Two-factor authentication</h2>
        <p className="mb-4 mt-1 text-sm text-ink-2">
          Protect your account with an authenticator app (Google Authenticator), SMS or e-mail codes.
          {me?.mfa.required_by_policy && " Your administrator requires at least one factor."}
        </p>
        <SecurityPanel />
      </section>
      <PushSection />
    </div>
  );
}

function labelFor(m: string) {
  return m === "totp" ? "Authenticator app" : m === "phone" ? "SMS" : m === "email" ? "E-mail code" : m;
}

/** Second-factor management, also used by the enrolment gate. */
export function SecurityPanel({ gate = false }: { gate?: boolean }) {
  const { config, firebaseUser, firebaseAuth, me, refreshMe } = useAuth();
  const [tick, setTick] = useState(0);
  const [mode, setMode] = useState<"none" | "totp" | "sms" | "email">("none");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const factors = firebaseUser ? enrolledFactors(firebaseUser) : [];
  const firebaseMode = config?.auth_mode === "firebase" && !!firebaseUser && !!firebaseAuth;

  const done = async (msg: string) => {
    setMode("none");
    setError(null);
    setNotice(msg);
    setTick((t) => t + 1);
    if (firebaseUser) await firebaseUser.getIdToken(true).catch(() => undefined);
    await refreshMe();
  };

  const remove = async (uid: string) => {
    if (!firebaseUser) return;
    try {
      await unenroll(firebaseUser, uid);
      await done("Factor removed.");
    } catch (e) {
      setError(firebaseMessage(e));
    }
  };

  const disableEmail = async () => {
    try {
      await api.del("/auth/mfa/email");
      await done("E-mail codes disabled.");
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  };

  return (
    <div className="space-y-3" key={tick}>
      <ul className="divide-y divide-line rounded-lg border border-line">
        {factors.map((f) => (
          <li key={f.uid} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
            <span>
              {f.displayName || labelFor(f.factorId)}
              <span className="ml-2 text-xs text-ink-3">{f.factorId === "totp" ? "Authenticator app" : "SMS"}</span>
            </span>
            {!gate && (
              <button className="text-xs text-critical hover:underline" onClick={() => void remove(f.uid)}>
                Remove
              </button>
            )}
          </li>
        ))}
        {me?.mfa.email_enabled && (
          <li className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
            <span>
              E-mail code <span className="ml-2 text-xs text-ink-3">{me.user.email}</span>
            </span>
            {!gate && (
              <button className="text-xs text-critical hover:underline" onClick={() => void disableEmail()}>
                Remove
              </button>
            )}
          </li>
        )}
        {factors.length === 0 && !me?.mfa.email_enabled && <li className="px-3 py-2 text-sm text-ink-2">No second factor enrolled.</li>}
      </ul>

      {mode === "none" && (
        <div className="flex flex-wrap gap-2">
          {firebaseMode && (
            <>
              <button className="btn-ghost" onClick={() => { setNotice(null); setMode("totp"); }}>
                Add authenticator app
              </button>
              <button className="btn-ghost" onClick={() => { setNotice(null); setMode("sms"); }}>
                Add SMS
              </button>
            </>
          )}
          {!me?.mfa.email_enabled && (
            <button className="btn-ghost" onClick={() => { setNotice(null); setMode("email"); }}>
              Add e-mail codes
            </button>
          )}
        </div>
      )}
      {mode === "totp" && firebaseUser && <TotpEnroll onDone={() => void done("Authenticator app added. Use it at your next sign-in.")} onCancel={() => setMode("none")} />}
      {mode === "sms" && firebaseUser && <SmsEnroll onDone={() => void done("Phone added. Use it at your next sign-in.")} onCancel={() => setMode("none")} />}
      {mode === "email" && <EmailEnroll onDone={() => void done("E-mail codes enabled.")} onCancel={() => setMode("none")} />}

      {notice && <p className="text-sm text-good">{notice}{gate && " If PFF still asks for a factor, sign out and sign in again."}</p>}
      {error && <p className="text-sm text-critical">{error}</p>}
    </div>
  );
}

function TotpEnroll({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const { firebaseUser, me } = useAuth();
  const [secret, setSecret] = useState<TotpSecret | null>(null);
  const [qr, setQr] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!firebaseUser) return;
    startTotpEnrollment(firebaseUser)
      .then(async (s) => {
        setSecret(s);
        setQr(await QRCode.toDataURL(s.generateQrCodeUrl(me?.user.email ?? "PFF user", "PFF"), { margin: 1, width: 192 }));
      })
      .catch((e) => setError(firebaseMessage(e)));
  }, [firebaseUser, me]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!firebaseUser || !secret) return;
    try {
      await finishTotpEnrollment(firebaseUser, secret, code.trim());
      onDone();
    } catch (err) {
      setError(firebaseMessage(err));
    }
  };

  return (
    <form onSubmit={submit} className="space-y-3 rounded-lg border border-line p-4">
      <p className="text-sm">Scan the QR code with Google Authenticator (or any TOTP app), then enter the 6-digit code.</p>
      {qr && <img src={qr} alt="TOTP QR code" className="h-48 w-48 rounded-lg bg-white p-1" />}
      {secret && (
        <p className="break-all font-mono text-xs text-ink-2">
          Manual key: {secret.secretKey}
        </p>
      )}
      <input className="input num tracking-widest" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" required />
      <div className="flex gap-2">
        <button className="btn-primary" type="submit" disabled={!secret || code.trim().length !== 6}>
          Verify and add
        </button>
        <button type="button" className="btn-ghost" onClick={onCancel}>
          Cancel
        </button>
      </div>
      {error && <p className="text-sm text-critical">{error}</p>}
    </form>
  );
}

function SmsEnroll({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const { firebaseUser, firebaseAuth } = useAuth();
  const [phone, setPhone] = useState("");
  const [verificationId, setVerificationId] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => () => resetRecaptcha(), []);

  const send = async (e: FormEvent) => {
    e.preventDefault();
    if (!firebaseUser || !firebaseAuth) return;
    try {
      setVerificationId(await startSmsEnrollment(firebaseAuth, firebaseUser, phone.trim(), "recaptcha-enroll"));
      setError(null);
    } catch (err) {
      setError(firebaseMessage(err));
      resetRecaptcha();
    }
  };

  const verify = async (e: FormEvent) => {
    e.preventDefault();
    if (!firebaseUser || !verificationId) return;
    try {
      await finishSmsEnrollment(firebaseUser, verificationId, code.trim());
      onDone();
    } catch (err) {
      setError(firebaseMessage(err));
    }
  };

  return (
    <div className="space-y-3 rounded-lg border border-line p-4">
      <p className="text-xs text-ink-2">SMS codes can be intercepted (SIM swapping). Prefer an authenticator app when possible.</p>
      {!verificationId ? (
        <form onSubmit={send} className="space-y-3">
          <label className="label" htmlFor="phone">
            Phone number (international format)
          </label>
          <input id="phone" className="input" type="tel" placeholder="+39 333 123 4567" value={phone} onChange={(e) => setPhone(e.target.value)} required />
          <div className="flex gap-2">
            <button className="btn-primary" type="submit">
              Send code
            </button>
            <button type="button" className="btn-ghost" onClick={onCancel}>
              Cancel
            </button>
          </div>
        </form>
      ) : (
        <form onSubmit={verify} className="space-y-3">
          <label className="label" htmlFor="sms-code">
            Code sent by SMS
          </label>
          <input id="sms-code" className="input num tracking-widest" inputMode="numeric" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} required />
          <button className="btn-primary" type="submit" disabled={code.trim().length !== 6}>
            Verify and add
          </button>
        </form>
      )}
      {error && <p className="text-sm text-critical">{error}</p>}
      <div id="recaptcha-enroll" />
    </div>
  );
}

function EmailEnroll({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const { me } = useAuth();
  const [challenge, setChallenge] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);

  const send = async () => {
    try {
      const r = await api.post<{ challenge_id: string }>("/auth/mfa/email/challenge", { purpose: "enroll" });
      setChallenge(r.challenge_id);
      setError(null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  };

  const verify = async (e: FormEvent) => {
    e.preventDefault();
    if (!challenge) return;
    try {
      await api.post("/auth/mfa/email/verify", { challenge_id: challenge, code: code.trim() });
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  };

  return (
    <div className="space-y-3 rounded-lg border border-line p-4">
      <p className="text-sm">
        Codes will be sent to <span className="font-medium">{me?.user.email}</span>. Note: this is the same mailbox as your Google account, so it is the weakest of the three factors.
      </p>
      {!challenge ? (
        <div className="flex gap-2">
          <button className="btn-primary" onClick={() => void send()}>
            Send code
          </button>
          <button className="btn-ghost" onClick={onCancel}>
            Cancel
          </button>
        </div>
      ) : (
        <form onSubmit={verify} className="space-y-3">
          <input className="input num tracking-widest" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" required />
          <button className="btn-primary" type="submit" disabled={code.trim().length !== 6}>
            Verify and enable
          </button>
        </form>
      )}
      {error && <p className="text-sm text-critical">{error}</p>}
    </div>
  );
}

function PushSection() {
  const { config } = useAuth();
  const [supported, setSupported] = useState<boolean | null>(null);
  const [enabled, setEnabled] = useState(pushEnabledHere());
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void pushSupported(config).then(setSupported);
  }, [config]);

  const toggle = async () => {
    if (!config) return;
    setBusy(true);
    setError(null);
    try {
      if (enabled) {
        await disablePush();
        setEnabled(false);
      } else {
        await enablePush(config);
        setEnabled(true);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="card p-5">
      <h2 className="font-semibold">Push notifications</h2>
      <p className="mt-1 text-sm text-ink-2">Budget alerts and bank-consent reminders on this device. On iPhone, add PFF to the Home Screen first.</p>
      {supported === false ? (
        <p className="mt-3 text-sm text-ink-3">
          {config?.push_enabled ? "This browser does not support push notifications." : "Push notifications are not configured on this server."}
          {" "}Notifications still appear in the in-app inbox.
        </p>
      ) : (
        <div className="mt-3 flex items-center gap-3">
          <button className={enabled ? "btn-ghost" : "btn-primary"} onClick={() => void toggle()} disabled={busy || supported === null}>
            {enabled ? "Disable on this device" : "Enable on this device"}
          </button>
          {pushPermission() === "denied" && <span className="text-xs text-critical">Blocked in browser settings</span>}
        </div>
      )}
      {error && <p className="mt-2 text-sm text-critical">{error}</p>}
    </section>
  );
}
