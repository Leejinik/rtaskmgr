import { useEffect, useState } from "react";
import { record } from "../../wailsjs/go/models";
import { ProcessHistory } from "../../wailsjs/go/main/App";
import { Proc, Frame } from "../types";
import { pct, mib, bytesRate } from "../format";
import Sparkline from "./Sparkline";

interface Props {
  hostId: string;
  pid: number;
  current?: Proc; // latest row for the header line
  frames?: Frame[]; // when set, build the timeline from this log (playback) instead of live
  onClose: () => void;
}

// DetailModal shows a process's 1-second CPU/Memory/Disk timeline. Live mode
// refreshes from the in-memory ring each second; playback mode builds it once
// from the opened log's frames.
export default function DetailModal({ hostId, pid, current, frames, onClose }: Props) {
  const [hist, setHist] = useState<record.Point[]>([]);

  useEffect(() => {
    if (frames) {
      const pts = frames
        .map((f) => {
          const p = f.procs.find((x) => x.pid === pid);
          return p
            ? record.Point.createFrom({
                t: f.t, cpu: p.cpu, memPct: p.memPct,
                rssKiB: p.rssKiB, diskR: p.diskR, diskW: p.diskW,
              })
            : null;
        })
        .filter((x): x is record.Point => x !== null);
      setHist(pts);
      return;
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
  }, [hostId, pid, frames]);

  const cpu = hist.map((p) => p.cpu);
  const mem = hist.map((p) => p.memPct);
  const disk = hist.map((p) => Math.max(0, p.diskR < 0 ? 0 : p.diskR + p.diskW));
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
            <Sparkline values={cpu} color="#4cc2ff" />
          </div>
          <div className="chart">
            <div className="ctitle">
              <span>메모리</span>
              <span className="cnow">{last ? mib(last.rssKiB) : "—"}</span>
            </div>
            <Sparkline values={mem} color="#7c4dff" />
          </div>
          <div className="chart">
            <div className="ctitle">
              <span>디스크 I/O</span>
              <span className="cnow">
                {last ? bytesRate(last.diskR < 0 ? -1 : last.diskR + last.diskW) : "—"}
              </span>
            </div>
            <Sparkline values={disk} color="#6ccb5f" />
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
