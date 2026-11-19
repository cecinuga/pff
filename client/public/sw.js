/* PFF service worker: offline app shell + Firebase Cloud Messaging.
 *
 * - Navigations are network-first with a cached index.html fallback, so the
 *   installed PWA opens offline (data views then show their error state).
 * - API responses are NEVER cached: they hold personal financial data.
 * - Push: the Firebase web config arrives in this script's URL (?config=...),
 *   set by the app when it registers the worker.
 */
const SHELL_CACHE = "pff-shell-v1";
const SHELL = ["/", "/manifest.webmanifest", "/icons/icon.svg", "/icons/icon-192.png"];

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(SHELL_CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== SHELL_CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin || url.pathname.startsWith("/api/")) return;

  if (req.mode === "navigate") {
    event.respondWith(
      fetch(req)
        .then((res) => {
          const copy = res.clone();
          caches.open(SHELL_CACHE).then((c) => c.put("/", copy));
          return res;
        })
        .catch(() => caches.match("/")),
    );
    return;
  }
  // Hashed build assets are immutable: cache-first.
  if (url.pathname.startsWith("/assets/") || url.pathname.startsWith("/icons/")) {
    event.respondWith(
      caches.match(req).then(
        (hit) =>
          hit ||
          fetch(req).then((res) => {
            if (res.ok) {
              const copy = res.clone();
              caches.open(SHELL_CACHE).then((c) => c.put(req, copy));
            }
            return res;
          }),
      ),
    );
  }
});

// ---- Firebase Cloud Messaging ------------------------------------------------
try {
  const raw = new URL(self.location.href).searchParams.get("config");
  if (raw) {
    importScripts(
      "https://www.gstatic.com/firebasejs/11.0.2/firebase-app-compat.js",
      "https://www.gstatic.com/firebasejs/11.0.2/firebase-messaging-compat.js",
    );
    firebase.initializeApp(JSON.parse(raw));
    // Messages with a "notification" payload are displayed by the SDK itself.
    firebase.messaging();
  }
} catch (e) {
  // Push stays unavailable; the app shell keeps working.
  console.warn("PFF: push disabled in service worker", e);
}

// Open the in-app link carried by the notification (works in dev over http,
// where FCM refuses click-through links).
self.addEventListener("notificationclick", (event) => {
  const data = event.notification.data || {};
  const link = (data.FCM_MSG && data.FCM_MSG.data && data.FCM_MSG.data.link) || data.link || "/notifications";
  event.notification.close();
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((wins) => {
      for (const w of wins) {
        if ("focus" in w) {
          w.navigate(link);
          return w.focus();
        }
      }
      return self.clients.openWindow(link);
    }),
  );
});
