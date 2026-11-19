import { Link } from "react-router-dom";
import { api } from "../lib/api";
import { useResource } from "../lib/hooks";
import type { NotificationItem } from "../lib/types";
import { dateTime } from "../lib/format";
import { Empty, ErrorBox, PageHeader, Spinner } from "../components/ui";

export function NotificationsPage() {
  const res = useResource((s) => api.get<{ notifications: NotificationItem[]; unread: number }>("/notifications", { limit: 100 }, s), []);

  const markAll = async () => {
    await api.post("/notifications/read-all").catch(() => undefined);
    res.reload();
  };
  const markOne = (id: string) => void api.post(`/notifications/${id}/read`).catch(() => undefined);

  return (
    <div className="space-y-5">
      <PageHeader
        title="Notifications"
        actions={
          res.data && res.data.unread > 0 ? (
            <button className="btn-ghost" onClick={() => void markAll()}>
              Mark all as read
            </button>
          ) : undefined
        }
      />
      {res.error && <ErrorBox message={res.error} onRetry={res.reload} />}
      {res.loading && !res.data && <Spinner />}
      {res.data && res.data.notifications.length === 0 && <Empty title="No notifications">Budget alerts and bank reminders will appear here.</Empty>}
      <ul className="space-y-2">
        {res.data?.notifications.map((n) => (
          <li key={n.id} className={`card p-4 ${n.read_at ? "" : "border-accent/50"}`}>
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <p className="font-medium">
                  {!n.read_at && <span className="mr-2 inline-block h-2 w-2 rounded-full bg-accent align-middle" aria-label="unread" />}
                  {n.title}
                </p>
                <p className="mt-0.5 text-sm text-ink-2">{n.body}</p>
                <p className="mt-1 text-xs text-ink-3">{dateTime(n.created_at)}</p>
              </div>
              {n.link && (
                <Link to={n.link} className="shrink-0 text-sm text-accent hover:underline" onClick={() => markOne(n.id)}>
                  Open
                </Link>
              )}
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}
