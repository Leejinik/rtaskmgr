// Display formatters matching the Windows Task Manager column conventions.

export function pct(v: number): string {
  if (v < 0) return "—";
  if (v > 0 && v < 0.1) return "0%";
  return `${v.toFixed(v < 10 ? 1 : 0)}%`;
}

// bytesRate renders a bytes/second value as Task Manager does the disk column
// ("0.1 MB/s"). A negative value means "no permission / not available".
export function bytesRate(bps: number): string {
  if (bps < 0) return "—";
  const mb = bps / (1024 * 1024);
  if (mb >= 0.05) return `${mb.toFixed(1)} MB/s`;
  const kb = bps / 1024;
  if (kb >= 0.5) return `${kb.toFixed(0)} KB/s`;
  return "0 MB/s";
}

// netRate renders the network column ("0.1 Mbps"). -1 means N/A.
export function netRate(bps: number): string {
  if (bps < 0) return "—";
  const mbps = (bps * 8) / (1000 * 1000);
  if (mbps >= 0.05) return `${mbps.toFixed(1)} Mbps`;
  return "0 Mbps";
}

export function mib(kib: number): string {
  const m = kib / 1024;
  if (m >= 1024) return `${(m / 1024).toFixed(1)} GB`;
  return `${m.toFixed(1)} MB`;
}

// diskBps is the combined read+write rate for sorting/aggregation; -1 if denied.
export function diskBps(diskR: number, diskW: number): number {
  if (diskR < 0) return -1;
  return diskR + diskW;
}

// fmtClock renders a unix-millis timestamp as "YYYY-MM-DD HH:MM:SS" (local time).
export function fmtClock(ms: number): string {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return (
    `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
    `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  );
}

// fmtUptime turns a duration in seconds into a compact human string
// (e.g. "45초", "12분 3초", "3시간 8분", "2일 5시간"). Shows the two largest
// non-zero units so a just-started process reads clearly as seconds.
export function fmtUptime(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) return "—";
  const s = Math.floor(sec);
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = s % 60;
  if (d > 0) return `${d}일 ${h}시간`;
  if (h > 0) return `${h}시간 ${m}분`;
  if (m > 0) return `${m}분 ${ss}초`;
  return `${ss}초`;
}

// heat returns a 0..1 intensity for the cell background bar.
export function heat(v: number, max: number): number {
  if (v <= 0 || max <= 0) return 0;
  return Math.min(1, v / max);
}
