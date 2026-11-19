import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { onForegroundPush } from "../lib/push";

const NAV = [
  { to: "/", label: "Overview", icon: "M3 12l9-8 9 8M5 10v10h14V10" },
  { to: "/domains/spending", label: "Analytics", icon: "M4 20V10M10 20V4M16 20v-7M22 20H2", match: "/domains" },
  { to: "/budgets", label: "Budgets", icon: "M12 3a9 9 0 109 9h-9z M14 3.5A9 9 0 0120.5 10H14z" },
  { to: "/connections", label: "Banks", icon: "M3 10l9-6 9 6M5 10v8M9 10v8M15 10v8M19 10v8M3 20h18" },
  { to: "/settings", label: "Settings", icon: "M12 15a3 3 0 100-6 3 3 0 000 6zM19.4 15a1.7 1.7 0 00.3 1.8l.1.1a2 2 0 11-2.8 2.8l-.1-.1a1.7 1.7 0 00-1.8-.3 1.7 1.7 0 00-1 1.5V21a2 2 0 11-4 0v-.1a1.7 1.7 0 00-1.1-1.5 1.7 1.7 0 00-1.8.3l-.1.1a2 2 0 11-2.8-2.8l.1-.1a1.7 1.7 0 00.3-1.8 1.7 1.7 0 00-1.5-1H3a2 2 0 110-4h.1a1.7 1.7 0 001.5-1.1 1.7 1.7 0 00-.3-1.8l-.1-.1a2 2 0 112.8-2.8l.1.1a1.7 1.7 0 001.8.3H9a1.7 1.7 0 001-1.5V3a2 2 0 114 0v.1a1.7 1.7 0 001 1.5 1.7 1.7 0 001.8-.3l.1-.1a2 2 0 112.8 2.8l-.1.1a1.7 1.7 0 00-.3 1.8V9a1.7 1.7 0 001.5 1H21a2 2 0 110 4h-.1a1.7 1.7 0 00-1.5 1z" },
];

function Icon({ d }: { d: string }) {
  return (
    <svg viewBox="0 0 24 24" className="h-5 w-5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d={d} />
    </svg>
  );
}

export function Layout() {
  const { me, signOut } = useAuth();
  const location = useLocation();
  const [unread, setUnread] = useState(0);
  const [toast, setToast] = useState<string | null>(null);

  // Unread count refreshes on navigation; foreground pushes show a toast.
  useEffect(() => {
    api
      .get<{ unread: number }>("/notifications", { limit: 1 })
      .then((r) => setUnread(r.unread))
      .catch(() => undefined);
  }, [location.pathname]);

  useEffect(
    () =>
      onForegroundPush((title, body) => {
        setToast(`${title} — ${body}`);
        setUnread((n) => n + 1);
        window.setTimeout(() => setToast(null), 6000);
      }),
    [],
  );

  const isActive = (to: string, match?: string) => (match ? location.pathname.startsWith(match) : location.pathname === to);

  return (
    <div className="min-h-screen md:flex">
      <aside className="hidden w-56 shrink-0 border-r border-line bg-raised md:flex md:flex-col">
        <div className="px-5 py-5 text-lg font-semibold tracking-tight">
          PFF<span className="text-accent">.</span>
        </div>
        <nav className="flex-1 space-y-0.5 px-3">
          {NAV.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              className={`flex items-center gap-3 rounded-lg px-3 py-2 text-sm ${
                isActive(n.to, n.match) ? "bg-sunken font-medium text-ink" : "text-ink-2 hover:bg-sunken"
              }`}
            >
              <Icon d={n.icon} />
              {n.label}
            </NavLink>
          ))}
        </nav>
        <div className="border-t border-line p-4 text-xs text-ink-2">
          <p className="truncate" title={me?.user.email}>
            {me?.user.email}
          </p>
          <button className="mt-2 text-accent hover:underline" onClick={() => void signOut()}>
            Sign out
          </button>
        </div>
      </aside>

      <div className="min-w-0 flex-1">
        <header className="sticky top-0 z-20 flex items-center justify-between border-b border-line bg-surface/90 px-4 py-3 backdrop-blur md:justify-end md:px-8">
          <span className="text-lg font-semibold md:hidden">
            PFF<span className="text-accent">.</span>
          </span>
          <NavLink to="/notifications" className="relative rounded-lg p-2 text-ink-2 hover:bg-sunken" aria-label={`Notifications, ${unread} unread`}>
            <Icon d="M18 8a6 6 0 10-12 0c0 7-3 9-3 9h18s-3-2-3-9M13.7 21a2 2 0 01-3.4 0" />
            {unread > 0 && (
              <span className="absolute -right-0.5 -top-0.5 min-w-[18px] rounded-full bg-critical px-1 text-center text-[10px] font-semibold leading-[18px] text-white">
                {unread > 99 ? "99+" : unread}
              </span>
            )}
          </NavLink>
        </header>
        <main className="mx-auto max-w-6xl px-4 pb-24 pt-5 md:px-8 md:pb-10">
          <Outlet />
        </main>
      </div>

      {/* Mobile / PWA bottom navigation */}
      <nav className="fixed inset-x-0 bottom-0 z-30 grid grid-cols-5 border-t border-line bg-raised pb-[env(safe-area-inset-bottom)] md:hidden">
        {NAV.map((n) => (
          <NavLink
            key={n.to}
            to={n.to}
            className={`flex flex-col items-center gap-0.5 py-2 text-[11px] ${isActive(n.to, n.match) ? "text-accent" : "text-ink-2"}`}
          >
            <Icon d={n.icon} />
            {n.label}
          </NavLink>
        ))}
      </nav>

      {toast && (
        <div role="status" className="card fixed bottom-20 left-1/2 z-50 w-[min(92vw,420px)] -translate-x-1/2 p-3 text-sm shadow-lg md:bottom-6">
          {toast}
        </div>
      )}
    </div>
  );
}
