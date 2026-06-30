import { useState } from "react";
import { Frame, SysSample, DiskStat } from "../types";
import { pct, bytesRate } from "../format";
import Sparkline from "./Sparkline";

interface Props {
  frame: Frame;
  samples: SysSample[];
}

const fmtBytes = (b: number) => {
  if (b >= 1024 ** 4) return `${(b / 1024 ** 4).toFixed(2)} TB`;
  if (b >= 1024 ** 3) return `${(b / 1024 ** 3).toFixed(1)} GB`;
  if (b >= 1024 ** 2) return `${(b / 1024 ** 2).toFixed(0)} MB`;
  if (b >= 1024) return `${(b / 1024).toFixed(0)} KB`;
  return `${b} B`;
};
const kib = (k: number) => fmtBytes(k * 1024);

// Donut shows a filesystem's used vs free space as a ring with a center percent.
function Donut({ usedFrac, color, size = 110 }: { usedFrac: number; color: string; size?: number }) {
  const stroke = 14;
  const r = (size - stroke) / 2;
  const c = 2 * Math.PI * r;
  const used = Math.max(0, Math.min(1, usedFrac));
  return (
    <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} style={{ flex: "0 0 auto" }}>
      <g transform={`rotate(-90 ${size / 2} ${size / 2})`}>
        <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke="var(--bg-row-hover)" strokeWidth={stroke} />
        <circle
          cx={size / 2} cy={size / 2} r={r} fill="none" stroke={color} strokeWidth={stroke}
          strokeDasharray={`${(used * c).toFixed(2)} ${c.toFixed(2)}`}
        />
      </g>
      <text x="50%" y="50%" textAnchor="middle" dominantBaseline="central" fill="var(--text)" fontSize="20" fontWeight="700">
        {(used * 100).toFixed(0)}%
      </text>
    </svg>
  );
}

// A mount is "non-system" (shown by default) for the root and user/data areas
// the operator cares about: /, /home, /data, /var*, /tmp. Everything else
// (/boot, /boot/efi, ...) is a system area (hidden by default, shown via the
// checkbox).
function isSystemMount(mp: string): boolean {
  return !(
    mp === "/" ||
    mp.startsWith("/home") ||
    mp.startsWith("/data") ||
    mp.startsWith("/var") ||
    mp.startsWith("/tmp")
  );
}

export default function PerformanceView({ frame, samples }: Props) {
  const [showSystem, setShowSystem] = useState(false);
  const [diskDonut, setDiskDonut] = useState(true); // true = 사용량 도넛, false = I/O 그래프
  const times = samples.map((s) => s.t);

  // ---- CPU ----
  const cpuVals = samples.map((s) => s.cpu);
  const cpuLabels = cpuVals.map((v) => pct(v));

  // ---- Memory + Swap ----
  const memVals = samples.map((s) => s.mem);
  const memLabels = samples.map((s) => kib(s.memUsed));
  const swapVals = samples.map((s) => (s.swapTotal > 0 ? (s.swapUsed / s.swapTotal) * 100 : 0));
  const swapLabels = samples.map((s) => kib(s.swapUsed));
  const memPctNow = frame.memTotal > 0 ? (frame.memUsed / frame.memTotal) * 100 : 0;
  const swapPctNow = frame.swapTotal > 0 ? (frame.swapUsed / frame.swapTotal) * 100 : 0;

  // ---- Network ----
  const netVals = samples.map((s) => s.netRx + s.netTx);
  const netLabels = netVals.map((v) => bytesRate(v));
  const netNow = frame.netRx + frame.netTx;
  const netLoad =
    frame.netSpeed > 0 ? Math.min(100, (netNow * 8) / (frame.netSpeed * 1_000_000) * 100) : -1;

  // ---- Disks (per partition) ----
  const disks = (frame.disks ?? []).filter((d) => showSystem || !isSystemMount(d.mount));
  const hiddenCount = (frame.disks ?? []).filter((d) => isSystemMount(d.mount)).length;
  const diskIO = (d: DiskStat) =>
    samples.map((s) => {
      const m = s.disks?.find((x) => x.mount === d.mount);
      return m ? m.rBps + m.wBps : 0;
    });

  return (
    <div className="perf">
      {/* CPU */}
      <section className="perf-card">
        <div className="perf-head">
          <span className="perf-title">CPU</span>
          <span className="perf-now">{pct(frame.cpu)}</span>
        </div>
        <div className="perf-sub">전체 {frame.ncpu} vCPU · 0–100%는 전 코어 부하 기준</div>
        <Sparkline values={cpuVals} times={times} labels={cpuLabels} max={100} color="#4cc2ff" height={90} />
      </section>

      {/* Memory + Swap */}
      <section className="perf-card">
        <div className="perf-head">
          <span className="perf-title">메모리</span>
          <span className="perf-now">
            {kib(frame.memUsed)} / {kib(frame.memTotal)} ({memPctNow.toFixed(0)}%)
          </span>
        </div>
        <Sparkline values={memVals} times={times} labels={memLabels} max={100} color="#7c4dff" height={70} />
        <div className="perf-sub" style={{ marginTop: 8 }}>
          스왑 {frame.swapTotal > 0 ? `${kib(frame.swapUsed)} / ${kib(frame.swapTotal)} (${swapPctNow.toFixed(0)}%)` : "없음"}
        </div>
        {frame.swapTotal > 0 && (
          <Sparkline values={swapVals} times={times} labels={swapLabels} max={100} color="#d18bff" height={36} />
        )}
      </section>

      {/* Network — total + per interface */}
      <section className="perf-card">
        <div className="perf-head">
          <span className="perf-title">네트워크 (전체)</span>
          <span className="perf-now">
            ↓ {bytesRate(frame.netRx)} · ↑ {bytesRate(frame.netTx)}
            {netLoad >= 0 ? ` · 대역폭 ${netLoad.toFixed(0)}%` : ""}
          </span>
        </div>
        <Sparkline values={netVals} times={times} labels={netLabels} color="#e6b450" height={70} />
        {(frame.nets ?? []).length === 0 ? (
          <div className="perf-sub" style={{ marginTop: 8 }}>물리 NIC를 찾지 못했습니다.</div>
        ) : (
          (frame.nets ?? []).map((ni) => {
            const io = samples.map((s) => {
              const n = s.nets?.find((x) => x.name === ni.name);
              return n ? n.rxBps + n.txBps : 0;
            });
            const ioLabels = io.map((v) => bytesRate(v));
            const niTot = ni.rxBps + ni.txBps;
            const load = ni.speed > 0 ? Math.min(100, (niTot * 8) / (ni.speed * 1_000_000) * 100) : -1;
            return (
              <div key={ni.name} className="perf-disk">
                <div className="perf-head" style={{ marginBottom: 2 }}>
                  <span style={{ fontWeight: 600 }}>
                    {ni.name}
                    <span className="perf-sub" style={{ marginLeft: 6 }}>
                      {ni.speed > 0 ? `${ni.speed} Mbps` : "속도 미상"}
                    </span>
                  </span>
                  <span className="perf-now" style={{ fontSize: 12 }}>
                    ↓ {bytesRate(ni.rxBps)} · ↑ {bytesRate(ni.txBps)}
                    {load >= 0 ? ` · ${load.toFixed(0)}%` : ""}
                  </span>
                </div>
                <Sparkline values={io} times={times} labels={ioLabels} color="#e6b450" height={48} />
              </div>
            );
          })
        )}
      </section>

      {/* Disks per partition */}
      <section className="perf-card">
        <div className="perf-head">
          <span className="perf-title">디스크 (파티션별)</span>
          <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
            <div className="viewtabs" title="사용량 도넛 ↔ I/O 그래프">
              <button className={"toolbtn" + (diskDonut ? " primary" : "")} onClick={() => setDiskDonut(true)}>🍩 도넛</button>
              <button className={"toolbtn" + (!diskDonut ? " primary" : "")} onClick={() => setDiskDonut(false)}>📈 그래프</button>
            </div>
            <label style={{ display: "flex", alignItems: "center", gap: 4, fontSize: 12, color: "var(--text-dim)" }}
              title="/, /home, /data, /var*, /tmp 외의 시스템 영역(/boot 등)도 표시">
              <input type="checkbox" checked={showSystem} onChange={(e) => setShowSystem(e.target.checked)} />
              시스템 영역 표시{hiddenCount > 0 && !showSystem ? ` (${hiddenCount}개 숨김)` : ""}
            </label>
          </div>
        </div>
        {disks.length === 0 ? (
          <div className="perf-sub">표시할 파티션이 없습니다.</div>
        ) : (
          disks.map((d) => {
            const usedPct = d.total > 0 ? (d.used / d.total) * 100 : 0;
            const usageColor = usedPct > 90 ? "var(--bad)" : usedPct > 75 ? "var(--warn)" : "var(--accent)";
            const io = diskIO(d);
            const ioLabels = io.map((v) => bytesRate(v));
            return (
              <div key={d.mount} className="perf-disk">
                <div className="perf-head" style={{ marginBottom: 2 }}>
                  <span style={{ fontWeight: 600 }}>
                    {d.mount}
                    <span className="perf-sub" style={{ marginLeft: 6 }}>
                      {d.fsType}{d.kind ? ` · ${d.kind}` : ""} · {d.dev}{isSystemMount(d.mount) ? " · 시스템" : ""}
                    </span>
                  </span>
                  <span className="perf-now" style={{ fontSize: 12 }}>
                    ↓ {bytesRate(d.rBps)} · ↑ {bytesRate(d.wBps)} · 활성 {d.busy.toFixed(0)}%
                  </span>
                </div>
                {diskDonut ? (
                  <div style={{ display: "flex", alignItems: "center", gap: 18, padding: "4px 0" }}>
                    <Donut usedFrac={d.total > 0 ? d.used / d.total : 0} color={usageColor} />
                    <div style={{ fontSize: 12, lineHeight: 1.9 }}>
                      <div>
                        <span style={{ display: "inline-block", width: 10, height: 10, borderRadius: 2, background: usageColor, marginRight: 6 }} />
                        사용 <b>{fmtBytes(d.used)}</b> ({usedPct.toFixed(0)}%)
                      </div>
                      <div>
                        <span style={{ display: "inline-block", width: 10, height: 10, borderRadius: 2, background: "var(--bg-row-hover)", marginRight: 6 }} />
                        여유 <b>{fmtBytes(d.free)}</b>
                      </div>
                      <div style={{ color: "var(--text-mute)" }}>용량 {fmtBytes(d.total)}</div>
                    </div>
                  </div>
                ) : (
                  <>
                    <div className="usagebar" title={`${fmtBytes(d.used)} / ${fmtBytes(d.total)} 사용`}>
                      <div className="usagefill" style={{ width: `${usedPct}%`, background: usageColor }} />
                      <span className="usagetext">
                        {fmtBytes(d.used)} / {fmtBytes(d.total)} ({usedPct.toFixed(0)}%) · 여유 {fmtBytes(d.free)}
                      </span>
                    </div>
                    <Sparkline values={io} times={times} labels={ioLabels} color="#6ccb5f" height={48} />
                  </>
                )}
              </div>
            );
          })
        )}
      </section>
    </div>
  );
}
