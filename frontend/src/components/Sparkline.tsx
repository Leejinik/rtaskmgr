// Tiny dependency-free SVG area chart for the process detail timeline.

interface Props {
  values: number[];
  width?: number;
  height?: number;
  color?: string;
  max?: number; // fixed scale; defaults to data max
}

export default function Sparkline({
  values,
  width = 300,
  height = 80,
  color = "#4cc2ff",
  max,
}: Props) {
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

  return (
    <svg width={width} height={height} preserveAspectRatio="none">
      <defs>
        <linearGradient id={`g-${color}`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity="0.35" />
          <stop offset="100%" stopColor={color} stopOpacity="0.02" />
        </linearGradient>
      </defs>
      <path d={area} fill={`url(#g-${color})`} />
      <path d={line} fill="none" stroke={color} strokeWidth="1.5" />
    </svg>
  );
}
