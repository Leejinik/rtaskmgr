// 로그 수집 — 클러스터 뷰의 세 번째 모드 (OQT-323 두 번째).
//
// 단일 호스트 화면에는 없다. hostname 으로 나눠 담는 것이 의미를 갖는 이유가 여러
// 서버에서 한 번에 모으기 때문이고, 한 대만 필요하면 그 클러스터에서 한 대만 체크하면
// 된다.
//
// 이 화면의 핵심은 **cp 하기 전에 무엇이 얼마나 있는지 먼저 보여주는 것**이다. 이
// 기능의 가장 큰 위험은 진단 중인 서버의 디스크를 사본으로 잠식하는 것이고, 그걸 막는
// 실질적 장치는 상한값이 아니라 트리의 용량 표시 + 서버별 여유 계산이다. clickhouse 나
// kafka 로그가 수십 GB 로 나오는 게 정상이므로, 답은 "상한에 걸려 실패"가 아니라
// "트리에서 꺼서 줄인다" 여야 한다.
//
// 그래서 이 화면이 보여주는 숫자는 서버가 시작 전에 계산하는 숫자와 같아야 한다.
// 어긋나면 "화면은 된다는데 서버가 거부"하거나 그 반대가 되고, 둘 다 운영자가 원인을
// 알 수 없는 실패다.
import { useEffect, useMemo, useRef, useState } from "react";
import { host } from "../../wailsjs/go/models";
import { monitor } from "../../wailsjs/go/models";
import {
  LogCollectSurvey, LogCollectLeftovers, DeleteLogCollectLeftover,
} from "../../wailsjs/go/main/App";

export interface LogCollectProgress {
  stage: string;
  done: number;
  total: number;
  pct: number;
  err: string;
}

export interface LogCollectTaskReq {
  hostId: string;
  picks: monitor.LogPick[];
  target: string;
  serverDir: string;
  recentDays: number;
  fromMs: number;
  toMs: number;
  journalUnits: string[];
}

interface Props {
  clusterId: string;
  hosts: host.Host[];
  connected: Record<string, boolean>;
  // The run is owned by App so it survives leaving this view — gigabytes take
  // minutes and the operator should be able to go look at a process list.
  running: boolean;
  progress: Record<string, LogCollectProgress>;
  result: any | null;
  onStart: (tasks: LogCollectTaskReq[]) => void;
  onCancel: () => void;
  onOpenFolder: () => void;
  onBundle: () => void;
  onClearResult: () => void;
  onRedownload: (hostId: string, path: string) => Promise<void>;
}

const CAT_LABEL: Record<string, string> = {
  modules: "liz 모듈",
  middleware: "미들웨어",
  system: "시스템",
  journal: "journald",
};
const CAT_ORDER = ["modules", "middleware", "system", "journal"];

// journald 기본 유닛. 전체로 두면 수 GB 가 나오기 쉬워 좁혀서 시작한다.
//
// 글롭(liz*.service)은 쓸 수 없다 — 유닛 이름은 systemctl 명령에 들어가므로 백엔드가
// 알파벳·숫자·@._:- 만 허용한다. 대신 그 호스트의 조사 결과에서 실제로 배포된 liz
// 모듈 이름을 가져와 유닛 이름을 만든다. 소스에 없는 모듈이 실서버에 배포돼 있고
// (lizadmin·lizapi 등) 목록을 박아두면 그것들이 빠지기 때문이다. 존재하지 않는 유닛을
// 넘기는 것은 무해하다(journalctl 이 그냥 아무 것도 내놓지 않는다).
const MIDDLEWARE_UNITS = [
  "kafka.service", "zookeeper.service", "mariadb.service", "mariadb-server.service",
  "redis.service", "redis-sentinel.service", "clickhouse-server.service", "keepalived.service",
];
const UNIT_OK = /^[A-Za-z0-9@._:-]+$/;

function unitsFor(s: monitor.LogSurvey): string[] {
  const out = new Set(MIDDLEWARE_UNITS);
  for (const m of s.modules ?? []) {
    if (m.category !== "modules") continue;
    const u = `${m.module}.service`;
    if (UNIT_OK.test(u)) out.add(u);
  }
  return [...out];
}

const fmtBytes = (b: number) => {
  if (!b || b < 0) return "—";
  if (b < 1024) return `${b} B`;
  const kb = b / 1024;
  if (kb < 1024) return `${kb.toFixed(0)} KB`;
  const mb = kb / 1024;
  if (mb < 1024) return `${mb.toFixed(1)} MB`;
  const gb = mb / 1024;
  if (gb < 1024) return `${gb.toFixed(2)} GB`;
  return `${(gb / 1024).toFixed(2)} TB`;
};

// 이 세 상수는 Go 와 같은 값이어야 한다(logcollect_run.go 의 logMinFreeBytes/
// logCopyOverhead, logcollect.go 의 textCompressRatio). 화면과 서버가 다른 숫자로
// 판단하면 한쪽이 반드시 거짓말을 한다.
const TEXT_RATIO = 0.35;
const COPY_OVERHEAD = 1.05;
const MIN_FREE = 2 * 1024 * 1024 * 1024;

// 이미 압축된 바이트는 다시 줄지 않는다. Go 의 estimateArchiveBytes 와 같은 계산.
const estOf = (bytes: number, compressed: number) =>
  Math.round(compressed + Math.max(0, bytes - compressed) * TEXT_RATIO);

// 선택 키에는 디렉터리가 들어간다. 같은 분류·모듈 이름이 서로 다른 디렉터리를 가리킬
// 수 있고(카탈로그가 liz*/logs 와 liz*/log 를 모두 돈다), 이름만으로 묶으면 한쪽을
// 체크했을 때 다른 쪽도 체크된 것처럼 보이면서 실제로는 수집되지 않는다.
const key = (hostId: string, m: monitor.LogModuleStat) =>
  `${hostId}|${m.category}/${m.module}|${m.dir}`;

const stageLabel = (p?: LogCollectProgress) => {
  if (!p) return "";
  switch (p.stage) {
    case "gate": return "점검 중…";
    case "copy": return p.total > 0 ? `복사 ${p.done}/${p.total}` : "복사 중…";
    case "archive": return "압축 중…";
    case "download": return `다운로드 ${Math.round(p.pct)}%`;
    case "verify": return "검증 중…";
    case "cleanup": return "서버 정리 중…";
    case "done": return "완료";
    default: return p.stage;
  }
};

// 자정 기준. 기간 선택은 날짜 단위이므로 시각은 00:00 으로 맞춘다.
const midnight = (d: Date) =>
  new Date(d.getFullYear(), d.getMonth(), d.getDate(), 0, 0, 0, 0).getTime();

export default function LogCollectView({
  clusterId, hosts, connected, running, progress, result,
  onStart, onCancel, onOpenFolder, onBundle, onClearResult, onRedownload,
}: Props) {
  const [surveys, setSurveys] = useState<monitor.LogSurvey[]>([]);
  const [surveying, setSurveying] = useState(false);
  const [surveyErr, setSurveyErr] = useState("");
  const [rangeMode, setRangeMode] = useState<"all" | "recent" | "span">("recent");
  const [recentDays, setRecentDays] = useState(3);
  const [fromDate, setFromDate] = useState("");
  const [toDate, setToDate] = useState("");
  const [allUnits, setAllUnits] = useState(false);
  const [targets, setTargets] = useState<Record<string, string>>({});
  const [leftovers, setLeftovers] = useState<monitor.LogLeftover[]>([]);
  const [busyLeft, setBusyLeft] = useState("");

  // 선택과 접힘 상태는 클러스터별로 보존한다. 40개 모듈을 다시 체크하게 만들면
  // 아무도 이 화면을 두 번 쓰지 않는다. (클러스터가 바뀌면 이 컴포넌트는 key 로
  // 재마운트되므로 여기서 clusterId 변화를 따로 다룰 필요가 없다.)
  const selKey = `rtm.lc.sel.${clusterId}`;
  const openKey = `rtm.lc.open.${clusterId}`;
  const [sel, setSel] = useState<Set<string>>(() => {
    try { return new Set(JSON.parse(localStorage.getItem(selKey) || "[]")); } catch { return new Set(); }
  });
  const [collapsed, setCollapsed] = useState<Set<string>>(() => {
    try { return new Set(JSON.parse(localStorage.getItem(openKey) || "[]")); } catch { return new Set(); }
  });
  useEffect(() => { localStorage.setItem(selKey, JSON.stringify([...sel])); }, [sel, selKey]);
  useEffect(() => { localStorage.setItem(openKey, JSON.stringify([...collapsed])); }, [collapsed, openKey]);

  const liveIds = useMemo(() => hosts.filter((h) => connected[h.id]).map((h) => h.id), [hosts, connected]);
  const liveKey = liveIds.join(",");

  const rangeSig = `${rangeMode}|${recentDays}|${fromDate}|${toDate}`;
  const [surveyedRange, setSurveyedRange] = useState("");
  const staleRange = surveys.length > 0 && surveyedRange !== "" && surveyedRange !== rangeSig;

  // 조사 시작 시각을 절대 순간으로 계산한다. 백엔드는 이 값을 find -newermt 에 그대로
  // 쓰므로, ‘기간 지정’이 여기서 빠지면 트리는 전 기간 용량을 보여주고 그 숫자로 여유
  // 계산이 돌아 하루짜리 수집이 화면에서만 막힌다.
  function surveyFromMs(): number {
    if (rangeMode === "recent") {
      const d = new Date();
      d.setDate(d.getDate() - recentDays);
      return midnight(d);
    }
    if (rangeMode === "span" && fromDate) return new Date(fromDate + "T00:00:00").getTime();
    return 0;
  }

  // 조사 요청 토큰. ‘모두 연결’을 누르면 호스트가 하나씩 streaming 이 되면서 liveKey 가
  // 여러 번 바뀌고, 그때마다 뜬 조사들이 도착 순서대로 서로를 덮어쓴다 — 먼저 시작한
  // (호스트가 적은) 응답이 나중에 도착하면 방금 연결된 서버가 트리에서 사라진다.
  const seq = useRef(0);
  const alive = useRef(true);
  // The flag must be RAISED on mount, not only lowered on unmount. StrictMode runs
  // every effect mount → cleanup → mount again in development, so an effect that
  // only returns a cleanup lowers the flag and never restores it — every guarded
  // setState after that is skipped and the view sits on "조사 중" forever while the
  // survey has in fact already come back.
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  async function runSurvey() {
    if (liveIds.length === 0) {
      setSurveys([]);
      setSurveyErr("연결된 서버가 없습니다. 먼저 ‘모두 연결’ 하세요.");
      return;
    }
    const my = ++seq.current;
    const sig = rangeSig;
    setSurveying(true);
    setSurveyErr("");
    try {
      const res = await LogCollectSurvey(liveIds, surveyFromMs());
      if (!alive.current || my !== seq.current) return; // 더 새로운 조사가 이미 떴다
      setSurveys(res ?? []);
      setSurveyedRange(sig);
      // 스테이징 위치 기본값 = 가장 여유 있는 비-루트 파티션.
      setTargets((prev) => {
        const next = { ...prev };
        for (const s of res ?? []) {
          if (next[s.hostId]) continue;
          const usable = (s.targets ?? []).filter((t) => t.writable || t.needsSudo);
          const ranked = [...usable].sort((a, b) => {
            const ar = a.mount === "/" ? 1 : 0, br = b.mount === "/" ? 1 : 0;
            if (ar !== br) return ar - br;
            return b.freeBytes - a.freeBytes;
          });
          if (ranked[0]) next[s.hostId] = ranked[0].path;
        }
        return next;
      });
    } catch (e: any) {
      if (!alive.current || my !== seq.current) return;
      setSurveyErr(String(e?.message ?? e));
    } finally {
      if (alive.current && my === seq.current) setSurveying(false);
    }
  }

  async function loadLeftovers() {
    if (liveIds.length === 0) { setLeftovers([]); return; }
    try {
      const res = await LogCollectLeftovers(liveIds);
      if (alive.current) setLeftovers(res ?? []);
    } catch { /* 조용히 */ }
  }

  // 연결 구성이 바뀌면 자동으로 한 번 조사하되, ‘모두 연결’처럼 호스트가 연달아 붙는
  // 동안 N번 발사되지 않도록 잠깐 기다렸다 마지막 상태로만 돈다. 기간 변경은 재조사
  // 대상이 아니다(조사는 왕복이고, 운영자가 ‘다시 조사’로 명시적으로 돌린다).
  const surveyedFor = useRef("");
  useEffect(() => {
    if (!liveKey || surveyedFor.current === liveKey) return;
    const t = setTimeout(() => {
      surveyedFor.current = liveKey;
      runSurvey();
      loadLeftovers();
    }, 400);
    return () => clearTimeout(t);
  }, [liveKey]);

  // ---- 트리 모델 ----
  const tree = useMemo(() => {
    const byCat: Record<string, { hostId: string; name: string; mods: monitor.LogModuleStat[] }[]> = {};
    for (const s of surveys) {
      const h = hosts.find((x) => x.id === s.hostId);
      for (const m of s.modules ?? []) {
        (byCat[m.category] ??= []);
        let row = byCat[m.category].find((r) => r.hostId === s.hostId);
        if (!row) {
          row = { hostId: s.hostId, name: h?.name || s.hostName || s.hostId, mods: [] };
          byCat[m.category].push(row);
        }
        row.mods.push(m);
      }
    }
    return CAT_ORDER.filter((c) => byCat[c]?.length).map((c) => ({ cat: c, hosts: byCat[c] }));
  }, [surveys, hosts]);

  const pickable = (m: monitor.LogModuleStat) =>
    (m.status === "ok" && m.files > 0 && !m.dupOfDir) || m.status === "cmd";

  const journalPicked = useMemo(
    () => surveys.some((s) => (s.modules ?? []).some(
      (m) => m.category === "journal" && sel.has(key(s.hostId, m)))),
    [surveys, sel]
  );

  function toggle(k: string, on: boolean) {
    setSel((prev) => {
      const n = new Set(prev);
      if (on) n.add(k); else n.delete(k);
      return n;
    });
  }
  function toggleMany(keys: string[], on: boolean) {
    setSel((prev) => {
      const n = new Set(prev);
      for (const k of keys) { if (on) n.add(k); else n.delete(k); }
      return n;
    });
  }

  // ---- 선택 합계 (서버별) ----
  //
  // 같은 파일을 두 모듈이 주장하는 경우를 한 번만 센다. keepalived 는
  // /var/log/messages 를 직접 지목하고 var-log 는 /var/log 를 재귀로 훑으므로, 둘 다
  // 켜면 그 파일이 두 번 더해진다. 서버는 dedupeEntries 로 한 번만 세므로, 빼지 않으면
  // 화면만 ‘여유 부족’이라고 서버를 막는다.
  const perServer = useMemo(() => {
    const out: Record<string, { bytes: number; files: number; est: number }> = {};
    for (const s of surveys) {
      const picked = (s.modules ?? []).filter((m) => sel.has(key(s.hostId, m)));
      const recursiveDirs = picked.filter((m) => m.recursive && !m.isFile).map((m) => m.dir);
      let bytes = 0, files = 0, est = 0;
      for (const m of picked) {
        if (m.isCmd) continue; // 명령 출력 크기는 실행 전에는 알 수 없다
        if (m.isFile && recursiveDirs.some((d) => m.dir.startsWith(d.replace(/\/$/, "") + "/"))) {
          continue; // 이미 상위 재귀 스캔에 포함된 파일
        }
        bytes += m.bytes;
        files += m.files;
        est += estOf(m.bytes, m.compressedBytes);
      }
      out[s.hostId] = { bytes, files, est };
    }
    return out;
  }, [surveys, sel]);

  const hasCmdPick = (s: monitor.LogSurvey) =>
    (s.modules ?? []).some((m) => m.isCmd && sel.has(key(s.hostId, m)));

  const totals = useMemo(() => {
    let bytes = 0, files = 0, est = 0, servers = 0;
    for (const s of surveys) {
      const v = perServer[s.hostId];
      if (!v || (v.files === 0 && !hasCmdPick(s))) continue;
      bytes += v.bytes; files += v.files; est += v.est; servers++;
    }
    return { bytes, files, est, servers };
  }, [surveys, perServer, sel]);

  function freeOf(s: monitor.LogSurvey): number {
    const t = (s.targets ?? []).find((x) => x.path === targets[s.hostId]);
    return t ? t.freeBytes : 0;
  }
  function fitsOn(s: monitor.LogSurvey): { need: number; free: number; ok: boolean } {
    const v = perServer[s.hostId] ?? { bytes: 0, est: 0, files: 0 };
    const need = Math.round(v.bytes * COPY_OVERHEAD) + v.est + MIN_FREE;
    const free = freeOf(s);
    return { need, free, ok: free > 0 && need <= free };
  }

  const chosenServers = surveys.filter((s) => {
    const v = perServer[s.hostId];
    return (v && v.files > 0) || hasCmdPick(s);
  });
  const blocked = chosenServers.filter((s) => !fitsOn(s).ok);
  const startable = chosenServers.filter((s) => fitsOn(s).ok);

  function buildTasks(): LogCollectTaskReq[] {
    const fromMs = rangeMode === "span" && fromDate ? new Date(fromDate + "T00:00:00").getTime() : 0;
    const toMs = rangeMode === "span" && toDate ? new Date(toDate + "T23:59:59").getTime() : 0;
    return startable.map((s) => ({
      hostId: s.hostId,
      serverDir: s.dirName || "",
      target: targets[s.hostId] || "",
      recentDays: rangeMode === "recent" ? recentDays : 0,
      fromMs, toMs,
      journalUnits: allUnits ? [] : unitsFor(s),
      picks: (s.modules ?? [])
        .filter((m) => sel.has(key(s.hostId, m)))
        .map((m) => ({ category: m.category, module: m.module, dir: m.isCmd ? "" : m.dir } as monitor.LogPick)),
    }));
  }

  async function removeLeftover(l: monitor.LogLeftover) {
    setBusyLeft(l.path);
    try {
      await DeleteLogCollectLeftover(l.hostId, l.path);
      await loadLeftovers();
    } catch (e: any) {
      setSurveyErr(String(e?.message ?? e));
    } finally {
      if (alive.current) setBusyLeft("");
    }
  }

  async function retryLeftover(l: monitor.LogLeftover) {
    setBusyLeft(l.path);
    try {
      await onRedownload(l.hostId, l.path);
      await loadLeftovers();
    } catch (e: any) {
      setSurveyErr(String(e?.message ?? e));
    } finally {
      if (alive.current) setBusyLeft("");
    }
  }

  const leftBytes = leftovers.reduce((a, l) => a + l.bytes, 0);
  // 실행 중 진행 상황은 고정 푸터에도 둔다. 트리 안에만 두면 카테고리를 접어둔 채
  // 시작했을 때(접힘 상태는 localStorage 에 남는다) 몇 분 동안 화면 어디에도 진행이
  // 보이지 않고, 서버가 많으면 진행 행이 스크롤 밖으로 밀린다.
  const runningRows = Object.entries(progress);

  return (
    <div className="lc-wrap">
      {/* ---- 상단 바 ---- */}
      <div className="lc-bar">
        <label className="mini-sel" title="어느 기간의 로그를 담을지">
          기간
          <select value={rangeMode} onChange={(e) => setRangeMode(e.target.value as any)}>
            <option value="recent">최근 N일</option>
            <option value="span">기간 지정</option>
            <option value="all">전체</option>
          </select>
        </label>
        {rangeMode === "recent" && (
          <label className="mini-sel" title="장애 대응에서 그 이상을 보는 일이 드물고, 용량이 한 자릿수 GB 로 떨어진다">
            <input type="number" min={1} max={90} value={recentDays} style={{ width: 52 }}
              onChange={(e) => setRecentDays(Math.max(1, Math.min(90, Number(e.target.value) || 1)))} />
            일
          </label>
        )}
        {rangeMode === "span" && (
          <>
            <input type="date" value={fromDate} onChange={(e) => setFromDate(e.target.value)} />
            <span style={{ opacity: 0.6 }}>~</span>
            <input type="date" value={toDate} onChange={(e) => setToDate(e.target.value)} />
          </>
        )}
        {journalPicked && (
          <label className="lc-check"
            title={allUnits
              ? "유닛을 좁히지 않으면 journald 만으로 수 GB 가 나오기 쉽습니다 (상한에서 잘리면 매니페스트에 표시됩니다)"
              : `배포된 liz 모듈 + 미들웨어 유닛만 담습니다 (${surveys[0] ? unitsFor(surveys[0]).length : 0}개)`}>
            <input type="checkbox" checked={allUnits} onChange={(e) => setAllUnits(e.target.checked)} />
            journald 전체 유닛
          </label>
        )}
        <span style={{ flex: 1 }} />
        <button className={"toolbtn" + (staleRange ? " primary" : "")}
          onClick={runSurvey} disabled={surveying || running}>
          {surveying ? "조사 중…" : "다시 조사"}
        </button>
      </div>

      {staleRange && (
        <div className="lc-warn">
          기간을 바꿨습니다 — 아래 용량은 이전 기간 기준입니다. ‘다시 조사’를 눌러 다시 계산하세요.
        </div>
      )}
      {surveyErr && <div className="lc-err">{surveyErr}</div>}
      {surveys.some((s) => s.err) && (
        <div className="lc-err">
          {surveys.filter((s) => s.err).map((s) => (
            <div key={s.hostId}>{hosts.find((h) => h.id === s.hostId)?.name ?? s.hostId}: {s.err}</div>
          ))}
        </div>
      )}

      {/* ---- 트리 ---- */}
      <div className="lc-tree">
        {surveying && surveys.length === 0 && <div className="lc-dim">서버를 조사하는 중입니다…</div>}
        {!surveying && tree.length === 0 && (
          <div className="lc-dim">수집할 수 있는 로그를 찾지 못했습니다. ‘다시 조사’를 눌러보세요.</div>
        )}
        {tree.map(({ cat, hosts: rows }) => {
          const catKeys = rows.flatMap((r) => r.mods.filter(pickable).map((m) => key(r.hostId, m)));
          const catOn = catKeys.length > 0 && catKeys.every((k) => sel.has(k));
          const catSome = catKeys.some((k) => sel.has(k));
          const catCollapsed = collapsed.has(cat);
          const catBytes = rows.reduce((a, r) => a + r.mods.reduce((b, m) => b + (sel.has(key(r.hostId, m)) ? m.bytes : 0), 0), 0);
          const catAll = rows.reduce((a, r) => a + r.mods.reduce((b, m) => b + m.bytes, 0), 0);
          return (
            <div className="lc-cat" key={cat}>
              <div className="lc-cat-head">
                <button className="lc-twisty" onClick={() => setCollapsed((p) => {
                  const n = new Set(p); n.has(cat) ? n.delete(cat) : n.add(cat); return n;
                })}>{catCollapsed ? "▸" : "▾"}</button>
                <input type="checkbox" checked={catOn}
                  ref={(el) => { if (el) el.indeterminate = !catOn && catSome; }}
                  onChange={(e) => toggleMany(catKeys, e.target.checked)} />
                <span className="lc-cat-name">{CAT_LABEL[cat] ?? cat}</span>
                <span style={{ flex: 1 }} />
                <span className="lc-dim">선택 {fmtBytes(catBytes)} / 전체 {fmtBytes(catAll)}</span>
              </div>
              {!catCollapsed && rows.map((r) => {
                const hKeys = r.mods.filter(pickable).map((m) => key(r.hostId, m));
                const hOn = hKeys.length > 0 && hKeys.every((k) => sel.has(k));
                const hSome = hKeys.some((k) => sel.has(k));
                const hid = `${cat}|${r.hostId}`;
                const hCollapsed = collapsed.has(hid);
                const hBytes = r.mods.reduce((b, m) => b + (sel.has(key(r.hostId, m)) ? m.bytes : 0), 0);
                const p = progress[r.hostId];
                return (
                  <div className="lc-host" key={hid}>
                    <div className="lc-host-head">
                      <button className="lc-twisty" onClick={() => setCollapsed((s) => {
                        const n = new Set(s); n.has(hid) ? n.delete(hid) : n.add(hid); return n;
                      })}>{hCollapsed ? "▸" : "▾"}</button>
                      <input type="checkbox" checked={hOn}
                        ref={(el) => { if (el) el.indeterminate = !hOn && hSome; }}
                        onChange={(e) => toggleMany(hKeys, e.target.checked)} />
                      <span className="lc-host-name">{r.name}</span>
                      <span className="lc-dim">{hosts.find((h) => h.id === r.hostId)?.addr}</span>
                      <span style={{ flex: 1 }} />
                      {p && <span className={"lc-stage" + (p.err ? " lc-bad" : "")}>{p.err ? "실패" : stageLabel(p)}</span>}
                      <span className="lc-dim">{fmtBytes(hBytes)}</span>
                    </div>
                    {!hCollapsed && (
                      <div className="lc-mods">
                        {r.mods.map((m) => {
                          const k = key(r.hostId, m);
                          const ok = pickable(m);
                          let note = "";
                          if (m.status === "missing") note = "— (없음)";
                          else if (m.status === "denied") note = "권한 없음";
                          else if (m.dupOfDir) note = `중복 — ${m.dupOfDir}`;
                          else if (m.status === "cmd") note = "명령 출력";
                          else if (m.files === 0) note = "이 기간에 파일 없음";
                          return (
                            <label className={"lc-mod" + (ok ? "" : " lc-off")} key={k} title={m.dir}>
                              <input type="checkbox" disabled={!ok} checked={sel.has(k)}
                                onChange={(e) => toggle(k, e.target.checked)} />
                              <span className="lc-mod-name">
                                {m.module}
                                {m.needsRoot && <span className="lc-root" title="root 권한이 필요한 로그입니다">🔒</span>}
                              </span>
                              <span className="lc-mod-dir">{m.dir}</span>
                              <span className="lc-mod-size">{note || fmtBytes(m.bytes)}</span>
                              <span className="lc-mod-files">{note ? "" : `${m.files}개`}</span>
                            </label>
                          );
                        })}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          );
        })}
      </div>

      {/* ---- 서버에 남은 수집물(고아) ---- */}
      {leftovers.length > 0 && (
        <div className="lc-left">
          <div className="lc-left-head">
            ⚠ 서버에 남아 있는 수집물 {leftovers.length}개 · {fmtBytes(leftBytes)}
            <span className="lc-dim" style={{ marginLeft: 8 }}>
              중단된 수집이 남긴 것입니다. 압축본은 다시 내려받을 수 있고, 받고 나면 서버에서 지웁니다.
            </span>
          </div>
          {leftovers.map((l) => (
            <div className="lc-left-row" key={l.path}>
              <span className="lc-dim">{hosts.find((h) => h.id === l.hostId)?.name ?? l.hostId}</span>
              <span className="lc-mono">{l.path}</span>
              {/* 무엇이 남았는지가 곧 그 서버가 어디까지 갔는지다. 압축 스크립트는
                  gzip -t 가 통과한 뒤에야 사본을 지우므로, 압축본이 남았다면 다운로드
                  단계까지 갔던 것이고 사본 디렉터리가 남았다면 아직 복사/압축 중이었다. */}
              <span>{l.kind === "archive" ? "압축본" : "사본 디렉터리"}</span>
              <span>{fmtBytes(l.bytes)}</span>
              <span className="lc-dim">
                {l.kind === "archive"
                  ? "다운로드 중 중단됨"
                  : "압축 전에 중단됨 — 무엇이 담겼는지 알 수 없어 다시 받을 수 없습니다"}
              </span>
              <span style={{ flex: 1 }} />
              {l.kind === "archive" && (
                <button className="toolbtn primary" disabled={!!busyLeft || running}
                  onClick={() => retryLeftover(l)}
                  title="중단됐던 압축본을 다시 내려받습니다. 읽는 데 성공하면 서버에서 지웁니다.">
                  {busyLeft === l.path ? "받는 중…" : "다시 내려받기"}
                </button>
              )}
              <button className="toolbtn danger" disabled={!!busyLeft || running}
                onClick={() => removeLeftover(l)}>
                {busyLeft === l.path ? "삭제 중…" : "삭제"}
              </button>
            </div>
          ))}
        </div>
      )}

      {/* ---- 하단 고정 요약 ---- */}
      <div className="lc-foot">
        {running && runningRows.length > 0 && (
          <div className="lc-progress">
            {runningRows.map(([hid, p]) => (
              <div className={"lc-prog-row" + (p.err ? " lc-bad" : "")} key={hid}>
                <span className="lc-srv-name">{hosts.find((h) => h.id === hid)?.name ?? hid}</span>
                <span className={p.err ? "lc-bad" : "lc-stage"}>{p.err ? "실패" : stageLabel(p)}</span>
                {/* 총량을 아는 단계(복사·다운로드)만 퍼센트를 보여준다. 압축·검증·정리는
                    얼마나 남았는지 알 방법이 없으므로 0%에 멈춘 막대 대신 움직이는
                    막대를 둔다 — 멈춘 막대는 "죽었다"로 읽힌다. */}
                {!p.err && p.stage !== "done" && (
                  p.total > 0 ? (
                    <span className="lc-progbar"><i style={{ width: `${Math.min(100, p.pct)}%` }} /></span>
                  ) : (
                    <span className="lc-progbar indet"><i /></span>
                  )
                )}
                {p.err && <span className="lc-dim">{p.err}</span>}
              </div>
            ))}
          </div>
        )}
        <div className="lc-foot-total">
          선택 <b>{fmtBytes(totals.bytes)}</b> (파일 {totals.files}개, 서버 {totals.servers}대)
          · 예상 아카이브 ≈ <b>{fmtBytes(totals.est)}</b>
        </div>
        <div className="lc-foot-servers">
          {chosenServers.map((s) => {
            const f = fitsOn(s);
            const v = perServer[s.hostId];
            const name = hosts.find((h) => h.id === s.hostId)?.name ?? s.hostId;
            return (
              <div className={"lc-srv" + (f.ok ? "" : " lc-bad")} key={s.hostId}>
                <span className="lc-srv-name">{name}</span>
                <label className="mini-sel" title="사본을 모아 압축할 위치입니다. 원본은 건드리지 않습니다.">
                  스테이징
                  <select value={targets[s.hostId] ?? ""} disabled={running}
                    onChange={(e) => setTargets((p) => ({ ...p, [s.hostId]: e.target.value }))}>
                    {(s.targets ?? []).map((t) => (
                      <option key={t.path} value={t.path}>
                        {t.path} (여유 {fmtBytes(t.freeBytes)})
                      </option>
                    ))}
                  </select>
                </label>
                <span className="lc-dim">
                  {fmtBytes(v?.bytes ?? 0)} → ≈{fmtBytes(v?.est ?? 0)}
                </span>
                {!f.ok && (
                  <span className="lc-bad">
                    ⚠ 여유 {fmtBytes(f.free)} — {fmtBytes(f.need)} 가 필요합니다 (제외하거나 위치를 바꾸세요)
                  </span>
                )}
              </div>
            );
          })}
        </div>
        <div className="lc-actions">
          {blocked.length > 0 && (
            <span className="lc-bad">
              {blocked.length}대는 여유가 부족해 제외됩니다
            </span>
          )}
          <span style={{ flex: 1 }} />
          {result && (
            <>
              <button className="toolbtn" onClick={onOpenFolder}>📁 폴더 열기</button>
              <button className="toolbtn" onClick={onBundle} title="서버별 아카이브를 zip 하나로 묶습니다">
                하나로 묶기
              </button>
              <button className="toolbtn" onClick={onClearResult}>결과 지우기</button>
            </>
          )}
          {running ? (
            <button className="toolbtn danger" onClick={onCancel}>다운로드 취소</button>
          ) : (
            <button className="toolbtn primary" disabled={startable.length === 0}
              onClick={() => onStart(buildTasks())}
              title="사본을 모아 압축하고 내려받은 뒤, 검증에 성공하면 서버의 사본과 압축 파일을 지웁니다">
              수집 → 다운로드 ({startable.length}대)
            </button>
          )}
        </div>
        {result && (
          <div className="lc-result">
            <div>
              저장 위치 <span className="lc-mono">{result.dir}</span>
              {" · "}성공 {result.ok}대{result.failed > 0 && ` · 실패 ${result.failed}대`}
            </div>
            {(result.hosts ?? []).map((r: any) => (
              <div className={"lc-result-row" + (r.err ? " lc-bad" : "")} key={r.hostId}>
                <span className="lc-srv-name">{r.name}</span>
                {r.err ? (
                  <span>{r.err}</span>
                ) : (
                  <span>{r.files}개 · {fmtBytes(r.bytes)} · {r.cleaned ? "서버 정리 완료" : "서버에 아카이브가 남아 있습니다"}</span>
                )}
                {r.leftOnHost && <span className="lc-mono lc-dim">{r.leftOnHost}</span>}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
