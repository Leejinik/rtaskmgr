import { useEffect, useState } from "react";
import { monitor, main } from "../../wailsjs/go/models";
import {
  StartScheduled, ListScheduled, StopScheduled, DeleteScheduled,
  DownloadScheduledAndPlay, EstimateScheduled,
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

const REC_STATUS: Record<string, { label: string; color: string }> = {
  running: { label: "● 기록 중", color: "var(--good)" },
  deadline: { label: "■ 완료", color: "var(--text-dim)" },
  "low-disk": { label: "⚠ 디스크 부족으로 중단", color: "var(--bad)" },
  signal: { label: "■ 중지됨", color: "var(--warn, #d8b450)" },
  unknown: { label: "? 종료 사유 확인 불가", color: "var(--warn, #d8b450)" },
};

const recStatus = (status: string) => REC_STATUS[status] ?? REC_STATUS.unknown;

export default function ScheduledModal({ hostId, hostName, onClose, onPlay }: Props) {
  const [list, setList] = useState<monitor.RecMeta[]>([]);
  const [days, setDays] = useState(0);
  const [hours, setHours] = useState(0);
  const [minutes, setMinutes] = useState(10);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const [est, setEst] = useState<monitor.RecEstimate | null>(null);
  const [estBusy, setEstBusy] = useState(false);
  const [target, setTarget] = useState("");
  const [recInterval, setRecInterval] = useState(1); // recording cadence (s)

  const refresh = async () => {
    try {
      setList((await ListScheduled(hostId)) ?? []);
    } catch (e: any) {
      setErr(String(e));
    }
  };

  // Probe disk usage + free space. Runs a short sampler on the host (~6s).
  const measure = async () => {
    setEstBusy(true); setErr("");
    try {
      const e = await EstimateScheduled(hostId);
      setEst(e);
      const ts = e.targets ?? [];
      const def = ts.find((t) => t.writable) ?? ts.find((t) => t.needsSudo) ?? ts[0];
      setTarget(def ? def.path : "");
    } catch (e: any) {
      setErr(String(e));
    } finally {
      setEstBusy(false);
    }
  };

  useEffect(() => { void refresh(); void measure(); }, [hostId]);

  const durationSec = days * 86400 + hours * 3600 + minutes * 60;
  const overCap = durationSec > MAX_DAYS * 86400;

  const sel = est?.targets?.find((t) => t.path === target);
  const usable = (t: monitor.RecTarget) => t.writable || t.needsSudo;
  // Per-frame gzip size measured by the 1s probe; scale by the chosen recording
  // interval (fewer frames/day = proportionally less disk).
  const perFrame = est && est.frames > 0 ? est.probeBytes / est.frames : 0;
  const hourBytes = perFrame * (3600 / recInterval);
  const dayBytes = perFrame * (86400 / recInterval);
  const projected = durationSec > 0 ? perFrame * (durationSec / recInterval) : 0;
  const freePctUsed = sel && sel.freeBytes > 0 ? (projected / sel.freeBytes) * 100 : 0;
  const wouldFill = !!sel && projected > 0 && projected > sel.freeBytes * 0.95;

  const canStart =
    !busy && !estBusy && durationSec > 0 && !overCap &&
    !!sel && usable(sel) && !wouldFill;

  async function start() {
    if (durationSec <= 0) { setErr("기록 시간을 지정하세요"); return; }
    if (overCap) { setErr(`최대 ${MAX_DAYS}일까지만 가능합니다`); return; }
    if (!sel || !usable(sel)) { setErr("기록 위치를 선택하세요"); return; }
    if (wouldFill) { setErr("선택한 파티션 여유공간을 초과할 수 있습니다"); return; }
    setBusy(true); setErr("");
    try {
      await StartScheduled(hostId, durationSec, recInterval, sel.path);
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

        {/* ---- disk usage estimate + target partition ---- */}
        <div style={{ border: "1px solid var(--border)", borderRadius: 6, padding: "10px 12px", marginBottom: 12 }}>
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 8 }}>
            <b style={{ fontSize: 13 }}>기록 위치 & 예상 용량</b>
            <button className="toolbtn" onClick={measure} disabled={estBusy || busy}>
              {estBusy ? "측정 중… (약 6초)" : "다시 측정"}
            </button>
          </div>

          {estBusy && !est ? (
            <div style={{ color: "var(--text-mute)", fontSize: 12, padding: "6px 0" }}>
              호스트에서 6초간 샘플링해 하루 예상 용량을 측정 중입니다…
            </div>
          ) : est ? (
            <>
              <div style={{ display: "flex", alignItems: "center", gap: 8, fontSize: 12, marginBottom: 8, flexWrap: "wrap" }}>
                <label style={{ display: "flex", alignItems: "center", gap: 6 }}>
                  기록 주기
                  <select value={recInterval} onChange={(e) => setRecInterval(Number(e.target.value))}>
                    {[1, 2, 5, 10, 20, 30, 60].map((s) => (
                      <option key={s} value={s}>{s}초</option>
                    ))}
                  </select>
                </label>
                <span style={{ color: "var(--text-dim)" }}>
                  기준 약 <b style={{ color: "var(--text)" }}>{fmtSize(hourBytes)}/시간</b>
                  {" · "}<b style={{ color: "var(--text)" }}>{fmtSize(dayBytes)}/일</b>
                </span>
                <span style={{ color: "var(--text-mute)" }}>
                  (probe {est.frames}프레임 / {fmtSize(est.probeBytes)}, 근사치)
                </span>
              </div>

              {(est.targets ?? []).length === 0 ? (
                <div style={{ color: "var(--bad)", fontSize: 12 }}>
                  기록 가능한 파티션을 찾지 못했습니다.
                </div>
              ) : (
                <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
                  {est.targets.map((t) => {
                    const ok = usable(t);
                    return (
                      <label key={t.path}
                        style={{
                          display: "flex", alignItems: "center", gap: 8, fontSize: 12,
                          padding: "4px 6px", borderRadius: 4,
                          opacity: ok ? 1 : 0.5, cursor: ok ? "pointer" : "not-allowed",
                          background: target === t.path ? "var(--row-sel, rgba(255,255,255,0.06))" : "transparent",
                        }}>
                        <input type="radio" name="rectarget" disabled={!ok}
                          checked={target === t.path}
                          onChange={() => setTarget(t.path)} />
                        <span style={{ fontFamily: "monospace", minWidth: 150 }}>{t.path}</span>
                        <span style={{ color: "var(--text-mute)" }}>({t.mount})</span>
                        <span style={{ marginLeft: "auto" }}>
                          여유 <b style={{ color: "var(--good)" }}>{fmtSize(t.freeBytes)}</b>
                          {" / "}{fmtSize(t.totalBytes)}
                        </span>
                        <span style={{
                          fontSize: 11, padding: "1px 6px", borderRadius: 3,
                          background: t.writable ? "var(--good-bg, rgba(80,200,120,0.18))"
                            : t.needsSudo ? "var(--warn-bg, rgba(220,180,80,0.18))"
                            : "var(--bad-bg, rgba(220,90,90,0.18))",
                          color: t.writable ? "var(--good)" : t.needsSudo ? "var(--warn, #d8b450)" : "var(--bad)",
                        }}>
                          {t.writable ? "쓰기가능" : t.needsSudo ? "sudo로 생성" : "권한 없음"}
                        </span>
                      </label>
                    );
                  })}
                </div>
              )}

              {sel && durationSec > 0 && (
                <div style={{ fontSize: 12, marginTop: 8, color: wouldFill ? "var(--bad)" : "var(--text-dim)" }}>
                  이 설정으로 약 <b style={{ color: wouldFill ? "var(--bad)" : "var(--text)" }}>{fmtSize(projected)}</b> 기록 예상
                  {" — "}선택 파티션 여유의 <b>{freePctUsed.toFixed(freePctUsed < 1 ? 2 : 0)}%</b>
                  {wouldFill ? " · 여유공간 초과 위험!" : ""}
                </div>
              )}
            </>
          ) : (
            <div style={{ color: "var(--text-mute)", fontSize: 12 }}>측정 정보를 불러오지 못했습니다.</div>
          )}
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
          <button className="toolbtn primary" onClick={start} disabled={!canStart}>
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
                {list.map((r) => {
                  const state = recStatus(r.status);
                  return (
                    <tr key={r.id}>
                      <td className="left">
                        <span style={{ color: state.color }}>
                          {state.label}
                        </span>
                      </td>
                      <td className="left" style={{ fontSize: 11, color: "var(--text-dim)" }}>
                        {fmtTime(r.startT)} ~ {fmtTime(r.plannedEndT)}
                        {r.intervalSec ? ` · ${r.intervalSec}초 간격` : ""}
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
                  );
                })}
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
