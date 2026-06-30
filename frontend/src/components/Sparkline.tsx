// Tiny dependency-free SVG area chart for the process detail timeline.
import { useState } from "react";
import { fmtClock } from "../format";

interface Props {
  values: number[];
  times?: number[]; // unix millis aligned to values, for the hover tooltip
  labels?: string[]; // preformatted value strings aligned to values (tooltip)
  width?: number;
  height?: number;
  color?: string;
  max?: number; // fixed scale; defaults to data max
}

export default function Sparkline({
  values,
  times,
  labels,
  width = 300,
  height = 80,
  color = "#4cc2ff",
  max,
}: Props) {
  const [hover, setHover] = useState<number | null>(null);

  if (values.length === 0) {
    return (
      <svg width={width} height={height}>
        <rect width={width} height={height} fill="transparent" />
      </svg>
    );
  }
  const top = max ?? Math.max(1, ...values);
  const n = values.length;
  const dx = n > 1 ? width / (n - 1) : width;

  const pts = values.map((v, i) => {
    const x = i * dx;
    const y = height - Math.min(1, Math.max(0, v / top)) * (height - 2) - 1;
    return [x, y] as const;
  });

  const line = pts.map(([x, y], i) => `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`).join(" ");
  const area = `${line} L${width},${height} L0,${height} Z`;

  // Map the cursor (in rendered pixels) to the nearest sample index. The SVG is
  // stretched to its container (preserveAspectRatio none), so scale by the real
  // rendered width rather than the viewBox width.
  const onMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    if (rect.width <= 0) return;
    const dataX = ((e.clientX - rect.left) / rect.width) * width;
    setHover(Math.min(n - 1, Math.max(0, Math.round(dataX / dx))));
  };

  const hx = hover != null ? pts[hover][0] : 0;
  const leftPct = (hx / width) * 100;
  const topPct = hover != null ? (pts[hover][1] / height) * 100 : 0;

  return (
    <div style={{ position: "relative", width: "100%", lineHeight: 0 }}>
      <svg
        viewBox={`0 0 ${width} ${height}`}
        width={width}
        height={height}
        preserveAspectRatio="none"
        style={{ width: "100%", display: "block" }}
        onMouseMove={onMove}
        onMouseLeave={() => setHover(null)}
      >
        <defs>
          <linearGradient id={`g-${color}`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={color} stopOpacity="0.35" />
            <stop offset="100%" stopColor={color} stopOpacity="0.02" />
          </linearGradient>
        </defs>
        <path d={area} fill={`url(#g-${color})`} />
        <path d={line} fill="none" stroke={color} strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
        {hover != null && (
          <line
            x1={hx} y1={0} x2={hx} y2={height}
            stroke="var(--text-mute)" strokeWidth="1" strokeDasharray="3 3"
            vectorEffect="non-scaling-stroke"
          />
        )}
      </svg>
      {hover != null && (
        <div
          style={{
            position: "absolute",
            left: `${leftPct}%`,
            top: `${topPct}%`,
            width: 7,
            height: 7,
            borderRadius: "50%",
            background: color,
            transform: "translate(-50%,-50%)",
            pointerEvents: "none",
            zIndex: 4,
          }}
        />
      )}
      {hover != null && (
        <div
          style={{
            position: "absolute",
            top: 2,
            left: `${leftPct}%`,
            transform: `translateX(${leftPct > 60 ? "-100%" : leftPct < 40 ? "0" : "-50%"})`,
            pointerEvents: "none",
            background: "var(--bg-panel)",
            border: "1px solid var(--border)",
            borderRadius: 4,
            padding: "3px 6px",
            fontSize: 11,
            lineHeight: 1.4,
            color: "var(--text)",
            whiteSpace: "nowrap",
            zIndex: 5,
            boxShadow: "0 4px 12px rgba(0,0,0,0.35)",
          }}
        >
          {times && times[hover] != null && (
            <div style={{ color: "var(--text-mute)" }}>{fmtClock(times[hover])}</div>
          )}
          <div style={{ color, fontWeight: 600 }}>
            {labels && labels[hover] != null ? labels[hover] : values[hover]}
          </div>
        </div>
      )}
    </div>
  );
}
