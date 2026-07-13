import { useEffect, useRef, useState } from "react";
import { host } from "../../wailsjs/go/models";
import { Frame, HostStatus, SysSample, Proc } from "../types";
import { pct, bytesRate, mib } from "../format";
import Sparkline from "./Sparkline";
import ContextMenu from "./ContextMenu";

const REFRESH_OPTS = [1, 2, 3, 5, 10, 15, 20, 30, 60];

interface Props {
  clusterName: string;
  hosts: host.Host[];
  frames: Record<string, Frame>;
  status: Record<string, HostStatus>;
  sysHist: Record<string, SysSample[]>;
  refreshSec: number;
  onOpenHost: (id: string) => void;
  onConnectOne: (id: string) => void;
  onConnectAll: () => void;
  onDisconnectAll: () => void;
  onChangeInterval: (sec: number) => void;
  // Right-click a process row -> open the terminate menu, pinned to this host.
  onProcMenu?: (hostId: string, pid: number, name: string, service: string, x: number, y: number) => void;
}

const fmtGB = (kib: number) => `${(kib / 1024 / 1024).toFixed(1)} GB`;

function statusLabel(s?: string): string {
  switch (s) {
    case "connecting": return "연결 중…";
    case "probing": return "점검 중…";
    case "uploading": return "업로드 중…";
    case "streaming": return "데이터 대기…";
    case "error": return "오류";
    case "stopped": return "연결 종료됨";
    default: return "연결 안 됨";
  }
}

// ---- vitals summary card (요약 뷰) ----
function Gauge({ value, label }: { value: number; label: string }) {
  const v = Math.max(0, Math.min(100, value));
  const color = v > 90 ? "var(--bad)" : v > 75 ? "var(--warn)" : "var(--accent)";
  return (
    <div className="gauge" title={label}>
      <div className="gauge-fill" style={{ width: `${v}%`, background: color }} />
      <span className="gauge-text">{label}</span>
    </div>
  );
}

function ServerCard({
  h, frame, st, hist, onOpen, onConnect,
}: {
  h: host.Host;
  frame?: Frame;
  st?: HostStatus;
  hist: SysSample[];
  onOpen: () => void;
  onConnect: () => void;
}) {
  const connected = st?.state === "streaming" && !!frame;
  const cpuVals = hist.map((s) => s.cpu);
  const memPct = frame && frame.memTotal > 0 ? (frame.memUsed / frame.memTotal) * 100 : 0;
  const diskBps = frame ? (frame.disks ?? []).reduce((a, d) => a + Math.max(0, d.rBps) + Math.max(0, d.wBps), 0) : 0;

  return (
    <div className="server-card" onClick={onOpen} title="클릭하면 이 서버의 전체 화면으로 이동">
      <div className="sc-head">
        <span className={`dot ${st?.state ?? ""}`} />
        <span className="sc-name">{h.name}</span>
        <span className="sc-addr">{h.user}@{h.addr}</span>
      </div>

      {connected && frame ? (
        <>
          <div className="sc-cpu">
            <div className="sc-metric-row">
              <span className="sc-label">CPU</span>
              <span className="sc-value">{pct(frame.cpu)}</span>
            </div>
            <Sparkline values={cpuVals} max={100} color="#4cc2ff" height={40} />
          </div>
          <div className="sc-metric-row">
            <span className="sc-label">메모리</span>
            <span className="sc-sub">{fmtGB(frame.memUsed)} / {fmtGB(frame.memTotal)}</span>
          </div>
          <Gauge value={memPct} label={`${memPct.toFixed(0)}%`} />
          <div className="sc-foot">
            <span>네트워크 <b>{bytesRate(frame.netRx + frame.netTx)}</b></span>
            <span>디스크 <b>{bytesRate(diskBps)}</b></span>
          </div>
        </>
      ) : (
        <div className="sc-disconnected">
          <span className="sc-status">{statusLabel(st?.state)}{st?.detail ? ` · ${st.detail}` : ""}</span>
          {st?.state !== "connecting" && st?.state !== "probing" && st?.state !== "uploading" && (
            <button className="toolbtn primary" onClick={(e) => { e.stopPropagation(); onConnect(); }}>
              연결
            </button>
          )}
        </div>
      )}
    </div>
  );
}

// ---- process split view ----
type ColKey = "name" | "pid" | "service" | "cpu" | "mem" | "disk" | "net";
const MASTER_ORDER: ColKey[] = ["name", "pid", "service", "cpu", "mem", "disk", "net"];
const COL_LABEL: Record<ColKey, string> = {
  name: "이름", pid: "PID", service: "서비스", cpu: "CPU", mem: "메모리", disk: "디스크", net: "네트워크",
};

// Value used for the shared sort. Text keys lowercased; disk = read+write (−1 if
// denied); everything else numeric.
function sortValue(p: Proc, k: ColKey): number | string {
  switch (k) {
    case "name": return p.name.toLowerCase();
    case "service": return (p.service || "").toLowerCase();
    case "pid": return p.pid;
    case "cpu": return p.cpu;
    case "mem": return p.rssKiB;
    case "disk": return p.diskR < 0 ? -1 : p.diskR + p.diskW;
    case "net": return p.net;
  }
}

function cellValue(p: Proc, k: ColKey): string {
  switch (k) {
    case "name": return p.name;
    case "pid": return String(p.pid);
    case "service": return p.service || "—";
    case "cpu": return pct(p.cpu);
    case "mem": return mib(p.rssKiB);
    case "disk": return p.diskR < 0 ? "—" : bytesRate(p.diskR + p.diskW);
    case "net": return bytesRate(p.net);
  }
}

interface Sort { key: ColKey; dir: 1 | -1 }
type Density = "compact" | "normal" | "wide";

function ServerProcColumn({
  h, frame, st, cols, colW, density, hideKthreads, sort, onSort, onResizeStart, onColMenu, onRowMenu, onOpen, onConnect,
}: {
  h: host.Host;
  frame?: Frame;
  st?: HostStatus;
  cols: ColKey[];
  colW: Record<ColKey, number>;
  density: Density;
  hideKthreads: boolean;
  sort: Sort;
  onSort: (k: ColKey) => void;
  onResizeStart: (k: ColKey, e: React.MouseEvent) => void;
  onColMenu: (k: ColKey, e: React.MouseEvent) => void;
  onRowMenu?: (pid: number, name: string, service: string, e: React.MouseEvent) => void;
  onOpen: () => void;
  onConnect: () => void;
}) {
  const connected = st?.state === "streaming" && !!frame;
  let procs: Proc[] = frame?.procs ?? [];
  if (hideKthreads) procs = procs.filter((p) => !p.name.startsWith("["));
  procs = [...procs].sort((a, b) => {
    const av = sortValue(a, sort.key), bv = sortValue(b, sort.key);
    const cmp = av < bv ? -1 : av > bv ? 1 : 0;
    return cmp !== 0 ? cmp * sort.dir : b.cpu - a.cpu;
  });

  return (
    <div className="proc-col">
      <div className="proc-col-head" onClick={onOpen} title="클릭하면 이 서버 전체 화면으로 이동">
        <span className={`dot ${st?.state ?? ""}`} />
        <span className="pc-name">{h.name}</span>
        <span className="pc-count">{connected ? `${procs.length}` : ""}</span>
      </div>
      {connected && frame ? (
        <div className="pc-scroll">
          <table className={`pc-table ${density}`}>
            <thead>
              <tr>
                {cols.map((c) => (
                  <th key={c} className={c === "name" || c === "service" ? "left" : ""}
                    style={{ width: c === "service" ? undefined : colW[c] }}
                    onClick={(e) => { e.stopPropagation(); onSort(c); }}
                    onContextMenu={(e) => { e.preventDefault(); e.stopPropagation(); onColMenu(c, e); }}
                    title="클릭: 정렬 · 우클릭: 컬럼 제거">
                    {COL_LABEL[c]}
                    {sort.key === c && <span className="pc-arrow">{sort.dir === 1 ? " ▲" : " ▼"}</span>}
                    {c !== "service" && (
                      <span className="col-resizer"
                        onMouseDown={(e) => onResizeStart(c, e)}
                        onClick={(e) => e.stopPropagation()}
                        title="드래그해서 폭 조절" />
                    )}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {procs.map((p) => (
                <tr
                  key={p.pid}
                  onContextMenu={
                    onRowMenu
                      ? (e) => { e.preventDefault(); e.stopPropagation(); onRowMenu(p.pid, p.name, p.service, e); }
                      : undefined
                  }
                >
                  {cols.map((c) => (
                    <td key={c} className={c === "name" || c === "service" ? "left" : ""}
                      title={c === "name" ? p.name : c === "service" ? p.service : undefined}>
                      {cellValue(p, c)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="pc-disconnected">
          <span>{statusLabel(st?.state)}</span>
          {st?.state !== "connecting" && st?.state !== "probing" && st?.state !== "uploading" && (
            <button className="toolbtn primary" onClick={(e) => { e.stopPropagation(); onConnect(); }}>연결</button>
          )}
        </div>
      )}
    </div>
  );
}

// Column visibility dropdown (default all on; at least one must remain).
function ColumnPicker({ visible, onToggle }: { visible: Set<ColKey>; onToggle: (k: ColKey) => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);
  return (
    <div className="col-picker" ref={ref}>
      <button className="toolbtn" onClick={() => setOpen((o) => !o)}>컬럼 ▾</button>
      {open && (
        <div className="col-picker-menu">
          {MASTER_ORDER.map((c) => (
            <label key={c} className="col-picker-item">
              <input type="checkbox" checked={visible.has(c)} onChange={() => onToggle(c)} />
              {COL_LABEL[c]}
            </label>
          ))}
        </div>
      )}
    </div>
  );
}

const PER_PAGE_OPTS: (number | "all")[] = [1, 2, 3, 4, 5, "all"];

export default function ClusterOverview({
  clusterName, hosts, frames, status, sysHist, refreshSec,
  onOpenHost, onConnectOne, onConnectAll, onDisconnectAll, onChangeInterval, onProcMenu,
}: Props) {
  const connectedCount = hosts.filter((h) => status[h.id]?.state === "streaming").length;
  const [mode, setMode] = useState<"summary" | "proc">("summary");
  const [hideKthreads, setHideKthreads] = useState(true);
  const [page, setPage] = useState(0);
  const [sort, setSort] = useState<Sort>({ key: "cpu", dir: -1 });
  const [perPageChoice, setPerPageChoice] = useState<number | "all">(3);
  const [visibleCols, setVisibleCols] = useState<Set<ColKey>>(() => new Set(MASTER_ORDER));
  const [density, setDensity] = useState<Density>("compact");
  const [cardSize, setCardSize] = useState<"small" | "normal" | "large">("normal");
  const [colMenu, setColMenu] = useState<{ x: number; y: number; col: ColKey } | null>(null);

  // Per-column widths, shared across every server pane. 서비스는 폭 미지정(남는
  // 공간을 전부 차지)이라 여기에 없다 — 다른 컬럼을 좁히면 서비스가 자동으로 넓어진다.
  const [colW, setColW] = useState<Record<ColKey, number>>(() => ({
    name: 120, pid: 58, cpu: 48, mem: 60, disk: 62, net: 62, service: 0,
  }));
  const dragRef = useRef<{ col: ColKey; startX: number; startW: number } | null>(null);
  useEffect(() => {
    const onMove = (e: MouseEvent) => {
      const d = dragRef.current;
      if (!d) return;
      const w = Math.max(40, Math.min(420, d.startW + (e.clientX - d.startX)));
      setColW((prev) => ({ ...prev, [d.col]: w }));
    };
    const onUp = () => {
      dragRef.current = null;
      document.body.classList.remove("col-resizing");
    };
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
    return () => {
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
    };
  }, []);
  function startResize(col: ColKey, e: React.MouseEvent) {
    dragRef.current = { col, startX: e.clientX, startW: colW[col] };
    document.body.classList.add("col-resizing");
    e.preventDefault();
    e.stopPropagation();
  }

  // Header click: same column toggles direction; new column starts desc for
  // numeric metrics, asc for text (name/service). Applies to all servers.
  function onSort(k: ColKey) {
    setSort((prev) =>
      prev.key === k
        ? { key: k, dir: prev.dir === 1 ? -1 : 1 }
        : { key: k, dir: k === "name" || k === "service" ? 1 : -1 }
    );
  }
  function toggleCol(k: ColKey) {
    setVisibleCols((prev) => {
      const next = new Set(prev);
      if (next.has(k)) { if (next.size > 1) next.delete(k); } else next.add(k);
      return next;
    });
  }

  const cols = MASTER_ORDER.filter((c) => visibleCols.has(c));
  const total = Math.max(1, hosts.length);
  const perPage = perPageChoice === "all" ? total : Math.min(perPageChoice, total);
  const pages = Math.max(1, Math.ceil(hosts.length / perPage));
  const curPage = Math.min(page, pages - 1);
  const pageHosts = hosts.slice(curPage * perPage, curPage * perPage + perPage);

  return (
    <div className="main">
      <div className="main-topbar">
        <span className="main-title">
          📦 {clusterName}
          <span className="sub">서버 {connectedCount}/{hosts.length} 연결됨</span>
        </span>
        <div className="viewtabs">
          <button className={"toolbtn" + (mode === "summary" ? " primary" : "")}
            onClick={() => setMode("summary")}>요약</button>
          <button className={"toolbtn" + (mode === "proc" ? " primary" : "")}
            onClick={() => setMode("proc")}>프로세스</button>
        </div>
        <button className="toolbtn primary" onClick={onConnectAll}>모두 연결</button>
        <button className="toolbtn" onClick={onDisconnectAll}>모두 해제</button>

        {mode === "summary" && (
          <label className="mini-sel" title="요약 카드 크기">
            카드
            <select value={cardSize} onChange={(e) => setCardSize(e.target.value as any)}>
              <option value="small">작게</option>
              <option value="normal">보통</option>
              <option value="large">크게</option>
            </select>
          </label>
        )}

        {mode === "proc" && (
          <>
            <label className="mini-sel" title="한 화면에 표시할 서버 수">
              서버
              <select value={String(perPageChoice)}
                onChange={(e) => { setPage(0); setPerPageChoice(e.target.value === "all" ? "all" : Number(e.target.value)); }}>
                {PER_PAGE_OPTS.map((o) => (
                  <option key={String(o)} value={String(o)}>{o === "all" ? "전체" : `${o}대`}</option>
                ))}
              </select>
            </label>
            <ColumnPicker visible={visibleCols} onToggle={toggleCol} />
            <label className="mini-sel" title="행 높이(밀도)">
              밀도
              <select value={density} onChange={(e) => setDensity(e.target.value as Density)}>
                <option value="compact">촘촘</option>
                <option value="normal">보통</option>
                <option value="wide">넓게</option>
              </select>
            </label>
            <label style={{ display: "flex", alignItems: "center", gap: 4, fontSize: 12, whiteSpace: "nowrap" }}
              title="커널 스레드([대괄호] 프로세스) 숨기기">
              <input type="checkbox" checked={hideKthreads} onChange={(e) => setHideKthreads(e.target.checked)} />
              커널 스레드 숨김
            </label>
          </>
        )}

        <span style={{ flex: 1 }} />
        {mode === "proc" && pages > 1 && (
          <div className="proc-pager">
            <button className="toolbtn" disabled={curPage <= 0} onClick={() => setPage(curPage - 1)}>‹</button>
            <span className="pager-ind">{curPage + 1} / {pages}</span>
            <button className="toolbtn" disabled={curPage >= pages - 1} onClick={() => setPage(curPage + 1)}>›</button>
          </div>
        )}
        <label className="refresh-sel" title="화면 갱신 주기 (1~60초)">
          갱신
          <select value={refreshSec} onChange={(e) => onChangeInterval(Number(e.target.value))}>
            {REFRESH_OPTS.map((s) => (<option key={s} value={s}>{s}초</option>))}
          </select>
        </label>
      </div>

      {mode === "summary" ? (
        <div className={`cluster-grid ${cardSize}`}>
          {hosts.map((h) => (
            <ServerCard
              key={h.id}
              h={h}
              frame={frames[h.id]}
              st={status[h.id]}
              hist={sysHist[h.id] ?? []}
              onOpen={() => onOpenHost(h.id)}
              onConnect={() => onConnectOne(h.id)}
            />
          ))}
        </div>
      ) : (
        <div className="proc-split">
          {pageHosts.map((h) => (
            <ServerProcColumn
              key={h.id}
              h={h}
              frame={frames[h.id]}
              st={status[h.id]}
              cols={cols}
              colW={colW}
              density={density}
              hideKthreads={hideKthreads}
              sort={sort}
              onSort={onSort}
              onResizeStart={startResize}
              onColMenu={(k, e) => setColMenu({ x: e.clientX, y: e.clientY, col: k })}
              onRowMenu={onProcMenu ? (pid, name, service, e) => onProcMenu(h.id, pid, name, service, e.clientX, e.clientY) : undefined}
              onOpen={() => onOpenHost(h.id)}
              onConnect={() => onConnectOne(h.id)}
            />
          ))}
        </div>
      )}

      {colMenu && (
        <ContextMenu
          x={colMenu.x}
          y={colMenu.y}
          items={[
            {
              label: `‘${COL_LABEL[colMenu.col]}’ 컬럼 제거`,
              danger: true,
              onClick: () => toggleCol(colMenu.col),
            },
          ]}
          onClose={() => setColMenu(null)}
        />
      )}
    </div>
  );
}
