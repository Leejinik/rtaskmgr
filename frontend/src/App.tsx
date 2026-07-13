import { useEffect, useRef, useState } from "react";
import { EventsOn } from "../wailsjs/runtime";
import {
  ListHosts, Connect, Disconnect, DeleteHost, SetInterval,
  ConnectMany, DisconnectMany,
  StartRecording, StopRecording,
  NethogsInstall, NethogsRollback,
  OpenLogDialog, LogFrames,
  KillProcess,
  ServiceAction,
  PwConfig, RenewPasswords,
  AutoUpdate, ApplyUpdate, GetPendingReleaseNotes, MarkReleaseNotesSeen,
} from "../wailsjs/go/main/App";

const REFRESH_OPTS = [1, 2, 3, 5, 10, 15, 20, 30, 60];
import { host, main, updater } from "../wailsjs/go/models";
import { Frame, HostStatus, Capabilities, SortKey, SortSpec, MAX_SORT, SysSample } from "./types";
import ProcTable from "./components/ProcTable";
import PerformanceView from "./components/PerformanceView";
import DetailModal from "./components/DetailModal";
import ConnectDialog from "./components/ConnectDialog";
import ClusterDialog from "./components/ClusterDialog";
import ClusterPasswordDialog from "./components/ClusterPasswordDialog";
import ClusterOverview from "./components/ClusterOverview";
import ContextMenu from "./components/ContextMenu";
import ConfirmDialog from "./components/ConfirmDialog";
import PlaybackBar from "./components/PlaybackBar";
import ScheduledModal from "./components/ScheduledModal";
import PasswordDialog from "./components/PasswordDialog";
import { fmtUptime, fmtClock } from "./format";
import { PwInfo, isUrgent, pwTooltip, expLabel } from "./pw";

interface Playback {
  meta: main.LogMeta;
  hostId: string;
  frames: Frame[];
  index: number;
  playing: boolean;
}

const basename = (p: string) => p.split(/[\\/]/).pop() || p;
import "./taskmgr.css";

// Module-level guard so the silent startup auto-update runs exactly once even
// if React StrictMode double-invokes the mount effect in dev.
let autoApplyFired = false;

export default function App() {
  const [hosts, setHosts] = useState<host.Host[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [frames, setFrames] = useState<Record<string, Frame>>({});
  const [status, setStatus] = useState<Record<string, HostStatus>>({});
  const [caps, setCaps] = useState<Record<string, Capabilities>>({});
  const [nethogs, setNethogs] = useState<Record<string, { active: boolean; installedByUs: boolean }>>({});
  const [nhBusy, setNhBusy] = useState<Record<string, boolean>>({});

  const [search, setSearch] = useState("");
  const [hideKthreads, setHideKthreads] = useState(false);
  const [topLevelOnly, setTopLevelOnly] = useState(false);
  const [view, setView] = useState<"proc" | "perf">("proc");
  const [sysHist, setSysHist] = useState<Record<string, SysSample[]>>({});
  const [theme, setTheme] = useState<"dark" | "light">(
    () => (localStorage.getItem("theme") === "light" ? "light" : "dark")
  );
  useEffect(() => {
    document.documentElement.classList.toggle("light", theme === "light");
    try { localStorage.setItem("theme", theme); } catch { /* ignore */ }
  }, [theme]);
  // Empty = the implicit default (CPU descending), which is always the lowest-
  // priority tiebreaker. Explicit sorts layer ON TOP of it, so clicking any
  // column visibly reorders while CPU-desc still breaks ties.
  const [sort, setSort] = useState<SortSpec[]>([]);
  const [selectedPid, setSelectedPid] = useState<number | null>(null);
  const [detailPid, setDetailPid] = useState<number | null>(null);
  const [playback, setPlayback] = useState<Playback | null>(null);

  const [dialog, setDialog] = useState<{ open: boolean; editing?: host.Host }>({ open: false });
  const [clusterDialogOpen, setClusterDialogOpen] = useState(false);
  const [clusterEdit, setClusterEdit] = useState<{ id: string; name: string; hosts: host.Host[] } | null>(null);
  const [clusterPwDialog, setClusterPwDialog] = useState<{ id: string; name: string } | null>(null);
  const [ctx, setCtx] = useState<{ x: number; y: number; h: host.Host } | null>(null);
  const [clusterCtx, setClusterCtx] = useState<{ x: number; y: number; id: string; name: string } | null>(null);
  // Right-click-on-process menu and the terminate confirmation it opens. hostId
  // is pinned to the row that was clicked (single-host OR a cluster column), so
  // the kill never resolves the target from whatever happens to be "selected".
  const [procCtx, setProcCtx] = useState<{ x: number; y: number; pid: number; name: string; hostId: string; service: string } | null>(null);
  const [killConfirm, setKillConfirm] = useState<{ pid: number; name: string; force: boolean; hostId: string } | null>(null);
  const [svcConfirm, setSvcConfirm] = useState<{ unit: string; name: string; action: "stop" | "restart"; hostId: string } | null>(null);
  // When set, the main area shows the cluster overview dashboard instead of a
  // single host. Selecting a host (row or overview card) clears it.
  const [overviewClusterId, setOverviewClusterId] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>(() => {
    try { return JSON.parse(localStorage.getItem("rtm.collapsed") || "{}"); } catch { return {}; }
  });
  function toggleCollapse(cid: string) {
    setCollapsed((prev) => {
      const next = { ...prev, [cid]: !prev[cid] };
      try { localStorage.setItem("rtm.collapsed", JSON.stringify(next)); } catch { /* ignore */ }
      return next;
    });
  }
  const [recording, setRecording] = useState<{ active: boolean; hostId: string; path: string }>(
    { active: false, hostId: "", path: "" }
  );
  const [schedOpen, setSchedOpen] = useState(false);
  // Managed-account (liz/root) password expiry, keyed by host id. Seeded from the
  // persisted host cache and refreshed live via the "pwstatus" event on connect.
  const [pwStatus, setPwStatus] = useState<Record<string, PwInfo>>({});
  const [pwWarnDays, setPwWarnDays] = useState(10);
  const [pwDismissed, setPwDismissed] = useState<Set<string>>(new Set());
  const [pwAlert, setPwAlert] = useState<{ hostId: string; name: string } | null>(null);
  const [pwDialog, setPwDialog] = useState<{ hostId: string; name: string } | null>(null);
  const [refreshSec, setRefreshSec] = useState<number>(
    () => Number(localStorage.getItem("rtm.interval")) || 10
  );
  const [toast, setToast] = useState("");

  // Auto-updater UI: a persistent manual-update pill (shown when the silent path
  // is blocked by the loop guard) and a one-shot release-notes popup carried
  // over from the version that just updated.
  const [manualUpdate, setManualUpdate] = useState<updater.UpdateInfo | null>(null);
  const [releaseNotes, setReleaseNotes] = useState<{ version: string; notes: string } | null>(null);

  const toastTimer = useRef<number | null>(null);
  const showToast = (m: string) => {
    setToast(m);
    if (toastTimer.current) window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast(""), 3000);
  };

  const refreshHosts = async () => {
    const h = await ListHosts().catch(() => [] as host.Host[]);
    setHosts(h ?? []);
    // Seed the expiry cache from each host's persisted last-known values so the
    // hover tooltip works before (re)connecting. Live "pwstatus" events override.
    setPwStatus((prev) => {
      const next = { ...prev };
      for (const x of h ?? []) {
        const liz = (x as any).lizExpDays ?? 0;
        const root = (x as any).rootExpDays ?? 0;
        if (!next[x.id] && (liz !== 0 || root !== 0)) {
          next[x.id] = { hasLiz: liz !== 0, hasRoot: root !== 0, lizExpDays: liz, rootExpDays: root };
        }
      }
      return next;
    });
    return h ?? [];
  };

  // Terminate a process on an explicit host (pinned by the row that was
  // right-clicked). Only reachable through the confirmation dialog. Before
  // signalling, re-check the host's latest frame: if the PID vanished or now
  // carries a different name, the row is stale — refuse rather than risk hitting
  // a recycled PID. (The exit→reuse race within the SSH round-trip is inherent
  // to any kill-by-PID tool and can't be fully closed; this catches the common
  // "already exited / changed" case.)
  async function doKill(t: { hostId: string; pid: number; name: string; force: boolean }) {
    if (!t.hostId) return;
    const live = frames[t.hostId]?.procs.find((p) => p.pid === t.pid);
    if (!live) {
      showToast(`PID ${t.pid} 프로세스가 이미 종료되었거나 목록에 없습니다`);
      return;
    }
    if (live.name !== t.name) {
      showToast(`PID ${t.pid}가 다른 프로세스로 바뀌었습니다 (${t.name} → ${live.name}). 다시 시도하세요`);
      return;
    }
    try {
      await KillProcess(t.hostId, t.pid, t.force);
      showToast(`PID ${t.pid} (${t.name}) ${t.force ? "강제 종료(SIGKILL)" : "종료(SIGTERM)"} 신호를 보냈습니다`);
    } catch (e: any) {
      showToast(typeof e === "string" ? e : e?.message ?? "프로세스 종료 실패");
    }
  }

  async function doService(t: { hostId: string; unit: string; name: string; action: "stop" | "restart" }) {
    if (!t.hostId || !t.unit) return;
    try {
      await ServiceAction(t.hostId, t.unit, t.action);
      showToast(`${t.unit} ${t.action === "restart" ? "재시작" : "정지"} 완료`);
    } catch (e: any) {
      showToast(typeof e === "string" ? e : e?.message ?? "서비스 동작 실패");
    }
  }

  useEffect(() => { void refreshHosts(); }, []);
  useEffect(() => { PwConfig().then((c) => setPwWarnDays(c.expiryWarnDays || 10)).catch(() => {}); }, []);

  // ---- Auto-update (silent startup path, run once) ----
  useEffect(() => {
    if (autoApplyFired) return;
    autoApplyFired = true;
    (async () => {
      // Pop the release notes stashed by the version that just updated (once).
      try {
        const notes = await GetPendingReleaseNotes();
        if (notes?.version) setReleaseNotes({ version: notes.version, notes: notes.notes ?? "" });
      } catch { /* ignore */ }
      // Guarded silent update. Applying → app quits & relaunches shortly.
      // Blocked (guard tripped) → offer a manual-update pill instead of looping.
      try {
        const r = await AutoUpdate();
        if (r?.applying) {
          showToast("업데이트 적용 중… " + (r.info?.latestVersion ?? ""));
        } else if (r?.blocked && r.info) {
          setManualUpdate(r.info);
        }
      } catch { /* ignore */ }
    })();
  }, []);

  const dismissReleaseNotes = () => {
    setReleaseNotes(null);
    MarkReleaseNotesSeen().catch(() => {});
  };

  const applyManualUpdate = async () => {
    if (!manualUpdate) return;
    try {
      showToast("업데이트 적용 중… " + (manualUpdate.latestVersion ?? ""));
      await ApplyUpdate(manualUpdate);
    } catch (e: any) {
      showToast(typeof e === "string" ? e : e?.message ?? "업데이트 적용 실패");
    }
  };

  // ---- backend events ----
  useEffect(() => {
    const offFrame = EventsOn("frame", (f: Frame) => {
      setFrames((prev) => ({ ...prev, [f.hostId]: f }));
      // Accumulate a rolling system-metrics history (no per-process rows) for the
      // performance charts. ~120 samples (2 min at 1s).
      setSysHist((prev) => {
        const arr = (prev[f.hostId] ?? []).slice(-119);
        arr.push({
          t: f.t, cpu: f.cpu, mem: f.mem, memTotal: f.memTotal, memUsed: f.memUsed,
          swapTotal: f.swapTotal, swapUsed: f.swapUsed,
          netRx: f.netRx, netTx: f.netTx, netSpeed: f.netSpeed,
          nets: f.nets, disks: f.disks,
        });
        return { ...prev, [f.hostId]: arr };
      });
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
    const offPw = EventsOn("pwstatus", (s: any) => {
      setPwStatus((prev) => ({
        ...prev,
        [s.hostId]: {
          hasLiz: !!s.hasLiz, hasRoot: !!s.hasRoot,
          lizExpDays: s.lizExpDays ?? 0, rootExpDays: s.rootExpDays ?? 0,
          todayDays: s.todayDays, err: s.err,
        },
      }));
    });
    const offRec = EventsOn("recording", (s: any) => {
      setRecording({ active: s.active, hostId: s.hostId || "", path: s.path || "" });
      if (s.auto) showToast("연결이 끊겨 실시간 기록을 자동 중지했습니다");
      else if (s.active) showToast(`기록 시작 — ${s.path}`);
      else if (s.path) showToast(`기록 종료 — ${s.path}`);
    });
    return () => { offFrame(); offStatus(); offNet(); offPw(); offRec(); };
  }, []);

  // Raise the expiry alert for the first urgent, not-yet-dismissed host. One at a
  // time; acting on it (Y/N) dismisses that host so the next can surface.
  useEffect(() => {
    if (pwAlert) return;
    for (const h of hosts) {
      const info = pwStatus[h.id];
      if (info && !pwDismissed.has(h.id) && isUrgent(info, pwWarnDays)) {
        setPwAlert({ hostId: h.id, name: h.name });
        break;
      }
    }
  }, [pwStatus, hosts, pwWarnDays, pwDismissed, pwAlert]);

  const dismissPw = (hostId: string) =>
    setPwDismissed((prev) => { const n = new Set(prev); n.add(hostId); return n; });

  async function alertRenew(hostId: string, name: string) {
    showToast(`${name}: 패스워드 만료일 갱신 중… (창을 닫지 마세요)`);
    try {
      await RenewPasswords(hostId);
      showToast(`${name}: 패스워드 만료일 갱신 완료`);
    } catch (e: any) {
      const msg = typeof e === "string" ? e : e?.message ?? "갱신 실패";
      showToast(`${name}: 갱신 실패 — ${msg}. 🔑 패스워드 관리에서 이력을 확인하세요`);
    }
  }

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
    setSysHist((prev) => { const n = { ...prev }; delete n[id]; return n; });
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
    setOverviewClusterId(null);
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

  // ---- cluster helpers ----
  function selectHost(id: string) {
    setOverviewClusterId(null);
    setSelectedId(id);
  }

  function openOverview(cid: string) {
    setSelectedId(null);
    setDetailPid(null);
    setOverviewClusterId(cid);
  }

  async function handleClusterSaved(saved: host.Host[], connect: boolean, clusterId?: string) {
    // When editing, delete any members that were removed (dropped from the rows).
    if (clusterEdit) {
      const keep = new Set(saved.map((h) => h.id));
      const removed = clusterEdit.hosts.filter((h) => !keep.has(h.id));
      if (removed.length) {
        disconnectCluster(removed.map((h) => h.id));
        for (const h of removed) await DeleteHost(h.id).catch(() => {});
      }
    }
    setClusterDialogOpen(false);
    setClusterEdit(null);
    await refreshHosts();
    const cid = clusterId ?? ((saved[0] as any)?.clusterId as string | undefined);
    if (cid) openOverview(cid);
    if (connect && saved.length) {
      const ids = saved.map((h) => h.id);
      setStatus((prev) => {
        const n = { ...prev };
        ids.forEach((id) => { n[id] = { state: "connecting", detail: "" }; });
        return n;
      });
      try {
        const res = await ConnectMany(ids, refreshSec);
        setCaps((prev) => {
          const n = { ...prev };
          (res ?? []).forEach((r) => { if (!r.err) n[r.hostId] = r.caps; });
          return n;
        });
        const failed = (res ?? []).filter((r) => r.err);
        if (failed.length) showToast(`${failed.length}대 연결 실패 — 카드에서 개별 재시도`);
      } catch (e: any) {
        showToast(`클러스터 연결 실패: ${e}`);
      }
    }
  }

  async function connectCluster(ids: string[]) {
    if (!ids.length) return;
    setStatus((prev) => {
      const n = { ...prev };
      ids.forEach((id) => { if (n[id]?.state !== "streaming") n[id] = { state: "connecting", detail: "" }; });
      return n;
    });
    try {
      const res = await ConnectMany(ids, refreshSec);
      setCaps((prev) => {
        const n = { ...prev };
        (res ?? []).forEach((r) => { if (!r.err) n[r.hostId] = r.caps; });
        return n;
      });
      const failed = (res ?? []).filter((r) => r.err);
      if (failed.length) showToast(`${failed.length}대 연결 실패`);
    } catch (e: any) {
      showToast(`연결 실패: ${e}`);
    }
  }

  function disconnectCluster(ids: string[]) {
    DisconnectMany(ids);
    ids.forEach((id) => {
      setFrames((prev) => { const n = { ...prev }; delete n[id]; return n; });
      setSysHist((prev) => { const n = { ...prev }; delete n[id]; return n; });
      setNethogs((prev) => { const n = { ...prev }; delete n[id]; return n; });
    });
  }

  async function handleClusterDelete(cid: string, name: string, ids: string[]) {
    if (!window.confirm(`클러스터 '${name}' 의 서버 ${ids.length}대를 모두 삭제할까요?`)) return;
    disconnectCluster(ids);
    for (const id of ids) {
      await DeleteHost(id).catch((e) => showToast(`삭제 실패: ${e}`));
    }
    await refreshHosts();
    if (overviewClusterId === cid) setOverviewClusterId(null);
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

  // Group hosts into named clusters (📦) + standalone. Order preserves ListHosts'
  // name sort; groups appear in first-seen order.
  const clusterGroups: { id: string; name: string; hosts: host.Host[] }[] = [];
  const standalone: host.Host[] = [];
  const groupIndex = new Map<string, number>();
  for (const h of hosts) {
    const cid = (h as any).clusterId as string | undefined;
    if (cid) {
      let idx = groupIndex.get(cid);
      if (idx === undefined) {
        idx = clusterGroups.length;
        groupIndex.set(cid, idx);
        clusterGroups.push({ id: cid, name: (h as any).clusterName || cid, hosts: [] });
      }
      clusterGroups[idx].hosts.push(h);
    } else {
      standalone.push(h);
    }
  }
  const overviewCluster = overviewClusterId
    ? clusterGroups.find((g) => g.id === overviewClusterId) ?? null
    : null;

  const selected = hosts.find((h) => h.id === selectedId) ?? null;
  const frame = selectedId ? frames[selectedId] : undefined;
  // While a process context menu or the terminate confirmation is open, hold the
  // table on a frozen snapshot so live re-sorting can't slide a different row
  // under the cursor between the right-click and pressing "종료" — otherwise the
  // user aims at one process and signals another. doKill still validates against
  // the LIVE frame, so a PID that truly exited/changed is caught regardless.
  const tableFrozen = procCtx !== null || killConfirm !== null || svcConfirm !== null;
  const frozenFrame = useRef<Frame | undefined>(undefined);
  if (!tableFrozen) frozenFrame.current = undefined;
  else if (frozenFrame.current === undefined) frozenFrame.current = frame;
  const tableFrame = tableFrozen ? (frozenFrame.current ?? frame) : frame;
  const st = selectedId ? status[selectedId] : undefined;
  const cap = selectedId ? caps[selectedId] : undefined;
  const connected = st?.state === "streaming" && !!frame;
  const detailProc =
    frame && detailPid != null ? frame.procs.find((p) => p.pid === detailPid) : undefined;
  const selectedClusterId = (selected as any)?.clusterId as string | undefined;

  // Open the process right-click menu via a document-level, capture-phase
  // listener rather than a React onContextMenu on the row. Wails production
  // builds can swallow the row's synthetic contextmenu event (it never reaches
  // React), but a capture-phase document listener fires reliably. We resolve the
  // row from data-pid, so re-sorting between hover and click doesn't matter — the
  // pid is whatever row the cursor is actually over. Live process view only.
  useEffect(() => {
    if (playback || !selected || !frame || view !== "proc") return;
    const hostId = selected.id;
    const onCtx = (e: MouseEvent) => {
      const tr = (e.target as HTMLElement)?.closest?.("tr[data-pid]") as HTMLElement | null;
      if (!tr) return; // right-click off a row → let it bubble and close any open menu
      const pid = Number(tr.dataset.pid);
      if (!Number.isFinite(pid)) return;
      e.preventDefault();
      e.stopPropagation(); // we own this event; don't let ContextMenu's close fire
      const name = tr.dataset.name || String(pid);
      const service = tr.dataset.service || "";
      setSelectedPid(pid);
      setProcCtx({ x: e.clientX, y: e.clientY, pid, name, hostId, service });
    };
    document.addEventListener("contextmenu", onCtx, true);
    return () => document.removeEventListener("contextmenu", onCtx, true);
  }, [playback, selected, frame, view]);

  // Sidebar right-click menus (host / cluster). Bound at the document capture
  // level for the same reason as the process rows — Wails production can swallow
  // a React onContextMenu — so the ⋯ buttons are gone and right-click is the only
  // trigger. Right-clicking empty space matches nothing and bubbles, closing any
  // open menu via ContextMenu's own window listener.
  useEffect(() => {
    const onCtx = (e: MouseEvent) => {
      const el = e.target as HTMLElement | null;
      const hostEl = el?.closest?.("[data-host-id]") as HTMLElement | null;
      if (hostEl) {
        const h = hosts.find((x) => x.id === hostEl.dataset.hostId);
        if (h) {
          e.preventDefault();
          e.stopPropagation();
          setClusterCtx(null);
          setCtx({ x: e.clientX, y: e.clientY, h });
        }
        return;
      }
      const clEl = el?.closest?.("[data-cluster-id]") as HTMLElement | null;
      if (clEl) {
        const cid = clEl.dataset.clusterId!;
        const member = hosts.find((x) => (x as any).clusterId === cid);
        if (member) {
          e.preventDefault();
          e.stopPropagation();
          setCtx(null);
          setClusterCtx({ x: e.clientX, y: e.clientY, id: cid, name: (member as any).clusterName || cid });
        }
      }
    };
    document.addEventListener("contextmenu", onCtx, true);
    return () => document.removeEventListener("contextmenu", onCtx, true);
  }, [hosts]);

  const renderHostItem = (h: host.Host, inCluster: boolean) => {
    const s = status[h.id]?.state;
    const pwUrgent = isUrgent(pwStatus[h.id], pwWarnDays);
    return (
      <div
        key={h.id}
        data-host-id={h.id}
        className={"host-item" + (selectedId === h.id ? " selected" : "") + (inCluster ? " in-cluster" : "")}
        title={pwTooltip(pwStatus[h.id])}
        onClick={() => selectHost(h.id)}
      >
        <div className="host-line1">
          <span className={`dot ${s ?? ""}`} />
          <span className="host-name">{h.name}</span>
          {pwUrgent && <span className="pw-warn-badge" title="패스워드 만료 임박">🔑</span>}
        </div>
        <div className="host-line2">{h.user}@{h.addr}</div>
      </div>
    );
  };

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
          <label style={{ display: "flex", alignItems: "center", gap: 4, fontSize: 12, whiteSpace: "nowrap" }}
            title="커널 스레드([대괄호] 프로세스) 숨기기">
            <input type="checkbox" checked={hideKthreads}
              onChange={(e) => setHideKthreads(e.target.checked)} />
            커널 스레드 숨김
          </label>
          <label style={{ display: "flex", alignItems: "center", gap: 4, fontSize: 12, whiteSpace: "nowrap" }}
            title="최상위 프로세스만 (부모 PID ≤ 1, 즉 systemd/커널 직속만)">
            <input type="checkbox" checked={topLevelOnly}
              onChange={(e) => setTopLevelOnly(e.target.checked)} />
            최상위만
          </label>
        </div>
        {pf ? (
          <ProcTable
            frame={pf}
            search={search}
            hideKthreads={hideKthreads}
            topLevelOnly={topLevelOnly}
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
          <div style={{ display: "flex", gap: 4 }}>
            <button className="toolbtn primary" onClick={() => setDialog({ open: true })}>
              + 호스트
            </button>
            <button className="toolbtn" onClick={() => setClusterDialogOpen(true)}>
              + 클러스터
            </button>
          </div>
        </div>
        <div className="sidebar-sub">RHEL8/9 SSH 대상</div>
        <div className="host-list">
          {hosts.length === 0 ? (
            <div className="sidebar-empty">
              저장된 호스트가 없습니다.<br />“+ 호스트” 또는 “+ 클러스터”로 등록하세요.
            </div>
          ) : (
            <>
              {clusterGroups.map((g) => {
                const ids = g.hosts.map((h) => h.id);
                const conn = g.hosts.filter((h) => status[h.id]?.state === "streaming").length;
                const isCollapsed = !!collapsed[g.id];
                const pwUrgentCluster = g.hosts.some((h) => isUrgent(pwStatus[h.id], pwWarnDays));
                const clusterPwTip = "패스워드 만료\n" + g.hosts.map((h) => {
                  const info = pwStatus[h.id];
                  const liz = info?.hasLiz ? expLabel(info.lizExpDays) : "-";
                  const root = info?.hasRoot ? expLabel(info.rootExpDays) : "-";
                  return `  ${h.name}: liz ${liz} / root ${root}`;
                }).join("\n");
                return (
                  <div key={g.id} className="cluster-group">
                    <div
                      data-cluster-id={g.id}
                      className={"cluster-head" + (overviewClusterId === g.id ? " selected" : "")}
                      title={clusterPwTip}
                      onClick={() => openOverview(g.id)}
                    >
                      <span
                        className="caret"
                        onClick={(e) => { e.stopPropagation(); toggleCollapse(g.id); }}
                        title={isCollapsed ? "펼치기" : "접기"}
                      >
                        {isCollapsed ? "▸" : "▾"}
                      </span>
                      <span className="cluster-icon">📦</span>
                      <span className="host-name">{g.name}</span>
                      {pwUrgentCluster && <span className="pw-warn-badge" title="패스워드 만료 임박">🔑</span>}
                      <span className="cluster-count">{conn}/{g.hosts.length}</span>
                    </div>
                    {!isCollapsed && g.hosts.map((h) => renderHostItem(h, true))}
                  </div>
                );
              })}
              {standalone.length > 0 && (
                <>
                  {clusterGroups.length > 0 && <div className="host-group-sep">개별 호스트</div>}
                  {standalone.map((h) => renderHostItem(h, false))}
                </>
              )}
            </>
          )}
        </div>
        <div className="sidebar-footer">
          {manualUpdate && (
            <button className="toolbtn"
              style={{
                width: "100%", marginBottom: 6,
                background: "#2f6f3f", borderColor: "#3c9553", color: "#fff",
              }}
              onClick={applyManualUpdate}
              title="새 버전을 지금 내려받아 적용합니다">
              ⬆️ 새 버전 {manualUpdate.latestVersion} (수동 업데이트)
            </button>
          )}
          <button className="toolbtn" style={{ width: "100%", marginBottom: 6 }}
            onClick={() => setTheme((t) => (t === "dark" ? "light" : "dark"))}
            title="다크/라이트 테마 전환">
            {theme === "dark" ? "☀️ 라이트 모드" : "🌙 다크 모드"}
          </button>
          <button className="toolbtn" style={{ width: "100%" }} onClick={openLog}>
            📂 로그 열기 (재생)
          </button>
        </div>
      </aside>

      {/* ---- main ---- */}
      {overviewCluster ? (
        <ClusterOverview
          clusterName={overviewCluster.name}
          hosts={overviewCluster.hosts}
          frames={frames}
          status={status}
          sysHist={sysHist}
          refreshSec={refreshSec}
          onOpenHost={selectHost}
          onConnectOne={handleConnect}
          onConnectAll={() => connectCluster(overviewCluster.hosts.map((h) => h.id))}
          onDisconnectAll={() => disconnectCluster(overviewCluster.hosts.map((h) => h.id))}
          onChangeInterval={changeInterval}
          onProcMenu={(hostId, pid, name, service, x, y) => setProcCtx({ x, y, pid, name, hostId, service })}
        />
      ) : (
      <main className="main">
        <div className="main-topbar">
          {selected ? (
            <>
              {selectedClusterId && (
                <button className="toolbtn" title="클러스터 요약으로 돌아가기"
                  onClick={() => openOverview(selectedClusterId)}>
                  ← 클러스터
                </button>
              )}
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
              {connected && (
                <div className="viewtabs">
                  <button className={"toolbtn" + (view === "proc" ? " primary" : "")}
                    onClick={() => setView("proc")}>프로세스</button>
                  <button className={"toolbtn" + (view === "perf" ? " primary" : "")}
                    onClick={() => setView("perf")}>성능</button>
                </div>
              )}
              {view === "proc" && (
                <>
                  <input
                    className="search"
                    placeholder="이름, 서비스 또는 PID로 검색"
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                  />
                  <label style={{ display: "flex", alignItems: "center", gap: 4, fontSize: 12, whiteSpace: "nowrap" }}
                    title="커널 스레드([대괄호] 프로세스) 숨기기">
                    <input type="checkbox" checked={hideKthreads}
                      onChange={(e) => setHideKthreads(e.target.checked)} />
                    커널 스레드 숨김
                  </label>
                  <label style={{ display: "flex", alignItems: "center", gap: 4, fontSize: 12, whiteSpace: "nowrap" }}
                    title="최상위 프로세스만 (부모 PID ≤ 1, 즉 systemd/커널 직속만)">
                    <input type="checkbox" checked={topLevelOnly}
                      onChange={(e) => setTopLevelOnly(e.target.checked)} />
                    최상위만
                  </label>
                </>
              )}
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

        {selected && frame && view === "proc" && (
          <ProcTable
            frame={tableFrame ?? frame}
            search={search}
            hideKthreads={hideKthreads}
            topLevelOnly={topLevelOnly}
            sort={sort}
            selectedPid={selectedPid}
            onSort={onSort}
            onSelect={setSelectedPid}
            onOpen={(pid) => setDetailPid(pid)}
          />
        )}
        {selected && frame && view === "perf" && (
          <PerformanceView frame={frame} samples={sysHist[selected.id] ?? []} />
        )}

        <div className="statusline">
          {frame && <span>{frame.ncpu} vCPU · 메모리 {(frame.memTotal / 1024 / 1024).toFixed(1)} GB</span>}
          {frame && <span>프로세스 {frame.procs.length}</span>}
          {tableFrozen && view === "proc" && (
            <span className="frozen" title="종료 대상을 고르는 동안 목록 순서가 고정됩니다">⏸ 목록 고정됨</span>
          )}
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
      )}

      {dialog.open && (
        <ConnectDialog
          initial={dialog.editing}
          onSaved={handleSaved}
          onClose={() => setDialog({ open: false })}
        />
      )}

      {(clusterDialogOpen || clusterEdit) && (
        <ClusterDialog
          editing={clusterEdit ?? undefined}
          onSaved={handleClusterSaved}
          onClose={() => { setClusterDialogOpen(false); setClusterEdit(null); }}
        />
      )}

      {clusterPwDialog && (() => {
        const g = clusterGroups.find((x) => x.id === clusterPwDialog.id);
        if (!g) return null;
        const connectedIds = new Set(g.hosts.filter((h) => status[h.id]?.state === "streaming").map((h) => h.id));
        return (
          <ClusterPasswordDialog
            clusterName={g.name}
            hosts={g.hosts}
            connectedIds={connectedIds}
            pwStatus={pwStatus}
            onClose={() => setClusterPwDialog(null)}
            onToast={showToast}
            onChanged={() => { void refreshHosts(); }}
          />
        );
      })()}

      {clusterCtx && (
        <ContextMenu
          x={clusterCtx.x}
          y={clusterCtx.y}
          items={[
            {
              label: "🔑 클러스터 패스워드 관리",
              onClick: () => setClusterPwDialog({ id: clusterCtx.id, name: clusterCtx.name }),
            },
            {
              label: "클러스터 편집",
              onClick: () => {
                const g = clusterGroups.find((x) => x.id === clusterCtx.id);
                if (g) setClusterEdit({ id: g.id, name: g.name, hosts: g.hosts });
              },
            },
            {
              label: "클러스터 삭제", danger: true,
              onClick: () => {
                const g = clusterGroups.find((x) => x.id === clusterCtx.id);
                if (g) handleClusterDelete(g.id, g.name, g.hosts.map((h) => h.id));
              },
            },
          ]}
          onClose={() => setClusterCtx(null)}
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
            { label: "🔑 패스워드 관리", onClick: () => setPwDialog({ hostId: ctx.h.id, name: ctx.h.name }) },
            { label: "편집", onClick: () => setDialog({ open: true, editing: ctx.h }) },
            { label: "삭제", danger: true, onClick: () => handleDelete(ctx.h.id) },
          ]}
          onClose={() => setCtx(null)}
        />
      )}

      {procCtx && (
        <ContextMenu
          x={procCtx.x}
          y={procCtx.y}
          items={[
            {
              label: "프로세스 끝내기 (SIGTERM)",
              danger: true,
              onClick: () => setKillConfirm({ pid: procCtx.pid, name: procCtx.name, force: false, hostId: procCtx.hostId }),
            },
            {
              label: "강제 종료 (SIGKILL)",
              danger: true,
              onClick: () => setKillConfirm({ pid: procCtx.pid, name: procCtx.name, force: true, hostId: procCtx.hostId }),
            },
            ...(procCtx.service && procCtx.service.endsWith(".service")
              ? [
                  {
                    label: `서비스 재시작 (${procCtx.service})`,
                    onClick: () => setSvcConfirm({ unit: procCtx.service, name: procCtx.name, action: "restart", hostId: procCtx.hostId }),
                  },
                  {
                    label: `서비스 정지 (${procCtx.service})`,
                    danger: true,
                    onClick: () => setSvcConfirm({ unit: procCtx.service, name: procCtx.name, action: "stop", hostId: procCtx.hostId }),
                  },
                ]
              : []),
          ]}
          onClose={() => setProcCtx(null)}
        />
      )}

      {killConfirm && (() => {
        // Look up the live process so we can show how long it has been running.
        // A PID that just got recycled reads as a few seconds — the cue the user
        // needs to tell "still the same process" from "already died & reused".
        const lp = frames[killConfirm.hostId]?.procs.find((p) => p.pid === killConfirm.pid);
        const ft = frames[killConfirm.hostId]?.t ?? 0;
        const hasUptime = !!lp && lp.start > 0 && ft > 0;
        const uptimeSec = hasUptime ? ft / 1000 - lp!.start : NaN;
        return (
        <ConfirmDialog
          title={killConfirm.force ? "강제 종료(SIGKILL)" : "프로세스 종료"}
          danger
          confirmLabel={killConfirm.force ? "강제 종료" : "종료"}
          message={
            <>
              정말 종료하시겠습니까?
              <div style={{ marginTop: 10, fontWeight: 600 }}>
                {killConfirm.name} <span style={{ color: "var(--text-mute)" }}>(PID {killConfirm.pid})</span>
              </div>
              <div style={{ marginTop: 6, fontSize: 12.5 }}>
                실행 시간:{" "}
                {hasUptime ? (
                  <>
                    <b style={{ color: "var(--accent)" }}>{fmtUptime(uptimeSec)}</b>
                    <span style={{ color: "var(--text-mute)" }}> · 시작 {fmtClock(lp!.start * 1000)}</span>
                  </>
                ) : (
                  <span style={{ color: "var(--text-mute)" }}>알 수 없음</span>
                )}
              </div>
              <div style={{ marginTop: 8, fontSize: 12.5, color: "var(--text-mute)" }}>
                {killConfirm.force
                  ? "SIGKILL은 프로세스가 정리 없이 즉시 강제 종료됩니다. 저장되지 않은 작업이 손실될 수 있습니다."
                  : "저장되지 않은 작업이 손실될 수 있습니다."}
              </div>
            </>
          }
          onCancel={() => setKillConfirm(null)}
          onConfirm={() => {
            void doKill(killConfirm);
            setKillConfirm(null);
          }}
        />
        );
      })()}

      {svcConfirm && (
        <ConfirmDialog
          title={svcConfirm.action === "restart" ? "서비스 재시작" : "서비스 정지"}
          danger
          confirmLabel={svcConfirm.action === "restart" ? "재시작" : "정지"}
          message={
            <>
              {svcConfirm.action === "restart" ? "이 서비스를 재시작하시겠습니까?" : "이 서비스를 정지하시겠습니까?"}
              <div style={{ marginTop: 10, fontWeight: 600 }}>{svcConfirm.unit}</div>
              <div style={{ marginTop: 6, fontSize: 12.5, color: "var(--text-mute)" }}>
                {svcConfirm.name} · systemctl {svcConfirm.action} {svcConfirm.unit}
              </div>
              <div style={{ marginTop: 8, fontSize: 12.5, color: "var(--text-mute)" }}>
                {svcConfirm.action === "stop"
                  ? "이 서비스(관련 프로세스 전체)가 중지됩니다."
                  : "서비스가 잠시 중단됐다가 다시 시작됩니다."}
              </div>
            </>
          }
          onCancel={() => setSvcConfirm(null)}
          onConfirm={() => {
            void doService(svcConfirm);
            setSvcConfirm(null);
          }}
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

      {pwDialog && (
        <PasswordDialog
          hostId={pwDialog.hostId}
          hostName={pwDialog.name}
          connected={status[pwDialog.hostId]?.state === "streaming"}
          info={pwStatus[pwDialog.hostId]}
          currentPassword={hosts.find((h) => h.id === pwDialog.hostId)?.password ?? ""}
          onClose={() => setPwDialog(null)}
          onToast={showToast}
          onChanged={() => { void refreshHosts(); }}
        />
      )}

      {pwAlert && (() => {
        const info = pwStatus[pwAlert.hostId];
        return (
          <ConfirmDialog
            title="🔑 패스워드 만료 임박"
            confirmLabel="갱신"
            cancelLabel="무시"
            message={
              <>
                <div style={{ fontWeight: 600, marginBottom: 8 }}>{pwAlert.name}</div>
                <div style={{ fontSize: 12.5, lineHeight: 1.7 }}>
                  {info?.hasLiz && <div>liz: <b>{expLabel(info.lizExpDays)}</b></div>}
                  {info?.hasRoot && <div>root: <b>{expLabel(info.rootExpDays)}</b></div>}
                </div>
                <div style={{ marginTop: 10, fontSize: 12.5, color: "var(--text-mute)" }}>
                  Y: 현재 패스워드를 유지한 채 만료일만 갱신합니다 (현재→임시→현재).<br />
                  다른 패스워드로 바꾸려면 🔑 패스워드 관리를 이용하세요.
                </div>
              </>
            }
            onCancel={() => { dismissPw(pwAlert.hostId); setPwAlert(null); }}
            onConfirm={() => {
              const { hostId, name } = pwAlert;
              dismissPw(hostId);
              setPwAlert(null);
              void alertRenew(hostId, name);
            }}
          />
        );
      })()}

      {toast && <div className="toast">{toast}</div>}

      {releaseNotes && (
        <div
          onClick={dismissReleaseNotes}
          style={{
            position: "fixed", inset: 0, zIndex: 1000,
            background: "rgba(0,0,0,0.5)",
            display: "flex", alignItems: "center", justifyContent: "center",
          }}
        >
          <div
            onClick={(e) => e.stopPropagation()}
            style={{
              width: "min(560px, 90vw)", maxHeight: "80vh", overflow: "auto",
              background: "var(--panel, #262626)", color: "var(--fg, #eee)",
              border: "1px solid #444", borderRadius: 8, padding: "18px 20px",
              boxShadow: "0 8px 32px rgba(0,0,0,0.4)",
            }}
          >
            <div style={{ fontSize: 16, fontWeight: 600, marginBottom: 10 }}>
              🎉 rtaskmgr {releaseNotes.version} 업데이트됨
            </div>
            <pre style={{
              whiteSpace: "pre-wrap", wordBreak: "break-word", margin: 0,
              fontFamily: "inherit", fontSize: 13, lineHeight: 1.5,
            }}>
              {releaseNotes.notes || "이 버전의 릴리스 노트가 없습니다."}
            </pre>
            <div style={{ textAlign: "right", marginTop: 16 }}>
              <button className="toolbtn primary" onClick={dismissReleaseNotes}>
                확인
              </button>
            </div>
          </div>
        </div>
      )}
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
