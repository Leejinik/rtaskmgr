// 메모리 추세 — 클러스터 뷰의 네 번째 모드.
//
// 질문은 하나다: "GC 가 치우고 난 뒤에도 남는 힙이 계속 늘어나는가?" 그게 누수의 모양이다.
// 데이터는 각 JVM 이 이미 쓰고 있는 GC 로그(-Xloggc)이고, 이 화면은 **읽기만** 한다.
// jcmd/jstat 로 JVM 에 붙지 않는 이유는 공유 테스트 서버에서 Full GC·safepoint 를 일으키면
// 남의 테스트가 깨지기 때문이다.
//
// 모듈이 재기동하면 JVM 이 GC 로그를 처음부터 다시 쓴다. 그래서 가져올 때마다 이 PC 에
// 누적 보관하고(~/.rtaskmgr/gctrend), 화면은 열 때 보관분부터 보여 준다.
import { useEffect, useMemo, useRef, useState } from "react";
import { host } from "../../wailsjs/go/models";
import { GcTrendLoad, GcTrendFetch, GcTrendChart, GcTrendOpenFolder } from "../../wailsjs/go/main/App";
import { fmtClock, fmtUptime } from "../format";

interface Props {
  hosts: host.Host[];
  connected: Record<string, boolean>;
}

const VERDICT: Record<string, { label: string; cls: string; rank: number; tip: string }> = {
  suspect: { label: "누수 의심", cls: "gt-v-bad", rank: 0, tip: "GC 후 바닥선이 꾸준히 올랐습니다" },
  watch: { label: "관찰 필요", cls: "gt-v-warn", rank: 1, tip: "바닥선이 조금씩 오릅니다" },
  hold: { label: "판정 보류", cls: "gt-v-warn", rank: 2, tip: "오르지만 Old 회수가 없어 판정할 수 없습니다" },
  stable: { label: "안정", cls: "gt-v-good", rank: 3, tip: "바닥선이 평평합니다" },
  insufficient: { label: "데이터 부족", cls: "gt-v-dim", rank: 4, tip: "분석할 기간이 짧습니다" },
  nolog: { label: "GC 로그 없음", cls: "gt-v-dim", rank: 5, tip: "-Xloggc 없이 기동된 JVM" },
};

type Row = { hostId: string; hostName: string; fetchedAt: number; m: any };

function fmtMB(v: number): string {
  if (!Number.isFinite(v)) return "—";
  if (Math.abs(v) >= 1024) return `${(v / 1024).toFixed(2)} GB`;
  return `${v.toFixed(0)} MB`;
}
function fmtSlope(v: number): string {
  if (!Number.isFinite(v)) return "—";
  return `${v >= 0 ? "+" : ""}${v.toFixed(Math.abs(v) < 10 ? 1 : 0)} MB/h`;
}
function fmtSpan(h: number): string {
  if (!(h > 0)) return "—";
  return h >= 48 ? `${(h / 24).toFixed(1)}일` : `${h.toFixed(1)}시간`;
}

export default function GcTrendView({ hosts, connected }: Props) {
  const [reports, setReports] = useState<any[]>([]);
  const [fetching, setFetching] = useState(false);
  const [err, setErr] = useState("");
  const [hideNoLog, setHideNoLog] = useState(true);
  const [sel, setSel] = useState<{ hostId: string; key: string } | null>(null);
  const alive = useRef(true);
  const seq = useRef(0);

  const ids = useMemo(() => hosts.map((h) => h.id), [hosts]);
  const liveIds = useMemo(() => ids.filter((id) => connected[id]), [ids, connected]);

  useEffect(() => {
    // StrictMode 는 mount→cleanup→mount 를 한다. 몸통에서 되살리지 않으면 두 번째 mount 뒤
    // 모든 setState 가드가 막힌다(로그 수집 화면에서 실제로 겪은 회귀).
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  useEffect(() => {
    const my = ++seq.current;
    GcTrendLoad(ids).then((r) => {
      if (alive.current && my === seq.current) setReports(r ?? []);
    });
  }, [ids.join(",")]);

  async function fetchNow() {
    if (liveIds.length === 0) {
      setErr("연결된 서버가 없습니다. 먼저 ‘모두 연결’ 하세요.");
      return;
    }
    const my = ++seq.current;
    setFetching(true);
    setErr("");
    try {
      const got = (await GcTrendFetch(liveIds)) ?? [];
      if (!alive.current || my !== seq.current) return;
      // 연결 안 된 서버는 보관분 그대로 둔다.
      setReports((prev) => {
        const byId: Record<string, any> = Object.fromEntries(prev.map((r) => [r.hostId, r]));
        for (const g of got) {
          // 가져오기 실패한 서버는 보관분을 지우지 않고 오류만 얹는다.
          byId[g.hostId] = g.err && byId[g.hostId] ? { ...byId[g.hostId], err: g.err } : g;
        }
        return ids.map((id) => byId[id]).filter(Boolean);
      });
    } catch (e: any) {
      if (alive.current) setErr(String(e?.message ?? e));
    } finally {
      if (alive.current && my === seq.current) setFetching(false);
    }
  }

  const rows: Row[] = useMemo(() => {
    const out: Row[] = [];
    for (const r of reports) {
      for (const m of r.modules ?? []) {
        if (hideNoLog && m.analysis?.verdict === "nolog") continue;
        out.push({ hostId: r.hostId, hostName: r.hostName, fetchedAt: r.fetchedAt, m });
      }
    }
    out.sort((a, b) =>
      (VERDICT[a.m.analysis?.verdict]?.rank ?? 9) - (VERDICT[b.m.analysis?.verdict]?.rank ?? 9) ||
      a.m.name.localeCompare(b.m.name) || a.hostName.localeCompare(b.hostName));
    return out;
  }, [reports, hideNoLog]);

  const hostErrs = reports.filter((r) => r.err);
  const lastFetch = Math.max(0, ...reports.map((r) => r.fetchedAt || 0));
  const selRow = sel ? rows.find((r) => r.hostId === sel.hostId && r.m.key === sel.key) : undefined;
  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const r of rows) c[r.m.analysis?.verdict] = (c[r.m.analysis?.verdict] ?? 0) + 1;
    return c;
  }, [rows]);

  return (
    <div className="lc-wrap">
      <div className="lc-bar">
        <button className="toolbtn primary" disabled={fetching} onClick={fetchNow}
          title="연결된 서버에서 GC 로그의 새 부분만 가져와 보관분에 합치고 다시 판정합니다">
          {fetching ? "가져오는 중…" : "⟳ 가져와서 분석"}
        </button>
        <span className="lc-dim">
          {lastFetch > 0 ? `마지막 가져오기 ${fmtClock(lastFetch)}` : "보관분만 표시 중"}
          {" · "}연결 {liveIds.length}/{ids.length}
        </span>
        <label className="lc-check">
          <input type="checkbox" checked={hideNoLog} onChange={(e) => setHideNoLog(e.target.checked)} />
          GC 로그 없는 JVM 숨김
        </label>
        <span style={{ flex: 1 }} />
        {Object.entries(VERDICT).filter(([k]) => counts[k]).map(([k, v]) => (
          <span key={k} className={"gt-badge " + v.cls}>{v.label} {counts[k]}</span>
        ))}
        <button className="toolbtn" onClick={() => GcTrendOpenFolder()} title="가져온 GC 기록을 보관하는 폴더">
          보관 폴더
        </button>
      </div>

      <div className="lc-dim gt-note">
        읽기 전용 — 각 JVM 이 이미 남기는 GC 로그만 읽습니다(JVM 에 붙지 않고 GC 를 일으키지 않음).
        판정은 <b>GC 직후 힙의 바닥선</b>이 시간에 따라 오르는지를 봅니다. 모듈이 재기동하면 서버의 GC 로그가
        새로 시작되므로, 재기동 전 기록은 그 전에 가져와 둔 것만 남습니다.
      </div>

      {err && <div className="lc-err">{err}</div>}
      {hostErrs.map((r) => (
        <div key={r.hostId} className="lc-warn">{r.hostName}: {r.err}</div>
      ))}

      <div className={"gt-body" + (selRow ? " with-detail" : "")}>
        <div className="gt-table-wrap">
          <table className="gt-table">
            <thead>
              <tr>
                <th>서버</th><th>모듈</th><th>판정</th><th>요약</th>
                <th className="num">기울기</th><th className="num">바닥선</th>
                <th className="num">분석 구간</th><th className="num">기동 후</th>
                <th className="num">RSS</th><th className="num">스레드</th>
              </tr>
            </thead>
            <tbody>
              {rows.length === 0 && (
                <tr><td colSpan={10} className="lc-dim gt-empty">
                  {fetching ? "가져오는 중…" : "보관된 기록이 없습니다. ‘가져와서 분석’을 누르세요."}
                </td></tr>
              )}
              {rows.map((r) => {
                const a = r.m.analysis ?? {};
                const v = VERDICT[a.verdict] ?? VERDICT.insufficient;
                const on = sel?.hostId === r.hostId && sel?.key === r.m.key;
                const judged = a.verdict === "suspect" || a.verdict === "watch" || a.verdict === "stable" || a.verdict === "hold";
                return (
                  <tr key={r.hostId + "/" + r.m.key} className={on ? "sel" : ""}
                    onClick={() => setSel(on ? null : { hostId: r.hostId, key: r.m.key })}>
                    <td>{r.hostName}</td>
                    <td title={r.m.gcLog || ""}>
                      {r.m.name}
                      {!r.m.running && r.fetchedAt > 0 && <span className="lc-dim"> (종료됨)</span>}
                      {r.m.err && <span className="lc-bad" title={r.m.err}> ⚠</span>}
                    </td>
                    <td><span className={"gt-badge " + v.cls} title={v.tip}>{v.label}</span></td>
                    <td className="gt-head" title={a.headline}>{a.headline}</td>
                    <td className="num">{judged ? fmtSlope(a.slopeMBh) : "—"}</td>
                    <td className="num">{judged ? `${fmtMB(a.floorFromMB)} → ${fmtMB(a.floorToMB)}` : "—"}</td>
                    <td className="num">{fmtSpan(a.spanH)}</td>
                    <td className="num">{r.m.upSec > 0 ? fmtUptime(r.m.upSec) : "—"}</td>
                    <td className="num">{r.m.rssMB > 0 ? fmtMB(r.m.rssMB) : "—"}</td>
                    <td className="num">{r.m.threads > 0 ? r.m.threads : "—"}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>

        {selRow && (
          <GcDetail key={selRow.hostId + "/" + selRow.m.key} row={selRow} onClose={() => setSel(null)} />
        )}
      </div>
    </div>
  );
}

function GcDetail({ row, onClose }: { row: Row; onClose: () => void }) {
  const m = row.m;
  const runs: any[] = m.runs ?? [];
  // -1 = 최근 구간. 인덱스로 들고 있으면 가져오기 뒤 재기동 구간이 늘었을 때 옛 구간을 보게 된다.
  const [run, setRun] = useState(-1);
  const [data, setData] = useState<any | null>(null);
  const [err, setErr] = useState("");

  useEffect(() => {
    let live = true;
    setData(null);
    setErr("");
    if (!m.gcLog) return;
    GcTrendChart(row.hostId, m.key, run)
      .then((r) => { if (live) setData(r); })
      .catch((e) => { if (live) setErr(String(e?.message ?? e)); });
    return () => { live = false; };
  }, [row.hostId, m.key, run, m.archived]);

  const a = data?.analysis ?? m.analysis ?? {};
  const p = m.proc ?? {};
  const v = VERDICT[a.verdict] ?? VERDICT.insufficient;

  return (
    <div className="gt-detail">
      <div className="gt-detail-head">
        <b>{row.hostName} · {m.name}</b>
        <span className={"gt-badge " + v.cls}>{v.label}</span>
        {runs.length > 1 && (
          <label className="mini-sel" title="재기동으로 나뉜 기동 구간">
            기동 구간
            <select value={run < 0 ? runs.length - 1 : run}
              onChange={(e) => { const i = Number(e.target.value); setRun(i === runs.length - 1 ? -1 : i); }}>
              {runs.map((r, i) => (
                <option key={i} value={i}>
                  {fmtClock(r.fromMs).slice(5, 16)} ~ {fmtClock(r.toMs).slice(5, 16)}{i === runs.length - 1 ? " (최근)" : ""}
                </option>
              ))}
            </select>
          </label>
        )}
        <span style={{ flex: 1 }} />
        <button className="toolbtn" onClick={onClose}>닫기</button>
      </div>

      <div className="gt-headline">{a.headline}</div>
      {err && <div className="lc-err">{err}</div>}
      {data?.chart && <GcChart c={data.chart} />}

      <div className="gt-facts">
        <Fact k="바닥선 기준" v={a.method === "reclaim" ? "Old 회수(mixed/Full GC) 직후 값" : a.method === "fewreclaims" ? "시작 최저값 + 회수 직후 값(적음)" : a.method === "bucketmin" ? "구간별 GC 후 최소값" : "—"} />
        <Fact k="바닥점 / 꾸준함" v={a.points ? `${a.points}개 / ρ ${Number(a.rho ?? 0).toFixed(2)}` : "—"} />
        <Fact k="최대 힙 / 확보" v={`${a.maxHeapMB ? fmtMB(a.maxHeapMB) : "?"} / ${fmtMB(a.capMB)}`} />
        <Fact k="최대 도달 예상" v={a.hoursToMax > 0 && a.verdict !== "stable" ? `약 ${fmtSpan(a.hoursToMax)} 뒤` : "—"} />
        <Fact k="Old 회수 / Full GC" v={`${a.reclaims ?? 0}회 / ${a.fullGCs ?? 0}회`} />
        <Fact k="최대 정지 / GC 시간" v={`${Math.round(a.maxPauseMs ?? 0)} ms / ${Number(a.gcTimePct ?? 0).toFixed(3)} %`} />
        <Fact k="RSS" v={p.samples > 1
          ? `${fmtMB(p.rssFromMB)} → ${fmtMB(m.rssMB)} (${p.samples}회, ${fmtSpan(p.spanH)})`
          : m.rssMB > 0 ? fmtMB(m.rssMB) : "—"} tip="GC 로그로는 안 보이는 힙 밖(네이티브) 메모리. 가져올 때마다 한 점씩 쌓입니다." />
        <Fact k="스레드" v={p.samples > 1 ? `${p.threadFrom} → ${m.threads}` : m.threads > 0 ? String(m.threads) : "—"} />
        <Fact k="보관 이벤트" v={`${m.archived ?? 0}건${m.added ? ` (이번 +${m.added})` : ""}`} />
        {m.archives > 0 && (
          <Fact k="재기동 압축본" v={`${m.archives}개 (이번에 ${m.archivesRead}개 읽음)`}
            tip="start 스크립트가 재기동 때 남긴 gc-logs-*.tar.gz. 지난 가져오기 이후 새로 생긴 것만 엽니다. 처음엔 최근 30일치만." />
        )}
        <Fact k="GC 로그" v={m.gcLog || "—"} mono />
      </div>

      {(a.notes ?? []).length > 0 && (
        <ul className="gt-notes">
          {(a.notes as string[]).map((n, i) => <li key={i}>{n}</li>)}
        </ul>
      )}
      {m.err && <div className="lc-warn">{m.err}</div>}
    </div>
  );
}

function Fact({ k, v, mono, tip }: { k: string; v: string; mono?: boolean; tip?: string }) {
  return (
    <div className="gt-fact" title={tip}>
      <span className="gt-fact-k">{k}</span>
      <span className={"gt-fact-v" + (mono ? " lc-mono" : "")}>{v}</span>
    </div>
  );
}

// ---- 차트 ----

const W = 900, H = 240, PL = 52, PR = 12, PT = 10, PB = 22;

function GcChart({ c }: { c: any }) {
  const [hover, setHover] = useState<{ x: number; t: number } | null>(null);
  const after: any[] = c.after ?? [];
  const cap: any[] = c.cap ?? [];
  const floor: any[] = c.floor ?? [];
  const rec: any[] = c.reclaims ?? [];
  const t0 = c.run?.fromMs ?? 0, t1 = Math.max(t0 + 1, c.run?.toMs ?? 1);
  const ymaxData = Math.max(1, ...after.map((p) => p.mb), ...cap.map((p) => p.mb));
  const showMax = c.maxHeapMB > 0 && c.maxHeapMB <= ymaxData * 1.6;
  const ymax = (showMax ? Math.max(ymaxData, c.maxHeapMB) : ymaxData) * 1.05;
  const X = (t: number) => PL + ((t - t0) / (t1 - t0)) * (W - PL - PR);
  const Y = (mb: number) => PT + (1 - mb / ymax) * (H - PT - PB);
  const path = (pts: any[]) => pts.map((p, i) => `${i ? "L" : "M"}${X(p.t).toFixed(1)},${Y(p.mb).toFixed(1)}`).join(" ");

  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => f * ymax / 1.05);
  const xt = [0, 0.25, 0.5, 0.75, 1].map((f) => t0 + f * (t1 - t0));
  const spanDays = (t1 - t0) / 864e5;
  const xLabel = (t: number) => {
    const s = fmtClock(t);
    return spanDays >= 2 ? s.slice(5, 10) + " " + s.slice(11, 13) + "시" : s.slice(11, 16);
  };

  // 호버: 가장 가까운 GC 후 점
  let near: any = null;
  if (hover && after.length) {
    near = after.reduce((b, p) => (Math.abs(p.t - hover.t) < Math.abs(b.t - hover.t) ? p : b), after[0]);
  }

  return (
    <div className="gt-chart">
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" width="100%" height={H}
        onMouseMove={(e) => {
          const r = e.currentTarget.getBoundingClientRect();
          const x = ((e.clientX - r.left) / r.width) * W;
          setHover({ x, t: t0 + ((x - PL) / (W - PL - PR)) * (t1 - t0) });
        }}
        onMouseLeave={() => setHover(null)}>
        {ticks.map((v, i) => (
          <g key={i}>
            <line x1={PL} x2={W - PR} y1={Y(v)} y2={Y(v)} className="gt-grid" />
            <text x={PL - 6} y={Y(v) + 4} className="gt-axis" textAnchor="end">{fmtMB(v)}</text>
          </g>
        ))}
        {xt.map((t, i) => (
          <text key={i} x={X(t)} y={H - 6} className="gt-axis"
            textAnchor={i === 0 ? "start" : i === xt.length - 1 ? "end" : "middle"}>{xLabel(t)}</text>
        ))}
        {c.warmupToMs > t0 && (
          <rect x={PL} y={PT} width={Math.max(0, X(Math.min(c.warmupToMs, t1)) - PL)} height={H - PT - PB} className="gt-warm" />
        )}
        {showMax && (
          <g>
            <line x1={PL} x2={W - PR} y1={Y(c.maxHeapMB)} y2={Y(c.maxHeapMB)} className="gt-max" />
            <text x={W - PR - 4} y={Y(c.maxHeapMB) - 4} className="gt-axis" textAnchor="end">최대 힙 {fmtMB(c.maxHeapMB)}</text>
          </g>
        )}
        {cap.length > 1 && <path d={path(cap)} className="gt-cap" />}
        {after.length > 1 && <path d={path(after)} className="gt-after" />}
        {rec.map((p, i) => <circle key={"r" + i} cx={X(p.t)} cy={Y(p.mb)} r={2} className="gt-rec" />)}
        {floor.map((p, i) => <circle key={"f" + i} cx={X(p.t)} cy={Y(p.mb)} r={3} className="gt-floor" />)}
        {c.fitTo?.t > 0 && (
          <line x1={X(c.fitFrom.t)} y1={Y(c.fitFrom.mb)} x2={X(c.fitTo.t)} y2={Y(c.fitTo.mb)} className="gt-fit" />
        )}
        {near && (
          <g>
            <line x1={X(near.t)} x2={X(near.t)} y1={PT} y2={H - PB} className="gt-cursor" />
            <text x={Math.min(X(near.t) + 6, W - 150)} y={PT + 12} className="gt-axis gt-tip">
              {fmtClock(near.t).slice(5)} · {fmtMB(near.mb)}
            </text>
          </g>
        )}
      </svg>
      <div className="gt-legend">
        <span><i className="gt-l-after" />GC 후 힙</span>
        <span><i className="gt-l-cap" />확보 힙</span>
        <span><i className="gt-l-floor" />바닥점(판정 근거)</span>
        <span><i className="gt-l-rec" />Old 회수</span>
        <span><i className="gt-l-fit" />추세선</span>
        {c.warmupToMs > t0 && <span><i className="gt-l-warm" />워밍업(제외)</span>}
        {!showMax && c.maxHeapMB > 0 && <span className="lc-dim">최대 힙 {fmtMB(c.maxHeapMB)} — 그래프 위쪽 밖</span>}
      </div>
    </div>
  );
}
