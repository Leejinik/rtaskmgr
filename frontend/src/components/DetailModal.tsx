import { useEffect, useState } from "react";
import { record } from "../../wailsjs/go/models";
import { ProcessHistory, LogProcSeries } from "../../wailsjs/go/main/App";
import { Proc } from "../types";
import { pct, mib, bytesRate } from "../format";
import Sparkline from "./Sparkline";

interface Props {
  hostId: string;
  pid: number;
  current?: Proc; // latest row for the header line
  logMode?: boolean; // playback: build the timeline once from the opened log (server-side)
  onClose: () => void;
}

// DetailModal shows a process's 1-second CPU/Memory/Disk timeline. Live mode
// refreshes from the in-memory ring each second; playback mode fetches the whole
// per-process series from the opened log server-side (the renderer never holds
// every frame's process list).
export default function DetailModal({ hostId, pid, current, logMode, onClose }: Props) {
  const [hist, setHist] = useState<record.Point[]>([]);

  useEffect(() => {
    if (logMode) {
      let live = true;
      LogProcSeries(hostId, pid)
        .then((h) => { if (live) setHist(h ?? []); })
        .catch(() => {});
      return () => { live = false; };
    }
    let live = true;
    const pull = async () => {
      try {
        const h = await ProcessHistory(hostId, pid);
        if (live) setHist(h ?? []);
      } catch {
        /* host gone */
      }
    };
    pull();
    const t = setInterval(pull, 1000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [hostId, pid, logMode]);

  const times = hist.map((p) => p.t);
  const cpu = hist.map((p) => p.cpu);
  const mem = hist.map((p) => p.memPct);
  const disk = hist.map((p) => Math.max(0, p.diskR < 0 ? 0 : p.diskR + p.diskW));
  // Tooltip labels mirror each chart's corner units (CPU %, memory MB, disk B/s).
  const cpuLabels = hist.map((p) => pct(p.cpu));
  const memLabels = hist.map((p) => mib(p.rssKiB));
  const diskLabels = hist.map((p) => bytesRate(p.diskR < 0 ? -1 : p.diskR + p.diskW));
  const last = hist[hist.length - 1];

  return (
    <div className="scrim" onMouseDown={onClose}>
      <div className="modal detail" onMouseDown={(e) => e.stopPropagation()} style={{ minWidth: 680 }}>
        <h2>
          {current?.name ?? "process"}
          <span className="pid">PID {pid}</span>
          {current?.service && current.service !== "-" && (
            <span className="pid">· {current.service}</span>
          )}
        </h2>
        <div style={{ color: "var(--text-mute)", fontSize: 12, marginBottom: 14 }}>
          사용자 {current?.user ?? "-"} · 스레드 {current?.threads ?? "-"} · 상태 {current?.state ?? "-"}
          {" · "}최근 {hist.length}초 기록 (1초 간격)
        </div>

        <div className="charts">
          <div className="chart">
            <div className="ctitle">
              <span>CPU</span>
              <span className="cnow">{last ? pct(last.cpu) : "—"}</span>
            </div>
            <Sparkline values={cpu} times={times} labels={cpuLabels} color="#4cc2ff" />
          </div>
          <div className="chart">
            <div className="ctitle">
              <span>메모리</span>
              <span className="cnow">{last ? mib(last.rssKiB) : "—"}</span>
            </div>
            <Sparkline values={mem} times={times} labels={memLabels} color="#7c4dff" />
          </div>
          <div className="chart">
            <div className="ctitle">
              <span>디스크 I/O</span>
              <span className="cnow">
                {last ? bytesRate(last.diskR < 0 ? -1 : last.diskR + last.diskW) : "—"}
              </span>
            </div>
            <Sparkline values={disk} times={times} labels={diskLabels} color="#6ccb5f" />
          </div>
          <div className="chart">
            <div className="ctitle">
              <span>네트워크</span>
              <span className="cnow">—</span>
            </div>
            <Sparkline values={[]} color="#e6b450" />
            <div style={{ color: "var(--text-mute)", fontSize: 11, marginTop: 4 }}>
              프로세스별 네트워크는 nethogs 연동 후 표시됩니다.
            </div>
          </div>
        </div>

        <div className="actions">
          <button className="toolbtn primary" onClick={onClose}>닫기</button>
        </div>
      </div>
    </div>
  );
}
