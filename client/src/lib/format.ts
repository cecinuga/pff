// Presentation-only formatting of values computed by the backend. Nothing here
// derives new numbers: amounts arrive as decimal strings and are only rendered.

const locale = typeof navigator !== "undefined" ? navigator.language : "en-GB";

const moneyFormatters = new Map<string, Intl.NumberFormat>();

/** Formats a decimal string as money, e.g. "1234.5" + "EUR" -> "€1,234.50". */
export function money(amount: string | null | undefined, currency: string): string {
  if (amount === null || amount === undefined || amount === "") return "—";
  const key = currency || "EUR";
  let f = moneyFormatters.get(key);
  if (!f) {
    try {
      f = new Intl.NumberFormat(locale, { style: "currency", currency: key, minimumFractionDigits: 2, maximumFractionDigits: 2 });
    } catch {
      f = new Intl.NumberFormat(locale, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    }
    moneyFormatters.set(key, f);
  }
  return f.format(Number(amount));
}

/** Compact axis label: 0, 950, 1.2K, 12K, 3.4M. */
export function compact(amount: string | number): string {
  return new Intl.NumberFormat(locale, { notation: "compact", maximumFractionDigits: 1 }).format(Number(amount));
}

/** Renders a backend percentage string ("42.50") as "42.5%". */
export function percent(p: string): string {
  return `${Number(p).toLocaleString(locale, { maximumFractionDigits: 2 })}%`;
}

export function date(iso: string | null | undefined, opts: Intl.DateTimeFormatOptions = { day: "numeric", month: "short", year: "numeric" }): string {
  if (!iso) return "—";
  // Calendar dates (YYYY-MM-DD) are civil dates: render them in UTC so the day
  // never shifts with the viewer's time zone.
  const d = iso.length === 10 ? new Date(iso + "T00:00:00Z") : new Date(iso);
  return new Intl.DateTimeFormat(locale, { ...opts, timeZone: iso.length === 10 ? "UTC" : undefined }).format(d);
}

export function dateTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(new Date(iso));
}

export function maskIban(iban: string): string {
  if (!iban || iban.length < 8) return iban;
  return `${iban.slice(0, 4)} •••• ${iban.slice(-4)}`;
}
