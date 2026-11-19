// Web push through Firebase Cloud Messaging. The service worker (public/sw.js)
// receives the Firebase web config in its URL so one static file serves every
// environment.
import { deleteToken, getMessaging, getToken, isSupported, onMessage } from "firebase/messaging";
import { api } from "./api";
import { firebaseApp } from "./firebase";
import type { PublicConfig } from "./types";

const TOKEN_KEY = "pff.pushToken";

export function swURL(cfg: PublicConfig | null): string {
  if (!cfg || cfg.auth_mode !== "firebase") return "/sw.js";
  return `/sw.js?config=${encodeURIComponent(JSON.stringify(cfg.firebase))}`;
}

/** Registers the service worker (PWA shell + push). Safe to call repeatedly. */
export async function registerServiceWorker(cfg: PublicConfig | null): Promise<ServiceWorkerRegistration | null> {
  if (!("serviceWorker" in navigator)) return null;
  try {
    return await navigator.serviceWorker.register(swURL(cfg), { scope: "/" });
  } catch {
    return null;
  }
}

export async function pushSupported(cfg: PublicConfig | null): Promise<boolean> {
  if (!cfg?.push_enabled || !firebaseApp() || !("Notification" in window) || !("serviceWorker" in navigator)) return false;
  return isSupported().catch(() => false);
}

export function pushPermission(): NotificationPermission | "unsupported" {
  return "Notification" in window ? Notification.permission : "unsupported";
}

/** Asks permission, obtains an FCM token and registers it with the backend. */
export async function enablePush(cfg: PublicConfig): Promise<void> {
  const app = firebaseApp();
  if (!app) throw new Error("Push notifications need Firebase sign-in.");
  const permission = await Notification.requestPermission();
  if (permission !== "granted") throw new Error("Notifications are blocked in this browser.");
  const registration = (await registerServiceWorker(cfg)) ?? (await navigator.serviceWorker.ready);
  const token = await getToken(getMessaging(app), { vapidKey: cfg.vapid_key, serviceWorkerRegistration: registration });
  if (!token) throw new Error("Could not obtain a push token.");
  await api.post("/push/devices", { token });
  try {
    localStorage.setItem(TOKEN_KEY, token);
  } catch {
    /* ignore */
  }
}

export async function disablePush(): Promise<void> {
  const app = firebaseApp();
  let token: string | null = null;
  try {
    token = localStorage.getItem(TOKEN_KEY);
  } catch {
    /* ignore */
  }
  if (token) await api.del("/push/devices", { token }).catch(() => undefined);
  if (app) await deleteToken(getMessaging(app)).catch(() => undefined);
  try {
    localStorage.removeItem(TOKEN_KEY);
  } catch {
    /* ignore */
  }
}

export function pushEnabledHere(): boolean {
  try {
    return pushPermission() === "granted" && !!localStorage.getItem(TOKEN_KEY);
  } catch {
    return false;
  }
}

/** Calls cb for push messages received while the app is in the foreground. */
export function onForegroundPush(cb: (title: string, body: string) => void): () => void {
  const app = firebaseApp();
  if (!app) return () => {};
  try {
    return onMessage(getMessaging(app), (p) => cb(p.notification?.title ?? "PFF", p.notification?.body ?? ""));
  } catch {
    return () => {};
  }
}
