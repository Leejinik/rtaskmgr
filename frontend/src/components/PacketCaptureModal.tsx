import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { monitor } from "../../wailsjs/go/models";
import {
  CaptureEnv, ListCaptures, StartCapture, StopCapture, StopCaptureForce,
  DeleteCapture, ForgetCapture, OpenCaptureFolder, OpenInWireshark, LocalOperator,
} from "../../wailsjs/go/main/App";
import { EventsOn } from "../../wailsjs/runtime";
import { Frame } from "../types";
import { parsePorts, bpfOf, parseHosts, bpfOfHosts, combinedBpf } from "../ports";
import { useEsc } from "../useEsc";
import ConfirmDialog from "./ConfirmDialog";
import TargetPicker, { defaultTarget, targetUsable } from "./TargetPicker";

interface Props {
  hostId: string;
  hostName: string;
  // The live frame drives the "how long until the size cap" estimate — the one
  // number that stops an operator from starting a 4-second capture on a busy link.
  frame: Frame | null;
  connected: boolean;
  // An in-flight download, owned by App so progress survives this modal closing.
  dl: { hostId: string; id: string; pct: number } | null;
  onDownload: (cm: monitor.CapMeta) => void;
  onClose: () => void;
  onToast: (m: string) => void;
}

const MAX_MIN = 240; // 4 hours — see MaxCaptureSeconds
const MAX_MB = 10240;

const fmtSize = (b: number) => {
  if (b < 0) return "—";
  if (b < 1024) return `${b} B`;
  const kb = b / 1024;
  if (kb < 1024) return `${kb.toFixed(0)} KB`;
  const mb = kb / 1024;
  if (mb < 1024) return `${mb.toFixed(1)} MB`;
  return `${(mb / 1024).toFixed(2)} GB`;
};
const fmtTime = (t: number) => (t > 0 ? new Date(t).toLocaleString("ko-KR", { hour12: false }) : "—");
const fmtNum = (n: number) => (n < 0 ? "—" : n.toLocaleString("ko-KR"));

// Durations under two minutes are shown in seconds: rounding 4 seconds up to
// "1분" hides exactly the case the estimate exists to warn about.
const fmtDur = (sec: number) => {
  if (!Number.isFinite(sec) || sec <= 0) return "—";
  if (sec < 120) return `${sec < 10 ? sec.toFixed(1) : Math.round(sec)}초`;
  const m = Math.floor(sec / 60);
  if (m < 120) return `${m}분`;
  const h = Math.floor(m / 60);
  return `${h}시간 ${m % 60}분`;
};

// How a capture ended → label, icon, colour. "interrupted" and "orphan" are the
// ones that must stand out: the first means the file is truncated, the second that
// a tcpdump is still holding it.
const STATUS_UI: Record<string, { label: string; icon: string; cls: string }> = {
  running:      { label: "캡쳐 중",           icon: "●", cls: "pcap-good" },
  stopping:     { label: "중지 중…",          icon: "◐", cls: "pcap-warn" },
  stopped:      { label: "사용자 중지",       icon: "⏹", cls: "pcap-dim" },
  deadline:     { label: "최대 시간 도달",    icon: "■", cls: "pcap-dim" },
  maxsize:      { label: "최대 용량 도달",    icon: "■", cls: "pcap-warn" },
  maxpackets:   { label: "최대 패킷 수 도달", icon: "■", cls: "pcap-dim" },
  "low-disk":   { label: "디스크 여유 부족",  icon: "⚠", cls: "pcap-warn" },
  interrupted:  { label: "비정상 종료",       icon: "⚠", cls: "pcap-bad" },
  orphan:       { label: "고아 프로세스 잔존", icon: "⚠", cls: "pcap-bad" },
  error:        { label: "실행 실패",         icon: "✖", cls: "pcap-bad" },
  done:         { label: "정상 종료",         icon: "■", cls: "pcap-dim" },
};
const statusUI = (s: string) => STATUS_UI[s] ?? { label: s || "완료", icon: "■", cls: "pcap-dim" };

const isLive = (s: string) => s === "running" || s === "stopping";

const SNAPLENS = [
  { v: 0, label: "전체" },
  { v: 256, label: "256B" },
  { v: 96, label: "헤더만 (96B)" },
];

export default function PacketCaptureModal({
  hostId, hostName, frame, connected, dl, onDownload, onClose, onToast,
}: Props) {
  const [list, setList] = useState<monitor.CapMeta[]>([]);
  const [env, setEnv] = useState<monitor.CapEnv | null>(null);
  const [envBusy, setEnvBusy] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [info, setInfo] = useState("");
  const [me, setMe] = useState("");

  // Form
  const [iface, setIface] = useState("");
  const [ports, setPorts] = useState("");
  const [hosts, setHosts] = useState("");
  const [noFilter, setNoFilter] = useState(false);
  const [maxMin, setMaxMin] = useState(10);
  const [maxMB, setMaxMB] = useState(512);
  const [minFreeMB, setMinFreeMB] = useState(1024);
  const [snapLen, setSnapLen] = useState(0);
  const [maxPackets, setMaxPackets] = useState(0);
  const [promisc, setPromisc] = useState(false);
  const [target, setTarget] = useState("");

  // Dialogs stacked on top of this modal.
  const [ask, setAsk] = useState<monitor.CapMeta | null>(null);      // download after stop
  const [delAsk, setDelAsk] = useState<monitor.CapMeta | null>(null);
  const [forceAsk, setForceAsk] = useState<monitor.CapMeta | null>(null);
  const [forgetAsk, setForgetAsk] = useState<monitor.CapMeta | null>(null);

  // Freshness. A silently failing poll would otherwise let "elapsed 00:41:07"
  // tick upward forever on a host that is long gone.
  const [lastOkAt, setLastOkAt] = useState(0);
  const [fails, setFails] = useState(0);
  const [now, setNow] = useState(Date.now());

  const wasLive = useRef<Set<string>>(new Set());
  const prompted = useRef<Set<string>>(new Set());
  // A completion prompt that arrives while the operator is already answering a
  // confirmation waits its turn. Two ConfirmDialogs mounted at once both bind Y/N
  // on window, so one keypress would actuate both — and the auto-raised one paints
  // UNDERNEATH the one being read, so the operator would be answering a question
  // they cannot see (a delete they meant plus an unrequested Save As for a
  // different capture).
  const pendingAsk = useRef<monitor.CapMeta[]>([]);
  const dialogOpenRef = useRef(false);
  const stale = fails >= 3 || !connected;

  // Esc closes this modal — but not while a ConfirmDialog owns the keyboard, or
  // one press would dismiss both.
  useEsc(onClose, !ask && !delAsk && !forceAsk && !forgetAsk);

  async function refresh(quiet = false) {
    try {
      const l = (await ListCaptures(hostId)) ?? [];
      setList(l);
      setLastOkAt(Date.now());
      setFails(0);
      // One path for every ending: a user stop, a stop that needed longer to
      // flush, and a self-stop (deadline/maxsize/low-disk) all land here.
      for (const cm of l) {
        if (isLive(cm.status)) {
          wasLive.current.add(cm.id);
        } else if (wasLive.current.has(cm.id) && !prompted.current.has(cm.id)) {
          prompted.current.add(cm.id);
          wasLive.current.delete(cm.id);
          if (dialogOpenRef.current) pendingAsk.current.push(cm);
          else setAsk(cm);
        }
      }
      return l;
    } catch (e: any) {
      setFails((n) => n + 1);
      if (!quiet) setErr(String(e));
      return null;
    }
  }

  async function loadEnv() {
    setEnvBusy(true);
    try {
      const e = await CaptureEnv(hostId);
      setEnv(e);
      const def = defaultTarget(e.targets ?? [], "pcap");
      setTarget((t) => t || (def ? def.path : ""));
      setIface((cur) => cur || pickDefaultNic(e.nics ?? []));
    } catch (e: any) {
      setErr(String(e));
    } finally {
      setEnvBusy(false);
    }
  }

  // The list first: the commonest reason to open this modal is "check the capture
  // I left running", so it must not wait behind an environment probe.
  useEffect(() => {
    void (async () => {
      await refresh();
      await loadEnv();
    })();
    LocalOperator().then(setMe).catch(() => { /* cosmetic only */ });
  }, [hostId]);

  // Poll fast while something is live, slowly otherwise. Transitions also arrive
  // as "capture" events; this only keeps size/elapsed honest.
  const anyLive = list.some((c) => isLive(c.status));
  useEffect(() => {
    if (!connected) return;
    const ms = anyLive ? 3000 : 12000;
    const t = window.setInterval(() => void refresh(true), ms);
    return () => window.clearInterval(t);
  }, [hostId, anyLive, connected]);

  useEffect(() => {
    const off = EventsOn("capture", (p: any) => {
      if (p?.hostId !== hostId) return;
      void refresh(true);
    });
    return () => off();
  }, [hostId]);

  // Elapsed ticker, frozen while the data is stale.
  useEffect(() => {
    if (stale) return;
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [stale]);

  // One confirmation at a time: mirror the open state into a ref (the background
  // poll's closure cannot see the current state) and raise any queued completion
  // prompt as soon as the screen is clear.
  useEffect(() => {
    dialogOpenRef.current = !!(ask || delAsk || forceAsk || forgetAsk);
    if (dialogOpenRef.current) return;
    const next = pendingAsk.current.shift();
    if (next) setAsk(next);
  }, [ask, delAsk, forceAsk, forgetAsk]);

  // ---- derived: the filter preview ------------------------------------
  const parsed = useMemo(() => parsePorts(ports), [ports]);
  const parsedHosts = useMemo(() => parseHosts(hosts), [hosts]);
  const portBpf = parsed.spec ? bpfOf(parsed.spec) : "";
  const hostBpf = parsedHosts.spec ? bpfOfHosts(parsedHosts.spec) : "";
  const bpf = combinedBpf(hostBpf, portBpf);
  const allPorts = !!parsed.spec?.all;
  const allHosts = !!parsedHosts.spec?.all;
  const noTerms = allPorts && allHosts;
  const filterErr = parsedHosts.err || parsed.err;

  // ---- derived: how long until the size cap ---------------------------
  const nic = env?.nics?.find((n) => n.name === iface);
  const anyIface = iface === "any" || iface === "";
  const liveBps = (() => {
    if (!frame) return 0;
    if (anyIface) return Math.max(0, frame.netRx + frame.netTx);
    const n = frame.nets?.find((x) => x.name === iface);
    return n ? Math.max(0, n.rxBps + n.txBps) : 0;
  })();
  const linkBps = nic && nic.speedMb > 0 ? (nic.speedMb * 1e6) / 8 : 0;
  const capBytes = maxMB * 1024 * 1024;
  const secToCap = liveBps > 0 ? capBytes / liveBps : linkBps > 0 ? capBytes / linkBps : 0;
  const estFromLink = liveBps <= 0 && linkBps > 0;
  const maxSec = Math.max(1, Math.min(MAX_MIN, maxMin)) * 60;
  const tooFast = secToCap > 0 && secToCap < maxSec * 0.1;

  const bonded = (env?.nics ?? []).filter((n) => n.master);
  const bondMasters = [...new Set(bonded.map((n) => n.master))];

  const sel = env?.targets?.find((t) => t.path === target);
  // The "already capturing" gate reads the LIST, not env.running: env is only
  // refreshed on demand, so after a self-stop it would keep the start button
  // disabled until the operator pressed "환경 다시 확인".
  const canStart =
    !busy && connected && !!env?.elevated && !!env?.tcpdump &&
    !anyLive && !!sel && targetUsable(sel) &&
    !filterErr && (!noTerms || noFilter) && !!iface;

  async function start() {
    if (filterErr) { setErr(filterErr); return; }
    if (noTerms && !noFilter) {
      setErr("호스트도 포트도 지정하지 않았습니다. 전체 트래픽을 캡쳐하려면 '필터 없이 진행'을 켜세요");
      return;
    }
    if (!sel) { setErr("저장 위치를 선택하세요"); return; }
    setBusy(true); setErr(""); setInfo("");
    try {
      const cm = await StartCapture(hostId, new monitor.CapRequest({
        iface, portSpec: ports, hostSpec: hosts, targetDir: sel.path,
        maxSec, maxMB, minFreeMB, maxPackets, snapLen, promisc, noFilter,
      }));
      wasLive.current.add(cm.id);
      prompted.current.delete(cm.id);
      setInfo(`캡쳐를 시작했습니다 — ${iface} · ${bpf || "필터 없음"}`);
      await refresh();
      await loadEnv();
    } catch (e: any) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  }

  async function stop(cm: monitor.CapMeta) {
    setBusy(true); setErr("");
    try {
      const res = await StopCapture(hostId, cm.id);
      if (res.status === "stopping") {
        setInfo("중지 신호를 보냈습니다. 파일을 마무리하는 중입니다 — 끝나면 알려드립니다.");
      }
      await refresh();
    } catch (e: any) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  }

  async function forceStop(cm: monitor.CapMeta) {
    setBusy(true); setErr("");
    try {
      await StopCaptureForce(hostId, cm.id);
      await refresh();
    } catch (e: any) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  }

  async function remove(cm: monitor.CapMeta) {
    setBusy(true); setErr("");
    try {
      await DeleteCapture(hostId, cm.id);
      await refresh();
      await loadEnv();
    } catch (e: any) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  }

  async function forget(cm: monitor.CapMeta) {
    setBusy(true); setErr("");
    try {
      await ForgetCapture(hostId, cm.id);
      wasLive.current.delete(cm.id);
      prompted.current.delete(cm.id);
      setInfo("목록에서 제외했습니다. pcap 파일은 서버에 그대로 남아 있습니다.");
      await refresh();
      await loadEnv();
    } catch (e: any) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  }

  const notMine = (cm: monitor.CapMeta) => !!cm.startedBy && !!me && cm.startedBy !== me;

  return (
    <>
      <div className="scrim" onMouseDown={onClose}>
        <div className="modal" onMouseDown={(e) => e.stopPropagation()} style={{ minWidth: 700, maxWidth: 900 }}>
          <h2>📡 패킷 캡쳐 — {hostName}</h2>
          <div className="pcap-dim" style={{ fontSize: 12, marginBottom: 12 }}>
            서버에서 detached로 tcpdump를 실행합니다. 이 앱을 닫아도 계속 캡쳐하며,
            <b> 최대 시간 · 최대 용량 · 최소 여유 디스크</b> 세 가지 중 하나라도 걸리면 스스로 멈춥니다.
            멈춘 파일은 언제든 이 목록에서 내려받을 수 있습니다.
          </div>

          {stale && (
            <div className="pcap-warn" style={{ fontSize: 12, marginBottom: 8 }}>
              ⚠ 연결 끊김 — {lastOkAt > 0 ? `${Math.round((Date.now() - lastOkAt) / 1000)}초 전 정보` : "정보를 가져오지 못했습니다"}
              입니다. 서버의 캡쳐는 계속 진행 중일 수 있습니다.
            </div>
          )}

          {/* ---------------- new capture ---------------- */}
          <div className="pcap-box">
            <div className="pcap-head">
              <span>새 캡쳐</span>
              <button className="toolbtn" onClick={() => void loadEnv()} disabled={envBusy || busy}>
                {envBusy ? "확인 중…" : "환경 다시 확인"}
              </button>
            </div>

            {env && !env.elevated && (
              <div className="pcap-bad" style={{ fontSize: 12, marginBottom: 8 }}>
                이 호스트에서 root/sudo 권한을 얻지 못했습니다. 패킷 캡쳐를 시작할 수 없습니다.
              </div>
            )}
            {env && env.elevated && !env.tcpdump && (
              <div className="pcap-bad" style={{ fontSize: 12, marginBottom: 8 }}>
                이 호스트에 tcpdump가 없습니다. 설치 후 다시 시도하세요 (이 앱은 tcpdump를 설치하지 않습니다).
              </div>
            )}
            {env?.tcpdump && (
              <div className="pcap-dim" style={{ fontSize: 11, marginBottom: 8 }}>
                <span className="pcap-mono">{env.tcpdumpPath}</span>
                {env.version ? ` · ${env.version}` : ""}
              </div>
            )}

            {/* NIC — radio rows, not a <select>: .modal select has no dark styling
                and a native dropdown flashes white on the dark theme. */}
            <label className="pcap-dim" style={{ fontSize: 12, display: "block", margin: "4px 0" }}>
              캡쳐할 인터페이스
            </label>
            <div className="pcap-nics">
              {(env?.nics ?? []).map((n) => {
                const live = frame?.nets?.find((x) => x.name === n.name);
                return (
                  <label key={n.name} className={"pcap-nic" + (iface === n.name ? " sel" : "") + (n.up ? "" : " down")}>
                    <input type="radio" name="pcapnic" checked={iface === n.name}
                      onChange={() => setIface(n.name)} />
                    <b className="pcap-mono" style={{ minWidth: 92 }}>{n.name}</b>
                    {n.name === "any" ? (
                      <span className="pcap-dim">전체 인터페이스</span>
                    ) : (
                      <>
                        <span className={n.up ? "pcap-good" : "pcap-dim"}>{n.up ? "up" : "down"}</span>
                        {n.speedMb > 0 && <span className="pcap-dim">{n.speedMb} Mb/s</span>}
                        {n.ipv4 && <span className="pcap-mono pcap-dim">{n.ipv4}</span>}
                        {n.mac && <span className="pcap-mono pcap-dim" style={{ fontSize: 11 }}>{n.mac}</span>}
                        {n.master && <span className="pcap-badge">{n.master} 슬레이브</span>}
                        {n.isMaster && <span className="pcap-badge pcap-good">본딩/브리지 마스터</span>}
                        {n.loopback && <span className="pcap-badge">loopback</span>}
                        {n.virtual && !n.loopback && <span className="pcap-badge">가상</span>}
                      </>
                    )}
                    {live && (
                      <span className="pcap-dim" style={{ marginLeft: "auto", fontSize: 11 }}>
                        ↓{fmtSize(live.rxBps)}/s ↑{fmtSize(live.txBps)}/s
                      </span>
                    )}
                  </label>
                );
              })}
            </div>
            {anyIface && (
              <div className="pcap-dim" style={{ fontSize: 11, marginTop: 4, lineHeight: 1.6 }}>
                전체(-i any)는 링크 계층이 Linux cooked(SLL2)로 기록되어 MAC 주소가 남지 않고,
                파일을 받는 분의 Wireshark가 3.6 이상이어야 열립니다.
                {bondMasters.length > 0 && (
                  <div className="pcap-warn">
                    이 호스트는 본딩({bondMasters.join(", ")}) 구성입니다. any는 슬레이브에서도 같은 패킷을
                    잡아 파일이 2배가 되고 분석에서 중복/재전송으로 오인됩니다 — {bondMasters[0]}를 선택하세요.
                  </div>
                )}
              </div>
            )}
            {!anyIface && nic && !nic.up && (
              <div className="pcap-warn" style={{ fontSize: 11, marginTop: 4 }}>
                이 인터페이스는 down 상태입니다. 패킷이 잡히지 않을 수 있습니다.
              </div>
            )}

            {/* Filter: host and port, the two halves of `host X and port Y` */}
            <div className="row2" style={{ marginTop: 10, alignItems: "flex-start" }}>
              <div>
                <label>호스트 / IP (쉼표·공백 구분, 대역은 10.0.0.0/24)</label>
                <input type="text" value={hosts} placeholder="예: 10.0.0.5, 10.1.0.0/24, db1.example.com"
                  onChange={(e) => setHosts(e.target.value)} />
              </div>
              <div>
                <label>포트 (쉼표·공백 구분, 범위는 8080-8090)</label>
                <input type="text" value={ports} placeholder="예: 3306, 9092, 8080-8090"
                  onChange={(e) => setPorts(e.target.value)} />
              </div>
              <div style={{ flex: 1.4 }}>
                <label>패킷 크기 (스냅렌)</label>
                <div style={{ display: "flex", gap: 4 }}>
                  {SNAPLENS.map((s) => (
                    <button key={s.v} className={"toolbtn" + (snapLen === s.v ? " primary" : "")}
                      onClick={() => setSnapLen(s.v)} style={{ flex: 1, fontSize: 11 }}>
                      {s.label}
                    </button>
                  ))}
                </div>
              </div>
            </div>

            {/* The compiled filter, shown exactly as tcpdump will receive it. */}
            <div className="pcap-preview" style={{ minHeight: 40 }}>
              {filterErr ? (
                <span className="pcap-bad">✖ {filterErr}</span>
              ) : noTerms ? (
                <span className="pcap-warn">
                  필터 없음 — 이 인터페이스의 모든 트래픽을 캡쳐합니다
                </span>
              ) : (
                <span className="pcap-dim">
                  필터: <span className="pcap-mono" style={{ color: "var(--text)" }}>{bpf}</span>
                </span>
              )}
              <div className="pcap-dim" style={{ fontSize: 11 }}>
                {snapLen === 96
                  ? "헤더만 저장 (-s 96) — 용량이 크게 줄지만 페이로드/스트림 추적은 불가합니다."
                  : snapLen === 256
                  ? "앞 256바이트만 저장합니다 (-s 256)."
                  : "패킷 전체를 저장합니다 (-s 0)."}
              </div>
            </div>

            {noTerms && (
              <label className="check" style={{ fontSize: 12 }}>
                <input type="checkbox" checked={noFilter} onChange={(e) => setNoFilter(e.target.checked)} />
                필터 없이 진행 (호스트·포트 없이 전체 트래픽 캡쳐)
              </label>
            )}

            {/* Guards */}
            <div className="row2" style={{ marginTop: 8 }}>
              <div>
                <label>최대 시간 (분, 1–{MAX_MIN})</label>
                <input type="number" min={1} max={MAX_MIN} value={maxMin}
                  onChange={(e) => setMaxMin(Math.max(1, Math.min(MAX_MIN, Number(e.target.value) || 1)))} />
              </div>
              <div>
                <label>최대 용량 (MB, ≤{MAX_MB})</label>
                <input type="number" min={1} max={MAX_MB} value={maxMB}
                  onChange={(e) => setMaxMB(Math.max(1, Math.min(MAX_MB, Number(e.target.value) || 1)))} />
              </div>
              <div>
                <label>최소 여유 디스크 (MB, ≥256)</label>
                <input type="number" min={256} value={minFreeMB}
                  onChange={(e) => setMinFreeMB(Math.max(256, Number(e.target.value) || 256))} />
              </div>
              <div>
                <label>최대 패킷 수 (0=무제한)</label>
                <input type="number" min={0} value={maxPackets}
                  onChange={(e) => setMaxPackets(Math.max(0, Number(e.target.value) || 0))} />
              </div>
            </div>

            <div className="pcap-preview" style={{ minHeight: 40 }}>
              {secToCap > 0 ? (
                <span className={tooFast ? "pcap-warn" : "pcap-dim"}>
                  {estFromLink
                    ? `현재 트래픽이 없습니다 — 링크가 포화되면 최대 용량(${maxMB} MB)까지 약 `
                    : `현재 트래픽 기준 최대 용량(${maxMB} MB)까지 약 `}
                  <b>{fmtDur(secToCap)}</b>
                  {tooFast && (
                    <>
                      {" "}· 설정한 {maxMin}분보다 훨씬 먼저 멈춥니다.
                      {snapLen !== 96 && (
                        <button className="toolbtn" style={{ marginLeft: 6, fontSize: 11 }}
                          onClick={() => setSnapLen(96)}>
                          헤더만으로 바꾸기
                        </button>
                      )}
                    </>
                  )}
                  {anyIface && !estFromLink && (
                    <div className="pcap-dim" style={{ fontSize: 11 }}>
                      현재 트래픽은 물리 NIC 기준 최소치입니다. 전체(-i any)는 루프백·컨테이너 트래픽까지
                      잡으므로 실제 파일은 이보다 클 수 있습니다.
                    </div>
                  )}
                </span>
              ) : (
                <span className="pcap-dim">트래픽 정보를 아직 알 수 없습니다.</span>
              )}
            </div>

            <label className="pcap-dim" style={{ fontSize: 12, display: "block", margin: "6px 0 2px" }}>
              저장 위치
            </label>
            <TargetPicker targets={env?.targets ?? []} value={target} onChange={setTarget}
              mode="pcap" disabled={busy} />

            <div style={{ display: "flex", alignItems: "center", gap: 12, marginTop: 10, flexWrap: "wrap" }}>
              <label className="check" style={{ fontSize: 12, marginTop: 0 }}>
                <input type="checkbox" checked={promisc} onChange={(e) => setPromisc(e.target.checked)} />
                프로미스큐어스 모드 (기본 꺼짐)
              </label>
              <span style={{ flex: 1 }} />
              {anyLive && (
                <span className="pcap-warn" style={{ fontSize: 12 }}>
                  이 서버에서 이미 캡쳐가 실행 중입니다 — 먼저 중지하세요
                </span>
              )}
              <button className="toolbtn primary" onClick={start} disabled={!canStart}>
                {busy ? "처리 중…" : "📡 캡쳐 시작"}
              </button>
            </div>
          </div>

          <div className="err">{err}</div>
          {info && <div className="pcap-warn" style={{ fontSize: 12, margin: "2px 0 6px" }}>{info}</div>}

          {/* ---------------- existing captures ---------------- */}
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", margin: "6px 0" }}>
            <b style={{ fontSize: 13 }}>
              캡쳐 목록
              {env && env.existingCount > 0 && (
                <span className="pcap-dim" style={{ fontWeight: 400 }}>
                  {" "}· {env.existingCount}개 / {fmtSize(env.existingBytes)}
                </span>
              )}
            </b>
            <button className="toolbtn" onClick={() => void refresh()} disabled={busy}>새로고침</button>
          </div>

          <div style={{ maxHeight: 300, overflowY: "auto" }}>
            {list.length === 0 ? (
              <div className="pcap-dim" style={{ padding: 14, textAlign: "center" }}>
                이 서버에 캡쳐가 없습니다.
              </div>
            ) : (
              <table className="proc" style={{ tableLayout: "auto" }}>
                <thead>
                  <tr>
                    <th className="left"><span className="lbl">상태</span></th>
                    <th className="left"><span className="lbl">시작 ~ 종료</span></th>
                    <th className="left"><span className="lbl">대상</span></th>
                    <th><span className="lbl">크기</span></th>
                    <th><span className="lbl">패킷 / 드롭</span></th>
                    <th className="left"><span className="lbl">동작</span></th>
                  </tr>
                </thead>
                <tbody>
                  {list.map((cm) => {
                    const st = statusUI(cm.status);
                    const live = isLive(cm.status);
                    const elapsed = live && cm.startT > 0 ? Math.max(0, (now - cm.startT) / 1000) : 0;
                    const dropped = Math.max(cm.droppedKern, 0) + Math.max(cm.droppedIf, 0);
                    const dling = dl && dl.hostId === hostId && dl.id === cm.id;
                    return (
                      <Fragment key={cm.id}>
                        <tr>
                          <td className="left">
                            <span className={st.cls} style={{ fontWeight: st.cls === "pcap-bad" ? 600 : 400 }}>
                              {st.icon} {st.label}
                            </span>
                            {cm.uncertain && (
                              <div className="pcap-dim" style={{ fontSize: 11 }}>(확인 불가 — hidepid)</div>
                            )}
                          </td>
                          <td className="left pcap-dim" style={{ fontSize: 11 }}>
                            {fmtTime(cm.startT)}
                            {live ? (
                              <> ~ 진행 중 · 경과 <b>{fmtDur(elapsed)}</b></>
                            ) : (
                              <> ~ {fmtTime(cm.lastT)}</>
                            )}
                            {cm.startedBy && (
                              <div className="pcap-dim" style={{ fontSize: 11 }}>시작: {cm.startedBy}</div>
                            )}
                          </td>
                          <td className="left pcap-dim" style={{ fontSize: 11 }}>
                            <span className="pcap-mono">{cm.iface || "any"}</span>
                            {" · "}
                            <span className="pcap-mono">
                              {cm.hostSpec || cm.portSpec
                                ? [cm.hostSpec, cm.portSpec].filter(Boolean).join(" / ")
                                : "필터 없음"}
                            </span>
                            {cm.snapLen > 0 && <div>스냅렌 {cm.snapLen}B</div>}
                            {cm.linkType && <div>{cm.linkType}</div>}
                          </td>
                          <td>{fmtSize(cm.sizeBytes)}</td>
                          <td style={{ fontSize: 11 }}>
                            {fmtNum(cm.captured)}
                            {dropped > 0 && <span className="pcap-warn"> / {fmtNum(dropped)}</span>}
                          </td>
                          <td className="left">
                            {live ? (
                              <>
                                <button className="toolbtn" onClick={() => void stop(cm)} disabled={busy}>중지</button>{" "}
                                <button className="toolbtn" onClick={() => onDownload(cm)} disabled={busy || !!dl}>
                                  현재까지 다운로드
                                </button>{" "}
                                {cm.status === "stopping" && (
                                  <button className="toolbtn danger" onClick={() => setForceAsk(cm)} disabled={busy}>
                                    강제 중지
                                  </button>
                                )}{" "}
                                {/* The only exit from a row whose liveness cannot be
                                    verified (hidepid): stop tracking it without
                                    touching the file. Otherwise it reads "capturing"
                                    forever and blocks every new capture on this host. */}
                                {cm.uncertain && (
                                  <button className="toolbtn" onClick={() => setForgetAsk(cm)} disabled={busy}
                                    title="상태를 확인할 수 없는 캡쳐를 목록에서만 제외합니다 (pcap 파일은 서버에 남습니다)">
                                    목록에서 제외
                                  </button>
                                )}
                              </>
                            ) : (
                              <>
                                <button className="toolbtn" onClick={() => onDownload(cm)} disabled={busy || !!dl}>
                                  다운로드
                                </button>{" "}
                                {cm.localPath && (
                                  <>
                                    <button className="toolbtn" title={cm.localPath}
                                      onClick={() => OpenCaptureFolder(hostId, cm.id).catch((e) => onToast(String(e)))}>
                                      📁 폴더 열기
                                    </button>{" "}
                                    <button className="toolbtn"
                                      onClick={() => OpenInWireshark(hostId, cm.id).catch((e) => onToast(String(e)))}>
                                      Wireshark로 열기
                                    </button>{" "}
                                  </>
                                )}
                                <button className="toolbtn" onClick={() => setDelAsk(cm)} disabled={busy}>삭제</button>{" "}
                                {cm.status === "orphan" && (
                                  <button className="toolbtn" onClick={() => setForgetAsk(cm)} disabled={busy}
                                    title="고아 상태 캡쳐를 목록에서만 제외합니다 (pcap 파일은 서버에 남습니다)">
                                    목록에서 제외
                                  </button>
                                )}
                              </>
                            )}
                          </td>
                        </tr>

                        {(cm.err || dropped > 0 || cm.status === "interrupted" || cm.status === "orphan" || dling) && (
                          <tr style={{ background: "var(--row-sel, rgba(255,255,255,0.03))" }}>
                            <td colSpan={6} className="left pcap-reason" style={{ padding: "6px 10px" }}>
                              {cm.status === "interrupted" && (
                                <div className="pcap-bad">
                                  ⚠ 서버 재부팅·강제종료 등으로 캡쳐가 끊겼습니다. 파일 끝부분이 잘려 있을 수 있지만
                                  대부분 그대로 열립니다.
                                </div>
                              )}
                              {cm.status === "orphan" && (
                                <div className="pcap-bad">
                                  ⚠ tcpdump 프로세스가 파일을 아직 붙들고 있습니다. 서버에서 확인이 필요합니다
                                  (삭제하면 디스크 공간이 돌아오지 않습니다).
                                </div>
                              )}
                              {dropped > 0 && (
                                <div className="pcap-warn">
                                  패킷 {fmtNum(dropped)}개가 드롭되었습니다. 포트 필터를 좁히거나 패킷 크기를
                                  '헤더만'으로 낮추세요.
                                </div>
                              )}
                              {cm.err && <div className="pcap-bad pcap-mono">{cm.err}</div>}
                              {dling && (
                                <div className="pcap-dl">
                                  다운로드 중… {Math.round(dl!.pct)}%
                                  <div className="pcap-bar"><div style={{ width: `${dl!.pct}%` }} /></div>
                                </div>
                              )}
                            </td>
                          </tr>
                        )}
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

      {/* Exactly ONE confirmation is ever mounted. ConfirmDialog binds Y/N/Escape
          on window, so two at once would both act on a single keypress — and since
          they share a z-index, the newer one paints underneath the one being read.
          The operator-initiated dialogs win; an auto-raised completion prompt is
          queued (see pendingAsk) rather than dropped. ConfirmDialog appends its own
          (Y)/(N) hints, so labels must not repeat them. */}
      {delAsk ? (
        <ConfirmDialog
          title="캡쳐 삭제"
          danger
          message={
            <>
              <div>이 캡쳐 파일을 서버에서 삭제할까요?</div>
              <div className="pcap-mono pcap-dim" style={{ fontSize: 12, marginTop: 6 }}>{delAsk.file}</div>
              {notMine(delAsk) && (
                <div className="pcap-warn" style={{ fontSize: 12, marginTop: 6 }}>
                  다른 사용자({delAsk.startedBy})가 시작한 캡쳐입니다.
                </div>
              )}
              {!delAsk.localPath && (
                <div className="pcap-warn" style={{ fontSize: 12, marginTop: 6 }}>
                  아직 이 PC로 내려받지 않았습니다. 삭제하면 복구할 수 없습니다.
                </div>
              )}
            </>
          }
          confirmLabel="삭제"
          onConfirm={() => { const cm = delAsk; setDelAsk(null); void remove(cm); }}
          onCancel={() => setDelAsk(null)}
        />
      ) : forceAsk ? (
        <ConfirmDialog
          title="캡쳐 강제 중지"
          danger
          message={
            <>
              <div>정상 중지에 응답하지 않는 캡쳐에 종료 신호를 직접 보냅니다.</div>
              <div className="pcap-warn" style={{ fontSize: 12, marginTop: 6 }}>
                파일 끝부분이 잘릴 수 있습니다. 가능하면 잠시 더 기다려 보세요.
              </div>
              {notMine(forceAsk) && (
                <div className="pcap-warn" style={{ fontSize: 12, marginTop: 6 }}>
                  다른 사용자({forceAsk.startedBy})가 시작한 캡쳐입니다.
                </div>
              )}
            </>
          }
          confirmLabel="강제 중지"
          onConfirm={() => { const cm = forceAsk; setForceAsk(null); void forceStop(cm); }}
          onCancel={() => setForceAsk(null)}
        />
      ) : forgetAsk ? (
        <ConfirmDialog
          title="목록에서 제외"
          message={
            <>
              <div>
                이 캡쳐는 <b>서버에서 상태를 확인할 수 없습니다</b>
                {forgetAsk.status === "orphan"
                  ? " (tcpdump가 파일을 붙들고 있다고 기록된 상태)."
                  : " (/proc 접근이 제한된 호스트)."}
              </div>
              <div style={{ marginTop: 8 }}>
                목록에서만 제외하고, <b>pcap 파일은 서버에 그대로 남깁니다.</b> 아직 기록 중일
                가능성이 있어 파일은 지우지 않습니다.
              </div>
              <div className="pcap-mono pcap-dim" style={{ fontSize: 12, marginTop: 6 }}>{forgetAsk.file}</div>
              <div className="pcap-dim" style={{ fontSize: 12, marginTop: 6 }}>
                제외하면 이 서버에서 새 캡쳐를 다시 시작할 수 있습니다. 남은 파일은 서버에서
                직접 확인·정리하세요.
              </div>
            </>
          }
          confirmLabel="제외"
          cancelLabel="취소"
          onConfirm={() => { const cm = forgetAsk; setForgetAsk(null); void forget(cm); }}
          onCancel={() => setForgetAsk(null)}
        />
      ) : ask ? (
        <ConfirmDialog
          title="패킷 캡쳐 완료"
          message={
            <>
              <div>
                <b>{statusUI(ask.status).label}</b> — {fmtSize(ask.sizeBytes)}
                {ask.captured >= 0 ? ` · ${fmtNum(ask.captured)} 패킷` : ""}
              </div>
              <div style={{ marginTop: 8 }}>패킷의 내용을 다운로드 하시겠습니까?</div>
              <div className="pcap-dim" style={{ fontSize: 12, marginTop: 6 }}>
                아니오를 눌러도 서버에 남아 있어 나중에 이 목록에서 받을 수 있습니다.
              </div>
            </>
          }
          confirmLabel="다운로드"
          cancelLabel="나중에"
          onConfirm={() => { const cm = ask; setAsk(null); onDownload(cm); }}
          onCancel={() => setAsk(null)}
        />
      ) : null}
    </>
  );
}

// pickDefaultNic prefers a bond/bridge master (capturing on "any" would double
// every packet across its slaves), then the fastest physical interface that is
// up, and only falls back to "any".
function pickDefaultNic(nics: monitor.NIC[]): string {
  const master = nics.find((n) => n.isMaster && n.up);
  if (master) return master.name;
  const phys = nics
    .filter((n) => n.up && !n.virtual && !n.loopback && n.name !== "any")
    .sort((a, b) => b.speedMb - a.speedMb);
  if (phys.length > 0) return phys[0].name;
  return "any";
}
