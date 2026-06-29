import { main } from "../../wailsjs/go/models";

interface Props {
  meta: main.LogMeta;
  hostId: string;
  index: number;
  total: number;
  playing: boolean;
  currentT: number;
  onHost: (id: string) => void;
  onIndex: (i: number) => void;
  onPlayToggle: () => void;
  onStep: (delta: number) => void;
  onClose: () => void;
}

const fmtTime = (t: number) =>
  t > 0 ? new Date(t).toLocaleTimeString("ko-KR", { hour12: false }) : "—";

const fileName = (p: string) => p.split(/[\\/]/).pop() || p;

export default function PlaybackBar({
  meta, hostId, index, total, playing, currentT,
  onHost, onIndex, onPlayToggle, onStep, onClose,
}: Props) {
  return (
    <div className="pbbar">
      <span className="pbtag">▶ 로그 재생</span>
      <span className="pbfile" title={meta.path}>{fileName(meta.path)}</span>

      {meta.hosts.length > 1 && (
        <select value={hostId} onChange={(e) => onHost(e.target.value)} className="pbsel">
          {meta.hosts.map((h) => (
            <option key={h.id} value={h.id}>{h.name} ({h.frames}f)</option>
          ))}
        </select>
      )}

      <button className="toolbtn" onClick={() => onStep(-1)} title="이전 프레임">⏮</button>
      <button className="toolbtn primary" onClick={onPlayToggle}>
        {playing ? "⏸ 일시정지" : "▶ 재생"}
      </button>
      <button className="toolbtn" onClick={() => onStep(1)} title="다음 프레임">⏭</button>

      <input
        type="range"
        className="pbslider"
        min={0}
        max={Math.max(0, total - 1)}
        value={index}
        onChange={(e) => onIndex(Number(e.target.value))}
      />
      <span className="pbtime">
        {fmtTime(currentT)} · {index + 1}/{total}
      </span>

      <button className="toolbtn" onClick={onClose}>✕ 라이브로</button>
    </div>
  );
}
