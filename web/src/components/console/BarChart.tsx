import { useEffect, useRef, useState } from "react";

export type Bar = { label: string; value: number; title: string };

/** Rounds a max up to 1, 2 or 5 × 10^n so axis ticks read cleanly. */
function niceMax(v: number) {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  const n = v / p;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * p;
}

/**
 * Small hand-written SVG bar chart (no chart library, see docs/UI.md).
 * Accessible: role="img" with a summary label, and a <title> on every bar.
 */
export function BarChart({
  bars,
  format,
  label,
  height = 200,
  tone = "fill-signal",
}: {
  bars: Bar[];
  /** Formats axis ticks; receives the tick value and the axis maximum (to keep decimals consistent). */
  format: (v: number, max: number) => string;
  label: string;
  height?: number;
  /** Fill class of non-zero bars (a token, such as fill-danger for errors). */
  tone?: "fill-signal" | "fill-danger" | "fill-warning";
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(640);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.max(240, e.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const max = niceMax(Math.max(0, ...bars.map((b) => b.value)));
  const left = 56;
  const bottom = 22;
  const top = 8;
  const plotW = width - left - 4;
  const plotH = height - top - bottom;
  const step = plotW / Math.max(1, bars.length);
  const barW = Math.max(2, Math.min(28, step * 0.7));
  const every = Math.ceil(bars.length / Math.max(2, Math.floor(plotW / 64)));
  const ticks = [0, max / 2, max];

  return (
    <div ref={ref} className="w-full">
      <svg role="img" aria-label={label} width={width} height={height} className="block text-muted-foreground">
        {ticks.map((t) => {
          const y = top + plotH - (t / max) * plotH;
          return (
            <g key={t}>
              <line x1={left} x2={width - 4} y1={y} y2={y} className="stroke-border" strokeDasharray={t === 0 ? undefined : "3 3"} />
              <text x={left - 8} y={y} dy="0.32em" textAnchor="end" className="fill-muted-foreground text-[11px] tabular-nums">
                {format(t, max)}
              </text>
            </g>
          );
        })}
        {bars.map((b, i) => {
          const h = (b.value / max) * plotH;
          const x = left + i * step + (step - barW) / 2;
          return (
            <g key={b.label + i}>
              <rect
                x={x}
                y={top + plotH - h}
                width={barW}
                height={Math.max(0, h)}
                rx={2}
                className={b.value > 0 ? tone : "fill-nodata"}
              >
                <title>{b.title}</title>
              </rect>
              {/* a transparent column makes small bars hoverable */}
              <rect x={left + i * step} y={top} width={step} height={plotH} className="fill-transparent">
                <title>{b.title}</title>
              </rect>
              {i % every === 0 && (
                <text x={left + i * step + step / 2} y={height - 6} textAnchor="middle" className="fill-muted-foreground text-[11px]">
                  {b.label}
                </text>
              )}
            </g>
          );
        })}
      </svg>
    </div>
  );
}
