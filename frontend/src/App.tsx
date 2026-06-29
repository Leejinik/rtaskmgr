import { useEffect, useRef, useState } from "react";
import { EventsOn } from "../wailsjs/runtime";
import {
  ListHosts, Connect, Disconnect, DeleteHost,
  SaveLog, IsLogNamed, KeepLogAndQuit, DiscardAndQuit,
} from "../wailsjs/go/main/App";
import { host } from "../wailsjs/go/models";
import { Frame, HostStatus, Capabilities, SortKey } from "./types";
import ProcTable from "./components/ProcTable";
import DetailModal from "./components/DetailModal";
import ConnectDialog from "./components/ConnectDialog";
import NamePrompt from "./components/NamePrompt";
import ContextMenu from "./components/ContextMenu";
import "./taskmgr.css";

export default function App() {
  const [hosts, setHosts] = useState<host.Host[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [frames, setFrames] = useState<Record<string, Frame>>({});
  const [status, setStatus] = useState<Record<string, HostStatus>>({});
  const [caps, setCaps] = useState<Record<string, Capabilities>>({});

  const [search, setSearch] = useState("");
  const [sortKey, setSortKey] = useState<SortKey>("cpu");
  const [sortDir, setSortDir] = useState<1 | -1>(-1);
  const [selectedPid, setSelectedPid] = useState<number | null>(null);
  const [detailPid, setDetailPid] = useState<number | null>(null);

  const [dialog, setDialog] = useState<{ open: boolean; editing?: host.Host }>({ open: false });
  const [ctx, setCtx] = useState<{ x: number; y: number; h: host.Host } | null>(null);
  const [namePrompt, setNamePrompt] = useState<null | "save" | "exit">(null);
  const [logName, setLogName] = useState("");
  const [recNamed, setRecNamed] = useState(false);
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
    const offConfirm = EventsOn("confirm-save", () => setNamePrompt("exit"));
    return () => { offFrame(); offStatus(); offConfirm(); };
  }, []);

  // ---- Ctrl+S ----
  useEffect(() => {
    const onKey = async (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
        e.preventDefault();
        if (await IsLogNamed()) showToast(`기록 중 — ${logName || "저장된 로그"}`);
        else setNamePrompt("save");
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [logName]);

  async function handleConnect(id: string) {
    setSelectedId(id);
    setStatus((prev) => ({ ...prev, [id]: { state: "connecting", detail: "" } }));
    try {
      const c = await Connect(id);
      setCaps((prev) => ({ ...prev, [id]: c }));
      if (!c.sudo) {
        showToast("sudo 비밀번호가 일치하지 않아 몇몇 정보는 비활성화됩니다");
      }
    } catch (e: any) {
      showToast(`연결 실패: ${e}`);
    }
  }

  function handleDisconnect(id: string) {
    Disconnect(id);
    setFrames((prev) => { const n = { ...prev }; delete n[id]; return n; });
  }

  async function handleSaved(h: host.Host, connect: boolean) {
    setDialog({ open: false });
    await refreshHosts();
    setSelectedId(h.id);
    if (connect) void handleConnect(h.id);
  }

  async function handleDelete(id: string) {
    handleDisconnect(id);
    await DeleteHost(id).catch(() => {});
    const list = await refreshHosts();
    if (selectedId === id) setSelectedId(list[0]?.id ?? null);
  }

  function onSort(k: SortKey) {
    if (k === sortKey) setSortDir((d) => (d === 1 ? -1 : 1));
    else {
      setSortKey(k);
      setSortDir(k === "name" || k === "service" || k === "user" ? 1 : -1);
    }
  }

  async function doSaveLog(name: string) {
    try {
      await SaveLog(name);
      setLogName(name);
      setRecNamed(true);
      setNamePrompt(null);
      showToast(`로그 저장 시작 — ${name}`);
    } catch (e: any) {
      showToast(`저장 실패: ${e}`);
    }
  }

  const selected = hosts.find((h) => h.id === selectedId) ?? null;
  const frame = selectedId ? frames[selectedId] : undefined;
  const st = selectedId ? status[selectedId] : undefined;
  const cap = selectedId ? caps[selectedId] : undefined;
  const connected = st?.state === "streaming" && !!frame;
  const detailProc =
    frame && detailPid != null ? frame.procs.find((p) => p.pid === detailPid) : undefined;

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
                  </div>
                  <div className="host-line2">{h.user}@{h.addr}</div>
                </div>
              );
            })
          )}
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
            sortKey={sortKey}
            sortDir={sortDir}
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
          <span className={`rec ${recNamed ? "armed" : ""}`}>
            ● {recNamed ? `기록 중 — ${logName}` : "임시 기록 중 (Ctrl+S로 저장)"}
          </span>
        </div>
      </main>

      {dialog.open && (
        <ConnectDialog
          initial={dialog.editing}
          onSaved={handleSaved}
          onClose={() => setDialog({ open: false })}
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

      {namePrompt === "save" && (
        <NamePrompt
          title="로그 저장"
          message="이 시점부터 1초 단위 기록을 지정한 이름으로 계속 저장합니다."
          defaultName={logName}
          onConfirm={doSaveLog}
          onCancel={() => setNamePrompt(null)}
        />
      )}

      {namePrompt === "exit" && (
        <NamePrompt
          title="종료 — 기록을 저장할까요?"
          message="저장하지 않으면 지금까지 기록된 임시 로그는 삭제됩니다."
          defaultName={logName || "session"}
          confirmLabel="저장하고 종료"
          showDiscard
          onConfirm={(name) => KeepLogAndQuit(name)}
          onDiscard={() => DiscardAndQuit()}
          onCancel={() => setNamePrompt(null)}
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
