import { useEffect, useRef, useState } from "react";
import type { Series } from "../lib/types";
import { compact, date, money } from "../lib/format";

const HEIGHT = 200;
const PAD = { top: 12, right: 8, bottom: 26, left: 44 };
const MAX_BAR = 24;

function useWidth<T extends HTMLElement>(): [React.RefObject<T>, number] {
  const ref = useRef<T>(null);
  const [w, setW] = useState(320); // narrow start: the real width is measured on mount
  useEffect(() => {
    if (!ref.current) return;
    const ro = new ResizeObserver(([e]) => e && setW(Math.max(240, Math.floor(e.contentRect.width))));
    ro.observe(ref.current);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

/** Rounds the axis maximum up to 1, 2 or 5 × 10^n so ticks are clean numbers. */
function niceMax(v: number): number {
  if (v <= 0) return 1;
  const exp = Math.pow(10, Math.floor(Math.log10(v)));
  const f = v / exp;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * exp;
}

function bucketLabel(start: string, bucket: Series["bucket"]): string {
  if (bucket === "month") return date(start, { month: "short", year: "2-digit" });
  return date(start, { day: "numeric", month: "short" });
}

/** Columns with a 4px rounded data end and a square baseline. */
function columnPath(x: number, y: number, w: number, h: number): string {
  const r = Math.min(4, w / 2, h);
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`;
}

/** Totals of one domain per time bucket (single series: no legend, the title names it). */
export function SeriesChart({ series, title }: { series: Series; title: string }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const pts = series.points;
  const values = pts.map((p) => Number(p.total));
  const top = niceMax(Math.max(0, ...values));
  const plotW = width - PAD.left - PAD.right;
  const plotH = HEIGHT - PAD.top - PAD.bottom;
  const band = pts.length > 0 ? plotW / pts.length : plotW;
  const barW = Math.max(2, Math.min(MAX_BAR, band * 0.7));
  const y = (v: number) => PAD.top + plotH - (v / top) * plotH;
  const ticks = [0, top / 2, top];
  const labelEvery = Math.max(1, Math.ceil(pts.length / Math.max(1, Math.floor(plotW / 64))));
  const hovered = hover !== null ? pts[hover] : undefined;

  return (
    <figure className="relative" aria-label={title}>
      <div ref={ref} className="w-full overflow-hidden">
        <svg width={width} height={HEIGHT} role="img" aria-label={`${title}, ${pts.length} ${series.bucket}s`}>
          {ticks.map((t) => (
            <g key={t}>
              <line x1={PAD.left} x2={width - PAD.right} y1={y(t)} y2={y(t)} stroke="rgb(var(--line))" strokeWidth={1} />
              <text x={PAD.left - 6} y={y(t)} dy="0.32em" textAnchor="end" className="fill-ink-3 text-[10px]">
                {compact(t)}
              </text>
            </g>
          ))}
          {pts.map((p, i) => {
            const v = values[i] ?? 0;
            const x = PAD.left + i * band + (band - barW) / 2;
            const h = Math.max(0, PAD.top + plotH - y(v));
            return (
              <g key={p.start}>
                {h > 0 && (
                  <path
                    d={columnPath(x, y(v), barW, h)}
                    fill="rgb(var(--accent))"
                    opacity={hover === null || hover === i ? 1 : 0.45}
                  />
                )}
                {/* Hit target spans the whole band, larger than the mark. */}
                <rect
                  x={PAD.left + i * band}
                  y={PAD.top}
                  width={band}
                  height={plotH}
                  fill="transparent"
                  onMouseEnter={() => setHover(i)}
                  onMouseLeave={() => setHover(null)}
                  onTouchStart={() => setHover(i)}
                />
                {i % labelEvery === 0 && (
                  <text x={PAD.left + i * band + band / 2} y={HEIGHT - 8} textAnchor="middle" className="fill-ink-3 text-[10px]">
                    {bucketLabel(p.start, series.bucket)}
                  </text>
                )}
              </g>
            );
          })}
          <line x1={PAD.left} x2={width - PAD.right} y1={PAD.top + plotH} y2={PAD.top + plotH} stroke="rgb(var(--ink-3))" strokeWidth={1} />
        </svg>
      </div>
      {hovered && hover !== null && (
        <div
          className="pointer-events-none absolute top-0 z-10 -translate-x-1/2 rounded-lg border border-line bg-raised px-2.5 py-1.5 text-xs shadow-sm"
          style={{ left: Math.min(Math.max(PAD.left + hover * band + band / 2, 70), width - 70) }}
        >
          <div className="text-ink-2">
            {series.bucket === "day" ? date(hovered.start) : `${series.bucket === "week" ? "Week of" : ""} ${bucketLabel(hovered.start, series.bucket)}`}
          </div>
          <div className="num font-semibold">{money(hovered.total, series.currency)}</div>
          <div className="text-ink-3">
            {hovered.count} transaction{hovered.count === 1 ? "" : "s"}
          </div>
        </div>
      )}
      <details className="mt-1 text-xs text-ink-2">
        <summary className="cursor-pointer select-none">Show as table</summary>
        <table className="mt-2 w-full text-left">
          <thead>
            <tr className="text-ink-3">
              <th className="py-1 font-normal">{series.bucket === "day" ? "Day" : series.bucket === "week" ? "Week of" : "Month"}</th>
              <th className="py-1 text-right font-normal">Total</th>
              <th className="py-1 text-right font-normal">Count</th>
            </tr>
          </thead>
          <tbody className="num">
            {pts.map((p) => (
              <tr key={p.start} className="border-t border-line">
                <td className="py-1">{bucketLabel(p.start, series.bucket)}</td>
                <td className="py-1 text-right">{money(p.total, series.currency)}</td>
                <td className="py-1 text-right">{p.count}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </figure>
  );
}
