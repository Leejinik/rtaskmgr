import { Fragment, useEffect, useState } from "react";
import { monitor, main } from "../../wailsjs/go/models";
import {
  StartScheduled, ListScheduled, StopScheduled, DeleteScheduled,
  PrepareScheduledSlices, DownloadScheduledSlicesAndPlay, EstimateScheduled,
} from "../../wailsjs/go/main/App";
import { EventsOn } from "../../wailsjs/runtime";

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

// How each recording ended → label, icon and colour. "interrupted" is the
// important one: the sampler died without writing its .done marker (server
// reboot / OOM / SIGKILL), so the capture stops early and is truncated.
const STATUS_UI: Record<string, { label: string; icon: string; color: string }> = {
  running:     { label: "기록 중",       icon: "●", color: "var(--good)" },
  done:        { label: "완료",          icon: "■", color: "var(--text-dim)" },
  stopped:     { label: "사용자 중지",   icon: "⏹", color: "var(--text-dim)" },
  "low-disk":  { label: "디스크 부족 중단", icon: "⚠", color: "var(--warn, #d8b450)" },
  interrupted: { label: "비정상 종료",   icon: "⚠", color: "var(--bad)" },
};
const statusUI = (s: string) =>
  STATUS_UI[s] ?? { label: s || "완료", icon: "■", color: "var(--text-dim)" };

// Format an elapsed span (ms) as "N일 N시간 N분" for the "cut short by" hint.
const fmtSpan = (ms: number) => {
  const s = Math.max(0, Math.round(ms / 1000));
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  return [d && `${d}일`, h && `${h}시간`, (m || (!d && !h)) && `${m}분`].filter(Boolean).join(" ");
};

const WD = ["일", "월", "화", "수", "목", "금", "토"];
const HOUR_MS = 3600000;

// The recording's actual data-end (running → now; else last write or plan).
const recEnd = (r: monitor.RecMeta) =>
  r.status === "running" ? Date.now() : r.lastT > 0 ? r.lastT : r.plannedEndT;

// Next local midnight after ms (DST-safe).
const nextMidnight = (ms: number) => {
  const d = new Date(ms); d.setHours(0, 0, 0, 0); d.setDate(d.getDate() + 1);
  return d.getTime();
};

// Per-recording hourly-slice index state (built once on the host, then cached).
interface SliceState { keys: number[] | null; pct: number; loading: boolean; err?: string; }

interface DayGroup { dayStart: number; label: string; hourKeys: number[]; }

// Group epoch-hour slice keys (floor(t/3600000)) into local calendar days.
const groupByDay = (keys: number[]): DayGroup[] => {
  const map = new Map<number, number[]>();
  for (const k of keys) {
    const d = new Date(k * HOUR_MS); d.setHours(0, 0, 0, 0);
    const ds = d.getTime();
    (map.get(ds) ?? map.set(ds, []).get(ds)!).push(k);
  }
  return [...map.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([ds, hk]) => {
      const d = new Date(ds);
      return {
        dayStart: ds,
        label: `${d.getMonth() + 1}/${d.getDate()}(${WD[d.getDay()]})`,
        hourKeys: hk.sort((a, b) => a - b),
      };
    });
};

export default function ScheduledModal({ hostId, hostName, onClose, onPlay }: Props) {
  const [list, setList] = useState<monitor.RecMeta[]>([]);
  const [days, setDays] = useState(0);
  const [hours, setHours] = useState(0);
  const [minutes, setMinutes] = useState(10);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [info, setInfo] = useState("");

  const [est, setEst] = useState<monitor.RecEstimate | null>(null);
  const [estBusy, setEstBusy] = useState(false);
  const [target, setTarget] = useState("");
  const [recInterval, setRecInterval] = useState(1); // recording cadence (s)
  const [openRecs, setOpenRecs] = useState<Set<string>>(new Set());   // recordings expanded to day list
  const [openHourDays, setOpenHourDays] = useState<Set<string>>(new Set()); // "recId:dayStart" expanded to hours
  const [slices, setSlices] = useState<Record<string, SliceState>>({});     // per-recording hourly index

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

  // Stream split-progress (0..1) for the recording currently being indexed.
  useEffect(() => {
    const off = EventsOn("recSplit", (p: any) => {
      if (!p?.id) return;
      setSlices((s) => (s[p.id] ? { ...s, [p.id]: { ...s[p.id], pct: Number(p.pct) || 0 } } : s));
    });
    return () => off();
  }, []);

  // Build the hourly slice index for a recording on the host (once). Subsequent
  // window loads read only the overlapping slices, so any hour opens in seconds.
  async function prepare(r: monitor.RecMeta) {
    const cur = slices[r.id];
    if (cur?.loading || cur?.keys) return; // already indexing or indexed
    setSlices((s) => ({ ...s, [r.id]: { keys: null, pct: 0, loading: true } }));
    try {
      const keys = await PrepareScheduledSlices(hostId, r.id, r.startT, recEnd(r));
      setSlices((s) => ({ ...s, [r.id]: { keys: keys ?? [], pct: 1, loading: false } }));
    } catch (e: any) {
      setSlices((s) => ({ ...s, [r.id]: { keys: null, pct: 0, loading: false, err: String(e) } }));
    }
  }

  function toggleRec(r: monitor.RecMeta) {
    setOpenRecs((prev) => {
      const next = new Set(prev);
      if (next.has(r.id)) next.delete(r.id);
      else { next.add(r.id); void prepare(r); }
      return next;
    });
  }

  function toggleHourDay(key: string) {
    setOpenHourDays((prev) => {
      const next = new Set(prev);
      next.has(key) ? next.delete(key) : next.add(key);
      return next;
    });
  }

  // Play a [startMs, endMs) window from the prebuilt hourly slices.
  async function playSlice(r: monitor.RecMeta, startMs: number, endMs: number, label: string) {
    setBusy(true); setErr(""); setInfo("");
    try {
      const meta = await DownloadScheduledSlicesAndPlay(hostId, r.id, startMs, endMs, r.intervalSec || 1);
      if (meta.stride && meta.stride > 1) {
        setInfo(`${label} — ${meta.stride}프레임당 1개로 다운샘플 (해상도 축소).`);
      }
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
        {info && (
          <div style={{ color: "var(--warn, #d8b450)", fontSize: 12, margin: "2px 0 6px" }}>{info}</div>
        )}

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
                  const st = statusUI(r.status);
                  const interrupted = r.status === "interrupted";
                  // How much earlier than planned it stopped (only meaningful
                  // when it ended abnormally and we know the last-write time).
                  const cutShort =
                    interrupted && r.lastT > 0 && r.plannedEndT > r.lastT
                      ? r.plannedEndT - r.lastT
                      : 0;
                  const open = openRecs.has(r.id);
                  const sl = slices[r.id];
                  const dayGroups = sl?.keys ? groupByDay(sl.keys) : [];
                  return (
                  <Fragment key={r.id}>
                  <tr>
                    <td className="left">
                      <span style={{ color: st.color, fontWeight: interrupted ? 600 : 400 }}>
                        {st.icon} {st.label}
                      </span>
                    </td>
                    <td className="left" style={{ fontSize: 11, color: "var(--text-dim)" }}>
                      {fmtTime(r.startT)} ~ {fmtTime(r.plannedEndT)}
                      {r.intervalSec ? ` · ${r.intervalSec}초 간격` : ""}
                      {interrupted && (
                        <div style={{ color: "var(--bad)", marginTop: 3 }}>
                          ⚠ 예정보다 일찍 끊김 — 마지막 기록 <b>{fmtTime(r.lastT)}</b>
                          {cutShort > 0 ? ` (약 ${fmtSpan(cutShort)} 조기 종료)` : ""}
                          <div style={{ color: "var(--text-mute)", fontWeight: 400 }}>
                            서버 재부팅·강제종료 등으로 기록이 중단되었습니다. 이 시점까지의 데이터만 재생됩니다.
                          </div>
                        </div>
                      )}
                      {r.status === "low-disk" && (
                        <div style={{ color: "var(--warn, #d8b450)", marginTop: 3 }}>
                          디스크 여유공간 부족으로 자동 중단 — 마지막 기록 <b>{fmtTime(r.lastT)}</b>
                        </div>
                      )}
                    </td>
                    <td>{fmtSize(r.sizeBytes)}</td>
                    <td className="left">
                      <button className="toolbtn" onClick={() => toggleRec(r)} disabled={busy}>
                        📅 시간별 재생 {open ? "▴" : "▾"}
                      </button>{" "}
                      {r.status === "running" && (
                        <button className="toolbtn" onClick={() => stop(r.id)} disabled={busy}>중지</button>
                      )}{" "}
                      <button className="toolbtn" onClick={() => remove(r.id)} disabled={busy}>삭제</button>
                    </td>
                  </tr>

                  {/* --- indexing progress / error --- */}
                  {open && sl?.loading && (
                    <tr style={{ background: "var(--row-sel, rgba(255,255,255,0.03))" }}>
                      <td colSpan={4} style={{ fontSize: 11, color: "var(--text-dim)", padding: "6px 10px" }}>
                        시간별 조각 만드는 중… <b>{Math.round((sl.pct ?? 0) * 100)}%</b>
                        <div style={{ marginTop: 4, height: 5, background: "var(--border)", borderRadius: 3, overflow: "hidden" }}>
                          <div style={{ width: `${Math.round((sl.pct ?? 0) * 100)}%`, height: "100%", background: "var(--good)", transition: "width .2s" }} />
                        </div>
                        <div style={{ color: "var(--text-mute)", marginTop: 3 }}>
                          첫 준비만 전체를 한 번 훑습니다. 이후엔 어느 시간대든 즉시 열립니다.
                        </div>
                      </td>
                    </tr>
                  )}
                  {open && sl?.err && (
                    <tr style={{ background: "var(--row-sel, rgba(255,255,255,0.03))" }}>
                      <td colSpan={3} className="left" style={{ fontSize: 11, color: "var(--bad)", padding: "6px 10px" }}>
                        분할 실패: {sl.err}
                      </td>
                      <td className="left">
                        <button className="toolbtn" onClick={() => prepare(r)} disabled={busy}>다시 시도</button>
                      </td>
                    </tr>
                  )}
                  {open && sl?.keys && dayGroups.length === 0 && (
                    <tr style={{ background: "var(--row-sel, rgba(255,255,255,0.03))" }}>
                      <td colSpan={4} style={{ fontSize: 11, color: "var(--text-mute)", padding: "6px 10px" }}>
                        재생할 데이터가 없습니다.
                      </td>
                    </tr>
                  )}

                  {/* --- day → hour tree --- */}
                  {open && sl?.keys && dayGroups.map((d) => {
                    const dayKey = r.id + ":" + d.dayStart;
                    const hoursOpen = openHourDays.has(dayKey);
                    const dayEnd = nextMidnight(d.dayStart);
                    return (
                    <Fragment key={dayKey}>
                      <tr style={{ background: "var(--row-sel, rgba(255,255,255,0.04))" }}>
                        <td className="left" colSpan={2} style={{ fontSize: 11, paddingLeft: 12 }}>
                          <span
                            onClick={() => toggleHourDay(dayKey)}
                            style={{ cursor: "pointer", userSelect: "none", fontWeight: 600 }}>
                            {hoursOpen ? "▾" : "▸"} {d.label}
                          </span>
                          <span style={{ color: "var(--text-mute)", marginLeft: 6 }}>· {d.hourKeys.length}시간</span>
                        </td>
                        <td />
                        <td className="left">
                          <button className="toolbtn" onClick={() => playSlice(r, d.dayStart, dayEnd, d.label)} disabled={busy}>
                            그 날 전체
                          </button>
                        </td>
                      </tr>
                      {hoursOpen && d.hourKeys.map((k) => {
                        const hs = k * HOUR_MS;
                        const hh = new Date(hs).getHours();
                        const label = `${d.label} ${String(hh).padStart(2, "0")}시`;
                        return (
                          <tr key={dayKey + ":" + k}>
                            <td className="left" style={{ fontSize: 11, color: "var(--text-mute)", paddingLeft: 30 }}>
                              └ {String(hh).padStart(2, "0")}시
                            </td>
                            <td className="left" colSpan={2} style={{ fontSize: 11, color: "var(--text-dim)" }}>
                              {String(hh).padStart(2, "0")}:00 ~ {String((hh + 1) % 24).padStart(2, "0")}:00
                            </td>
                            <td className="left">
                              <button className="toolbtn" onClick={() => playSlice(r, hs, hs + HOUR_MS, label)} disabled={busy}>재생</button>
                            </td>
                          </tr>
                        );
                      })}
                    </Fragment>
                    );
                  })}
                  </Fragment>
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
