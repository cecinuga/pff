// Authentication state for the whole app.
//
// Firebase mode: Google sign-in through Firebase; second factors (TOTP, SMS)
// are enforced by Firebase itself at sign-in, e-mail codes by the backend.
// Dev mode: a local e-mail stands in for a Google account (backend must run
// with PFF_ENV=development and PFF_AUTH_MODE=dev).
//
// Whether an account may use PFF is decided by the backend (whitelist); the
// client only reflects the answer of GET /me.
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  GoogleAuthProvider,
  onIdTokenChanged,
  signInWithPopup,
  signOut as fbSignOut,
  type Auth,
  type User,
} from "firebase/auth";
import { api, ApiError, loadPublicConfig, onAuthProblem, setTokenProvider } from "./api";
import { initFirebase } from "./firebase";
import type { Me, PublicConfig } from "./types";

const DEV_KEY = "pff.devEmail";

export type AuthStatus =
  | "loading"
  | "signed_out"
  | "checking"
  | "ready"
  | "not_whitelisted"
  | "mfa_required"
  | "mfa_enrollment_required"
  | "error";

interface AuthContextValue {
  config: PublicConfig | null;
  status: AuthStatus;
  me: Me | null;
  error: string | null;
  firebaseAuth: Auth | null;
  firebaseUser: User | null;
  signInWithGoogle: () => Promise<void>;
  signInDev: (email: string) => Promise<void>;
  signOut: () => Promise<void>;
  refreshMe: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

function readDevEmail(): string | null {
  try {
    return localStorage.getItem(DEV_KEY);
  } catch {
    return null;
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [config, setConfig] = useState<PublicConfig | null>(null);
  const [status, setStatus] = useState<AuthStatus>("loading");
  const [me, setMe] = useState<Me | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [firebaseAuth, setFirebaseAuth] = useState<Auth | null>(null);
  const [firebaseUser, setFirebaseUser] = useState<User | null>(null);
  const devEmail = useRef<string | null>(readDevEmail());

  const refreshMe = useCallback(async () => {
    setStatus((s) => (s === "ready" ? s : "checking"));
    try {
      const m = await api.get<Me>("/me");
      setMe(m);
      setStatus(m.mfa.state === "ok" ? "ready" : m.mfa.state);
      setError(null);
    } catch (e) {
      if (e instanceof ApiError) {
        if (e.code === "not_whitelisted") return setStatus("not_whitelisted");
        if (e.code === "unauthenticated") return setStatus("signed_out");
        setError(e.message);
      } else {
        setError(String(e));
      }
      setStatus("error");
    }
  }, []);

  // Boot: load runtime config, then wire the token provider for the mode.
  useEffect(() => {
    let unsubscribe: (() => void) | undefined;
    loadPublicConfig()
      .then((cfg) => {
        setConfig(cfg);
        if (cfg.auth_mode === "dev") {
          setTokenProvider(async () => (devEmail.current ? `dev:${devEmail.current}` : null));
          if (devEmail.current) void refreshMe();
          else setStatus("signed_out");
          return;
        }
        const { auth } = initFirebase(cfg);
        setFirebaseAuth(auth);
        setTokenProvider(async () => (auth.currentUser ? auth.currentUser.getIdToken() : null));
        unsubscribe = onIdTokenChanged(auth, (u) => {
          setFirebaseUser(u);
          if (u) void refreshMe();
          else {
            setMe(null);
            setStatus("signed_out");
          }
        });
      })
      .catch((e) => {
        setError(String(e));
        setStatus("error");
      });
    return () => unsubscribe?.();
  }, [refreshMe]);

  // Any API call that hits an auth problem updates the global state.
  useEffect(() => {
    onAuthProblem((code) => {
      if (code === "unauthenticated") setStatus("signed_out");
      else if (code === "not_whitelisted") setStatus("not_whitelisted");
      else if (code === "mfa_required" || code === "mfa_enrollment_required") setStatus(code);
    });
  }, []);

  const signInWithGoogle = useCallback(async () => {
    if (!firebaseAuth) return;
    const provider = new GoogleAuthProvider();
    provider.setCustomParameters({ prompt: "select_account" });
    await signInWithPopup(firebaseAuth, provider);
  }, [firebaseAuth]);

  const signInDev = useCallback(
    async (email: string) => {
      devEmail.current = email.trim().toLowerCase();
      try {
        localStorage.setItem(DEV_KEY, devEmail.current);
      } catch {
        /* storage unavailable: session-only sign-in */
      }
      await refreshMe();
    },
    [refreshMe],
  );

  const signOut = useCallback(async () => {
    if (config?.auth_mode === "dev") {
      devEmail.current = null;
      try {
        localStorage.removeItem(DEV_KEY);
      } catch {
        /* ignore */
      }
      setMe(null);
      setStatus("signed_out");
      return;
    }
    if (firebaseAuth) await fbSignOut(firebaseAuth);
  }, [config, firebaseAuth]);

  const value = useMemo<AuthContextValue>(
    () => ({ config, status, me, error, firebaseAuth, firebaseUser, signInWithGoogle, signInDev, signOut, refreshMe }),
    [config, status, me, error, firebaseAuth, firebaseUser, signInWithGoogle, signInDev, signOut, refreshMe],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth outside AuthProvider");
  return ctx;
}
