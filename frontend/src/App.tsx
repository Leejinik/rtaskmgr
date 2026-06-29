import { useEffect, useRef, useState } from "react";
import { EventsOn } from "../wailsjs/runtime";
import {
  ListHosts, Connect, Disconnect, DeleteHost, SetInterval,
  StartRecording, StopRecording,
  NethogsInstall, NethogsRollback,
  OpenLogDialog, LogFrames,
} from "../wailsjs/go/main/App";

const REFRESH_OPTS = [1, 2, 3, 5, 10, 15, 20, 30, 60];
import { host, main } from "../wailsjs/go/models";
import { Frame, HostStatus, Capabilities, SortKey, SortSpec, MAX_SORT } from "./types";
import ProcTable from "./components/ProcTable";
import DetailModal from "./components/DetailModal";
import ConnectDialog from "./components/ConnectDialog";
import ContextMenu from "./components/ContextMenu";
import PlaybackBar from "./components/PlaybackBar";
import ScheduledModal from "./components/ScheduledModal";

interface Playback {
  meta: main.LogMeta;
  hostId: string;
  frames: Frame[];
  index: number;
  playing: boolean;
}

const basename = (p: string) => p.split(/[\\/]/).pop() || p;
import "./taskmgr.css";

export default function App() {
  const [hosts, setHosts] = useState<host.Host[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [frames, setFrames] = useState<Record<string, Frame>>({});
  const [status, setStatus] = useState<Record<string, HostStatus>>({});
  const [caps, setCaps] = useState<Record<string, Capabilities>>({});
  const [nethogs, setNethogs] = useState<Record<string, { active: boolean; installedByUs: boolean }>>({});
  const [nhBusy, setNhBusy] = useState<Record<string, boolean>>({});

  const [search, setSearch] = useState("");
  // Empty = the implicit default (CPU descending), which is always the lowest-
  // priority tiebreaker. Explicit sorts layer ON TOP of it, so clicking any
  // column visibly reorders while CPU-desc still breaks ties.
  const [sort, setSort] = useState<SortSpec[]>([]);
  const [selectedPid, setSelectedPid] = useState<number | null>(null);
  const [detailPid, setDetailPid] = useState<number | null>(null);
  const [playback, setPlayback] = useState<Playback | null>(null);

  const [dialog, setDialog] = useState<{ open: boolean; editing?: host.Host }>({ open: false });
  const [ctx, setCtx] = useState<{ x: number; y: number; h: host.Host } | null>(null);
  const [recording, setRecording] = useState<{ active: boolean; hostId: string; path: string }>(
    { active: false, hostId: "", path: "" }
  );
  const [schedOpen, setSchedOpen] = useState(false);
  const [refreshSec, setRefreshSec] = useState<number>(
    () => Number(localStorage.getItem("rtm.interval")) || 10
  );
  const [toast, setToast] = useState("");

  const toastTimer = useRef<number | null>(null);
  const showToast = (m: string) => {
    setToast(m);
    if (toastTimer.current) window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast(""), 3000);
  };

  const refreshHosts = async () => {
    const h = await ListHosts().catch(() => [] as host.Host[]);
    setHosts(h ?? []);
    return h ?? [];
  };

  useEffect(() => { void refreshHosts(); }, []);

  // ---- backend events ----
  useEffect(() => {
    const offFrame = EventsOn("frame", (f: Frame) => {
      setFrames((prev) => ({ ...prev, [f.hostId]: f }));
    });
    const offStatus = EventsOn("status", (s: any) => {
      setStatus((prev) => ({ ...prev, [s.hostId]: { state: s.state, detail: s.detail } }));
    });
    const offNet = EventsOn("nethogs", (s: any) => {
      setNethogs((prev) => ({
        ...prev,
        [s.hostId]: { active: s.active, installedByUs: s.installedByUs },
      }));
      if (s.msg) showToast(s.msg);
    });
    const offRec = EventsOn("recording", (s: any) => {
      setRecording({ active: s.active, hostId: s.hostId || "", path: s.path || "" });
      if (s.auto) showToast("연결이 끊겨 실시간 기록을 자동 중지했습니다");
      else if (s.active) showToast(`기록 시작 — ${s.path}`);
      else if (s.path) showToast(`기록 종료 — ${s.path}`);
    });
    return () => { offFrame(); offStatus(); offNet(); offRec(); };
  }, []);

  // ---- Ctrl+S toggles immediate recording for the selected host ----
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
        e.preventDefault();
        void toggleRecord();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  async function toggleRecord() {
    try {
      if (recording.active) {
        await StopRecording();
        return;
      }
      if (!selectedId) {
        showToast("기록할 호스트를 먼저 선택하세요");
        return;
      }
      await StartRecording(selectedId); // opens native Save As; emits "recording"
    } catch (err: any) {
      showToast(`기록 실패: ${err}`);
    }
  }

  // ---- log playback ----
  async function openLog() {
    try {
      const meta = await OpenLogDialog();
      if (!meta || !meta.hosts || meta.hosts.length === 0) return;
      const hostId = meta.hosts[0].id;
      const frames = (await LogFrames(hostId)) ?? [];
      setDetailPid(null);
      setPlayback({ meta, hostId, frames, index: 0, playing: false });
    } catch (e: any) {
      showToast(`로그 열기 실패: ${e}`);
    }
  }

  async function selectLogHost(hostId: string) {
    const frames = (await LogFrames(hostId)) ?? [];
    setPlayback((p) => (p ? { ...p, hostId, frames, index: 0, playing: false } : p));
  }

  // advance frames while playing
  useEffect(() => {
    if (!playback?.playing) return;
    const t = window.setInterval(() => {
      setPlayback((p) => {
        if (!p) return p;
        if (p.index >= p.frames.length - 1) return { ...p, playing: false };
        return { ...p, index: p.index + 1 };
      });
    }, 1000);
    return () => window.clearInterval(t);
  }, [playback?.playing]);

  async function handleConnect(id: string) {
    setSelectedId(id);
    setStatus((prev) => ({ ...prev, [id]: { state: "connecting", detail: "" } }));
    try {
      const c = await Connect(id, refreshSec);
      setCaps((prev) => ({ ...prev, [id]: c }));
      if (!c.sudo) {
        showToast("sudo 비밀번호가 일치하지 않아 몇몇 정보는 비활성화됩니다");
      }
    } catch (e: any) {
      showToast(`연결 실패: ${e}`);
    }
  }

  // changeInterval updates the live refresh interval, persists it as the default,
  // and applies it immediately to all connected hosts.
  function changeInterval(sec: number) {
    setRefreshSec(sec);
    try { localStorage.setItem("rtm.interval", String(sec)); } catch {}
    Object.entries(status).forEach(([id, st]) => {
      if (st.state === "streaming") void SetInterval(id, sec);
    });
  }

  function handleDisconnect(id: string) {
    Disconnect(id);
    setFrames((prev) => { const n = { ...prev }; delete n[id]; return n; });
    setNethogs((prev) => { const n = { ...prev }; delete n[id]; return n; });
  }

  async function handleNethogsInstall(id: string) {
    setNhBusy((prev) => ({ ...prev, [id]: true }));
    try {
      await NethogsInstall(id);
    } catch (e: any) {
      showToast(`네트워크 수집 실패: ${e}`);
    } finally {
      setNhBusy((prev) => ({ ...prev, [id]: false }));
    }
  }

  async function handleNethogsRollback(id: string) {
    setNhBusy((prev) => ({ ...prev, [id]: true }));
    try {
      await NethogsRollback(id);
    } catch (e: any) {
      showToast(`롤백 실패: ${e}`);
    } finally {
      setNhBusy((prev) => ({ ...prev, [id]: false }));
    }
  }

  async function handleSaved(h: host.Host, connect: boolean) {
    setDialog({ open: false });
    await refreshHosts();
    setSelectedId(h.id);
    if (connect) void handleConnect(h.id);
  }

  async function handleDelete(id: string) {
    const h = hosts.find((x) => x.id === id);
    if (!window.confirm(`호스트 '${h?.name ?? id}' 을(를) 목록에서 삭제할까요?`)) return;
    handleDisconnect(id);
    await DeleteHost(id).catch((e) => showToast(`삭제 실패: ${e}`));
    const list = await refreshHosts();
    if (selectedId === id) setSelectedId(list[0]?.id ?? null);
  }

  // onSort: each click cycles the column through ascending → descending →
  // unsorted. Columns accumulate in click order (first click = highest
  // priority), up to MAX_SORT. When every sort is cleared the table falls back
  // to the default (CPU descending).
  function onSort(k: SortKey) {
    if (!sort.some((s) => s.key === k) && sort.length >= MAX_SORT) {
      showToast(`정렬은 최대 ${MAX_SORT}개까지 가능합니다`);
      return;
    }
    setSort((prev) => {
      const i = prev.findIndex((s) => s.key === k);
      if (i < 0) {
        return [...prev, { key: k, dir: 1 }]; // new column → ascending
      }
      if (prev[i].dir === 1) {
        const n = [...prev]; // ascending → descending
        n[i] = { key: k, dir: -1 };
        return n;
      }
      return prev.filter((_, idx) => idx !== i); // descending → unsorted (drop)
    });
  }

  const selected = hosts.find((h) => h.id === selectedId) ?? null;
  const frame = selectedId ? frames[selectedId] : undefined;
  const st = selectedId ? status[selectedId] : undefined;
  const cap = selectedId ? caps[selectedId] : undefined;
  const connected = st?.state === "streaming" && !!frame;
  const detailProc =
    frame && detailPid != null ? frame.procs.find((p) => p.pid === detailPid) : undefined;

  // ---- playback screen (log reader) ----
  if (playback) {
    const pf = playback.frames[playback.index];
    const pbDetail =
      pf && detailPid != null ? pf.procs.find((p) => p.pid === detailPid) : undefined;
    return (
      <div className="app">
        <PlaybackBar
          meta={playback.meta}
          hostId={playback.hostId}
          index={playback.index}
          total={playback.frames.length}
          playing={playback.playing}
          currentT={pf?.t ?? 0}
          onHost={selectLogHost}
          onIndex={(i) => setPlayback((p) => (p ? { ...p, index: i, playing: false } : p))}
          onPlayToggle={() => setPlayback((p) => (p ? { ...p, playing: !p.playing } : p))}
          onStep={(d) =>
            setPlayback((p) =>
              p
                ? { ...p, playing: false, index: Math.min(p.frames.length - 1, Math.max(0, p.index + d)) }
                : p
            )
          }
          onClose={() => { setDetailPid(null); setPlayback(null); }}
        />
        <div className="topbar" style={{ borderTop: "1px solid var(--border)" }}>
          <input
            className="search"
            placeholder="이름, 서비스 또는 PID로 검색"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        {pf ? (
          <ProcTable
            frame={pf}
            search={search}
            sort={sort}
            selectedPid={selectedPid}
            onSort={onSort}
            onSelect={setSelectedPid}
            onOpen={(pid) => setDetailPid(pid)}
          />
        ) : (
          <div className="empty">이 호스트의 프레임이 없습니다</div>
        )}
        {detailPid != null && (
          <DetailModal
            hostId={playback.hostId}
            pid={detailPid}
            current={pbDetail}
            frames={playback.frames}
            onClose={() => setDetailPid(null)}
          />
        )}
        {toast && <div className="toast">{toast}</div>}
      </div>
    );
  }

  return (
    <div className="shell">
      {/* ---- sidebar ---- */}
      <aside className="sidebar">
        <div className="sidebar-header">
          <span className="title">호스트</span>
          <button className="toolbtn primary" onClick={() => setDialog({ open: true })}>
            + 추가
          </button>
        </div>
        <div className="sidebar-sub">RHEL8/9 SSH 대상</div>
        <div className="host-list">
          {hosts.length === 0 ? (
            <div className="sidebar-empty">
              저장된 호스트가 없습니다.<br />“+ 추가”로 등록하세요.
            </div>
          ) : (
            hosts.map((h) => {
              const s = status[h.id]?.state;
              return (
                <div
                  key={h.id}
                  className={"host-item" + (selectedId === h.id ? " selected" : "")}
                  onClick={() => setSelectedId(h.id)}
                  onContextMenu={(e) => {
                    e.preventDefault();
                    setCtx({ x: e.clientX, y: e.clientY, h });
                  }}
                >
                  <div className="host-line1">
                    <span className={`dot ${s ?? ""}`} />
                    <span className="host-name">{h.name}</span>
                    <button
                      className="host-more"
                      title="편집 / 삭제"
                      onClick={(e) => {
                        e.stopPropagation();
                        const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
                        setCtx({ x: r.right, y: r.bottom, h });
                      }}
                    >
                      ⋯
                    </button>
                  </div>
                  <div className="host-line2">{h.user}@{h.addr}</div>
                </div>
              );
            })
          )}
        </div>
        <div className="sidebar-footer">
          <button className="toolbtn" style={{ width: "100%" }} onClick={openLog}>
            📂 로그 열기 (재생)
          </button>
        </div>
      </aside>

      {/* ---- main ---- */}
      <main className="main">
        <div className="main-topbar">
          {selected ? (
            <>
              <span className="main-title">
                {selected.name}
                <span className="sub">{selected.user}@{selected.addr}</span>
              </span>
              {connected ? (
                <button className="toolbtn" onClick={() => handleDisconnect(selected.id)}>
                  연결 해제
                </button>
              ) : (
                <button className="toolbtn primary" onClick={() => handleConnect(selected.id)}>
                  연결
                </button>
              )}
              <input
                className="search"
                placeholder="이름, 서비스 또는 PID로 검색"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
              <label className="refresh-sel" title="화면 갱신 주기 (1~60초)">
                갱신
                <select value={refreshSec} onChange={(e) => changeInterval(Number(e.target.value))}>
                  {REFRESH_OPTS.map((s) => (
                    <option key={s} value={s}>{s}초</option>
                  ))}
                </select>
              </label>
              {connected && (
                nethogs[selected.id]?.active ? (
                  <button
                    className="toolbtn"
                    disabled={nhBusy[selected.id]}
                    onClick={() => handleNethogsRollback(selected.id)}
                    title="네트워크 수집을 중지하고, 이 앱이 설치한 경우 nethogs를 제거(롤백)합니다"
                  >
                    {nhBusy[selected.id]
                      ? "처리 중…"
                      : nethogs[selected.id]?.installedByUs
                      ? "● 네트워크 롤백(제거)"
                      : "● 네트워크 수집 중지"}
                  </button>
                ) : (
                  <button
                    className="toolbtn"
                    disabled={nhBusy[selected.id]}
                    onClick={() => handleNethogsInstall(selected.id)}
                    title="nethogs로 프로세스별 네트워크 사용량 수집을 시작합니다 (없으면 오프라인 RPM 설치)"
                  >
                    {nhBusy[selected.id]
                      ? "설치 중…"
                      : caps[selected.id]?.nethogs
                      ? "네트워크 수집 시작"
                      : "네트워크 수집 (nethogs 설치)"}
                  </button>
                )
              )}
              {connected && (
                <button
                  className={"toolbtn" + (recording.active && recording.hostId === selected.id ? " danger" : "")}
                  onClick={toggleRecord}
                  title="실시간 기록을 내 PC 파일로 저장/중지 (Ctrl+S). 연결이 끊기면 자동 중지됩니다."
                >
                  {recording.active && recording.hostId === selected.id ? "■ 기록 중지" : "● 기록"}
                </button>
              )}
              {connected && (
                <button
                  className="toolbtn"
                  onClick={() => setSchedOpen(true)}
                  title="서버에서 detached로 예약 기록 (클라이언트를 꺼도 계속, 최대 7일)"
                >
                  ⏱ 예약 기록…
                </button>
              )}
            </>
          ) : (
            <span className="main-title sub">호스트를 선택하세요</span>
          )}
        </div>

        {!selected && (
          <div className="empty">
            <div style={{ fontSize: 15 }}>왼쪽 사이드바에서 호스트를 추가하거나 선택하세요</div>
          </div>
        )}

        {selected && !frame && (
          <div className="empty">
            <div>{statusLabel(st?.state)} {st?.detail}</div>
            {st?.state !== "connecting" && st?.state !== "probing" && st?.state !== "uploading" && (
              <button className="toolbtn primary" onClick={() => handleConnect(selected.id)}>
                연결
              </button>
            )}
          </div>
        )}

        {selected && frame && (
          <ProcTable
            frame={frame}
            search={search}
            sort={sort}
            selectedPid={selectedPid}
            onSort={onSort}
            onSelect={setSelectedPid}
            onOpen={(pid) => setDetailPid(pid)}
          />
        )}

        <div className="statusline">
          {frame && <span>{frame.ncpu} vCPU · 메모리 {(frame.memTotal / 1024 / 1024).toFixed(1)} GB</span>}
          {frame && <span>프로세스 {frame.procs.length}</span>}
          {cap && (
            <span>
              {cap.os || "?"} · 권한 {cap.sudo ? "관리자(sudo)" : "일반"} · nethogs {cap.nethogs ? "예" : "없음"}
            </span>
          )}
          <span style={{ flex: 1 }} />
          {recording.active ? (
            <span className="rec">● 실시간 기록 중 — {basename(recording.path)}</span>
          ) : (
            <span style={{ color: "var(--text-mute)" }}>○ 기록 안 함 (● 기록 / Ctrl+S)</span>
          )}
        </div>
      </main>

      {dialog.open && (
        <ConnectDialog
          initial={dialog.editing}
          onSaved={handleSaved}
          onClose={() => setDialog({ open: false })}
        />
      )}

      {schedOpen && selected && (
        <ScheduledModal
          hostId={selected.id}
          hostName={selected.name}
          onClose={() => setSchedOpen(false)}
          onPlay={(meta) => {
            setSchedOpen(false);
            const hid = meta.hosts?.[0]?.id;
            if (hid) {
              LogFrames(hid).then((frames) => {
                setDetailPid(null);
                setPlayback({ meta, hostId: hid, frames: frames ?? [], index: 0, playing: false });
              });
            }
          }}
        />
      )}

      {ctx && (
        <ContextMenu
          x={ctx.x}
          y={ctx.y}
          items={[
            { label: "편집", onClick: () => setDialog({ open: true, editing: ctx.h }) },
            { label: "삭제", danger: true, onClick: () => handleDelete(ctx.h.id) },
          ]}
          onClose={() => setCtx(null)}
        />
      )}

      {detailPid != null && selectedId && (
        <DetailModal
          hostId={selectedId}
          pid={detailPid}
          current={detailProc}
          onClose={() => setDetailPid(null)}
        />
      )}

      {toast && <div className="toast">{toast}</div>}
    </div>
  );
}

function statusLabel(s?: string): string {
  switch (s) {
    case "connecting": return "연결 중…";
    case "probing": return "호스트 점검 중…";
    case "uploading": return "샘플러 업로드 중…";
    case "streaming": return "데이터 수신 대기 중…";
    case "error": return "오류:";
    case "stopped": return "연결 종료됨.";
    default: return "연결되지 않음.";
  }
}
