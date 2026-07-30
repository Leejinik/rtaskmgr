import { monitor } from "../../wailsjs/go/models";

interface Props {
  targets: monitor.RecTarget[];
  value: string;
  onChange: (path: string) => void;
  // "rec" keeps the scheduled-recording behaviour byte for byte. "pcap" adds what
  // a packet capture has to warn about: the filesystem type and the root
  // partition, because tcpdump writes orders of magnitude faster than the sampler.
  mode?: "rec" | "pcap";
  name?: string;
  disabled?: boolean;
}

const fmtSize = (b: number) => {
  if (b < 1024) return `${b} B`;
  const kb = b / 1024;
  if (kb < 1024) return `${kb.toFixed(0)} KB`;
  const mb = kb / 1024;
  if (mb < 1024) return `${mb.toFixed(1)} MB`;
  return `${(mb / 1024).toFixed(2)} GB`;
};

export const targetUsable = (t: monitor.RecTarget) => t.writable || t.needsSudo;

// Chooses the default target the way each feature wants it: a recording is happy
// with the first writable path, a capture wants the roomiest non-root one.
export function defaultTarget(
  targets: monitor.RecTarget[],
  mode: "rec" | "pcap" = "rec"
): monitor.RecTarget | undefined {
  const usable = targets.filter(targetUsable);
  if (usable.length === 0) return targets[0];
  if (mode === "rec") return usable[0];
  const ranked = [...usable].sort((a, b) => {
    const aRoot = a.mount === "/" ? 1 : 0;
    const bRoot = b.mount === "/" ? 1 : 0;
    if (aRoot !== bRoot) return aRoot - bRoot;
    return b.freeBytes - a.freeBytes;
  });
  return ranked[0];
}

// TargetPicker is the storage-location list shared by scheduled recording and
// packet capture.
export default function TargetPicker({ targets, value, onChange, mode = "rec", name, disabled }: Props) {
  if (targets.length === 0) {
    return (
      <div className="pcap-bad" style={{ fontSize: 12 }}>
        {mode === "pcap"
          ? "캡쳐를 저장할 수 있는 디스크 기반 위치를 찾지 못했습니다 (tmpfs/ramfs는 제외됩니다)."
          : "기록 가능한 파티션을 찾지 못했습니다."}
      </div>
    );
  }
  const group = name ?? (mode === "pcap" ? "pcaptarget" : "rectarget");
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      {targets.map((t) => {
        const ok = targetUsable(t) && !disabled;
        const isRoot = mode === "pcap" && t.mount === "/";
        return (
          <label
            key={t.path}
            style={{
              display: "flex", alignItems: "center", gap: 8, fontSize: 12,
              padding: "4px 6px", borderRadius: 4,
              opacity: ok ? 1 : 0.5, cursor: ok ? "pointer" : "not-allowed",
              background: value === t.path ? "var(--row-sel, rgba(255,255,255,0.06))" : "transparent",
            }}
          >
            <input type="radio" name={group} disabled={!ok}
              checked={value === t.path} onChange={() => onChange(t.path)} />
            <span className="pcap-mono" style={{ minWidth: 150 }}>{t.path}</span>
            <span style={{ color: "var(--text-mute)" }}>
              ({t.mount}{mode === "pcap" && t.fsType ? ` · ${t.fsType}` : ""})
            </span>
            {isRoot && (
              <span className="pcap-badge pcap-warn" title="루트 파티션이 차면 서버 전체가 영향을 받습니다">
                ⚠ 루트 파티션
              </span>
            )}
            <span style={{ marginLeft: "auto" }}>
              여유 <b className="pcap-good">{fmtSize(t.freeBytes)}</b>
              {" / "}{fmtSize(t.totalBytes)}
            </span>
            <span
              className="pcap-badge"
              style={{
                background: t.writable ? "var(--good-bg, rgba(80,200,120,0.18))"
                  : t.needsSudo ? "var(--warn-bg, rgba(220,180,80,0.18))"
                  : "var(--bad-bg, rgba(220,90,90,0.18))",
                color: t.writable ? "var(--good)" : t.needsSudo ? "var(--warn)" : "var(--bad)",
              }}
            >
              {t.writable ? "쓰기가능" : t.needsSudo ? "sudo로 생성" : "권한 없음"}
            </span>
          </label>
        );
      })}
    </div>
  );
}
