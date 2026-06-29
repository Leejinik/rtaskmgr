import { useEffect, useState } from "react";
import { monitor, main } from "../../wailsjs/go/models";
import {
  StartScheduled, ListScheduled, StopScheduled, DeleteScheduled, DownloadScheduledAndPlay,
} from "../../wailsjs/go/main/App";

interface Props {
  hostId: string;
  hostName: string;
  onClose: () => void;
  onPlay: (meta: main.LogMeta) => void;
}

const MAX_DAYS = 7;

const fmtSize = (b: number) => {
  if (b < 1024) return `${b} B`;
  const kb = b / 1024;
  if (kb < 1024) return `${kb.toFixed(0)} KB`;
  const mb = kb / 1024;
  if (mb < 1024) return `${mb.toFixed(1)} MB`;
  return `${(mb / 1024).toFixed(2)} GB`;
};
const fmtTime = (t: number) =>
  t > 0 ? new Date(t).toLocaleString("ko-KR", { hour12: false }) : "—";

export default function ScheduledModal({ hostId, hostName, onClose, onPlay }: Props) {
  const [list, setList] = useState<monitor.RecMeta[]>([]);
  const [days, setDays] = useState(0);
  const [hours, setHours] = useState(0);
  const [minutes, setMinutes] = useState(10);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const refresh = async () => {
    try {
      setList((await ListScheduled(hostId)) ?? []);
    } catch (e: any) {
      setErr(String(e));
    }
  };

  useEffect(() => { void refresh(); }, [hostId]);

  const durationSec = days * 86400 + hours * 3600 + minutes * 60;
  const overCap = durationSec > MAX_DAYS * 86400;

  async function start() {
    if (durationSec <= 0) { setErr("기록 시간을 지정하세요"); return; }
    if (overCap) { setErr(`최대 ${MAX_DAYS}일까지만 가능합니다`); return; }
    setBusy(true); setErr("");
    try {
      await StartScheduled(hostId, durationSec);
      await refresh();
    } catch (e: any) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  }

  async function play(id: string) {
    setBusy(true); setErr("");
    try {
      const meta = await DownloadScheduledAndPlay(hostId, id);
      onPlay(meta);
    } catch (e: any) {
      setErr(String(e));
      setBusy(false);
    }
  }

  async function stop(id: string) {
    setBusy(true);
    try { await StopScheduled(hostId, id); await refresh(); }
    catch (e: any) { setErr(String(e)); }
    finally { setBusy(false); }
  }

  async function remove(id: string) {
    if (!window.confirm("이 예약 기록 파일을 서버에서 삭제할까요?")) return;
    setBusy(true);
    try { await DeleteScheduled(hostId, id); await refresh(); }
    catch (e: any) { setErr(String(e)); }
    finally { setBusy(false); }
  }

  return (
    <div className="scrim" onMouseDown={onClose}>
      <div className="modal" onMouseDown={(e) => e.stopPropagation()} style={{ minWidth: 620, maxWidth: 820 }}>
        <h2>⏱ 예약 기록 — {hostName}</h2>
        <div style={{ color: "var(--text-mute)", fontSize: 12, marginBottom: 12 }}>
          서버에서 detached로 동작합니다. 이 앱을 종료해도 계속 기록하며, 설정한 시간이
          되면 자동 종료됩니다. gzip 압축 + 디스크 여유공간 가드 적용, 최대 {MAX_DAYS}일.
        </div>

        <div className="row2" style={{ alignItems: "flex-end" }}>
          <div>
            <label>일 (0–{MAX_DAYS})</label>
            <input type="number" min={0} max={MAX_DAYS} value={days}
              onChange={(e) => setDays(Math.max(0, Math.min(MAX_DAYS, Number(e.target.value))))} />
          </div>
          <div>
            <label>시간 (0–23)</label>
            <input type="number" min={0} max={23} value={hours}
              onChange={(e) => setHours(Math.max(0, Math.min(23, Number(e.target.value))))} />
          </div>
          <div>
            <label>분 (0–59)</label>
            <input type="number" min={0} max={59} value={minutes}
              onChange={(e) => setMinutes(Math.max(0, Math.min(59, Number(e.target.value))))} />
          </div>
          <div style={{ flex: 2, color: overCap ? "var(--bad)" : "var(--text-mute)", fontSize: 12, paddingBottom: 8 }}>
            총 {days}일 {hours}시간 {minutes}분{overCap ? ` · 7일 초과!` : ""}
          </div>
          <button className="toolbtn primary" onClick={start} disabled={busy || durationSec <= 0 || overCap}>
            {busy ? "처리 중…" : "예약 기록 시작"}
          </button>
        </div>

        <div className="err">{err}</div>

        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", margin: "6px 0" }}>
          <b style={{ fontSize: 13 }}>기록 목록</b>
          <button className="toolbtn" onClick={refresh} disabled={busy}>새로고침</button>
        </div>

        <div style={{ maxHeight: 320, overflowY: "auto" }}>
          {list.length === 0 ? (
            <div style={{ color: "var(--text-mute)", padding: 14, textAlign: "center" }}>
              예약 기록이 없습니다.
            </div>
          ) : (
            <table className="proc" style={{ tableLayout: "auto" }}>
              <thead>
                <tr>
                  <th className="left"><span className="lbl">상태</span></th>
                  <th className="left"><span className="lbl">시작 ~ 예정 종료</span></th>
                  <th><span className="lbl">크기</span></th>
                  <th className="left"><span className="lbl">동작</span></th>
                </tr>
              </thead>
              <tbody>
                {list.map((r) => (
                  <tr key={r.id}>
                    <td className="left">
                      <span style={{ color: r.status === "running" ? "var(--good)" : "var(--text-dim)" }}>
                        {r.status === "running" ? "● 기록 중" : "■ 완료"}
                      </span>
                    </td>
                    <td className="left" style={{ fontSize: 11, color: "var(--text-dim)" }}>
                      {fmtTime(r.startT)} ~ {fmtTime(r.plannedEndT)}
                    </td>
                    <td>{fmtSize(r.sizeBytes)}</td>
                    <td className="left">
                      <button className="toolbtn" onClick={() => play(r.id)} disabled={busy}>재생</button>{" "}
                      {r.status === "running" && (
                        <button className="toolbtn" onClick={() => stop(r.id)} disabled={busy}>중지</button>
                      )}{" "}
                      <button className="toolbtn" onClick={() => remove(r.id)} disabled={busy}>삭제</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        <div className="actions">
          <button className="toolbtn" onClick={onClose}>닫기</button>
        </div>
      </div>
    </div>
  );
}
