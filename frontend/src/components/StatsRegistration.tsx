import { useEffect, useRef, useState } from "react";
import { StatsInspect, StatsValidate, StatsRegister, StatsRetry, StatsImportPatterns, StatsConnections, StatsSaveConnection, StatsClusterConnections, StatsSetClusterConnection, StatsTesterPrefix, StatsCancelInspect, StatsPlacementHints, StatsRecentRegistrations } from "../../wailsjs/go/main/App";
import { EventsOn } from "../../wailsjs/runtime";
import { statsreg } from "../../wailsjs/go/models";
import { useEsc } from "../useEsc";
import StatsConnectionFields, { KafkaRow, defaultStatsConfig, statsConnection } from "./StatsConnectionFields";
import StatsDiscoverReview, { DiscoverResult, discoverFromCluster } from "./StatsDiscoverReview";
import StatsManage, { ManageStatus } from "./StatsManage";
import StatsMessage, { parseStatsMessage } from "./statsMessage";
import "./StatsRegistration.css";

// liz_server can hold the same hostname more than once; the server side rejects
// a repeated host, so only the first row of each hostname is sent.
function uniqueServers(s: statsreg.Selection): statsreg.Selection {
  const seen = new Set<string>();
  return new statsreg.Selection({ ...s, servers: s.servers.filter(h => !seen.has(h.hostname) && !!seen.add(h.hostname)) });
}

// Data format codes of a checkpoint, in screen words.
const formatNames = ["참/거짓 (Boolean)", "정수 (Integer)", "큰 정수 (Long)", "숫자 (Double)", "문자열 (String)"];
const firstToken = (key: string) => key.match(/\{([^{}]+)\}/)?.[1];
// A key whose value is not a number cannot be registered as a number (the server
// refuses it too): the collector fails on it every interval and slows every MK119
// point. Only a key outside the product list can be switched to text instead.
const numberLocked = (p: statsreg.Point) => p.badValue && p.format !== 4;
const numberLockedWhy = "지금 값이 숫자가 아니라 숫자로는 등록할 수 없습니다 — 수집 서버가 매 주기 오류를 내고 다른 수집까지 늦어집니다. 값이 숫자가 되면 다음 업데이트에서 추가하세요";

// cluster: the cluster the dialog was opened for. Its own connection (set in the
// cluster dialog) is filled in ahead of the recent list, and kept up to date.
export default function StatsRegistration({ initialHost, cluster, onClose }: { initialHost: string; cluster?: { id: string; name: string }; onClose: () => void }) {
  const [config, setConfig] = useState<statsreg.Config>(defaultStatsConfig(initialHost));
  const [kafkaRows, setKafkaRows] = useState<KafkaRow[]>([{ hostname: "", ip: initialHost, port: 9092 }]);
  const [plan, setPlan] = useState<statsreg.Plan | null>(null);
  const [selection, setSelection] = useState<statsreg.Selection | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [checked, setChecked] = useState(false);
  const [result, setResult] = useState<statsreg.Result | null>(null);
  const [retryID, setRetryID] = useState("");
  const [filter, setFilter] = useState("");
  const [progress, setProgress] = useState("");
  // "" = first choice (register / manage); "ask" = update an existing group or
  // register a new one; "new" / "update" = registering; "manage" = 기존 등록 관리.
  const [mode, setMode] = useState<"" | "ask" | "new" | "update" | "manage">("");
  // Registrations that did not finish, to resume without typing their number.
  const [recent, setRecent] = useState<statsreg.RegistrationRecord[]>([]);
  useEffect(() => { StatsRecentRegistrations().then(v => setRecent(v || [])).catch(() => {}); }, [result]);
  // The manage view's latest result, repeated in the bottom status line.
  const [manageStatus, setManageStatus] = useState<ManageStatus | null>(null);
  // A confirmation inside the manage view owns Escape.
  const [inner, setInner] = useState(false);
  const [askGroup, setAskGroup] = useState(0);
  const count = selection?.points.filter(p => p.selected).length || 0;
  const moveCount = selection?.points.filter(p => p.move).length || 0;
  const deviceCount = new Set(selection?.points.filter(p => p.selected || p.move).map(p => p.host)).size;
  const group = mode === "update" ? plan?.existing?.find(g => g.groupId === selection?.updateGroupId) : undefined;
  const existingDevice = (host: string) => group?.devices.filter(d => d.host === host).sort((a, b) => b.points - a.points)[0];
  const registeredCount = plan?.points.filter(p => p.registered).length || 0;
  const movedCount = plan?.points.filter(p => p.moved).length || 0;
  const newDeviceCount = new Set(selection?.points.filter(p => (p.selected || p.move) && !existingDevice(p.host)).map(p => p.host)).size;
  // Update mode: keys this environment never had as a checkpoint (appeared since the last registration).
  // Keys left out because their value is not a number were seen before: not "new".
  const isNew = (p: statsreg.Point) => mode === "update" && !p.registered && !p.deleted && !p.badValue;
  const badCount = selection?.points.filter(p => p.badValue && !p.registered).length || 0;
  const newCount = selection?.points.filter(isNew).length || 0;
  const movable = (selection?.points || []).map((p, i) => ({ p, i })).filter(({ p }) => mode === "update" && p.moved && p.checkpointId > 0 && p.moveGroupId === selection?.updateGroupId);
  // New keys first, then the rest in analysis order.
  const listed = (selection?.points || []).map((p, i) => ({ p, i })).filter(({ p }) => !p.registered).sort((a, b) => Number(isNew(b.p)) - Number(isNew(a.p)) || a.i - b.i);
  const unresolvedTokens = Array.from(new Set((plan?.points || []).filter(p => !p.host).map(p => firstToken(p.key)).filter((v): v is string => !!v)));
  // Tokens some key of which still has no server.
  const unresolvedLeft = unresolvedTokens.filter(token => selection?.points.some(p => !p.host && firstToken(p.key) === token)).length;
  useEsc(onClose, !busy && !inner);
  // Servers recommended for those values by the cluster's /etc/hosts (logan 10-23: 「hosts 보고 배치」).
  const [hints, setHints] = useState<Record<string, statsreg.PlacementHint>>({});
  const [hintsPlan, setHintsPlan] = useState("");
  const [hintsError, setHintsError] = useState("");
  const unplaced = (p: statsreg.Point) => !plan?.points.find(o => o.key === p.key)?.host;
  const tokenHost = (token: string) => selection?.points.find(p => unplaced(p) && firstToken(p.key) === token)?.host || "";
  function placeToken(token: string, host: string) { if (selection) edit({ points: selection.points.map(p => unplaced(p) && firstToken(p.key) === token ? { ...p, host, selected: !!host && !p.badValue } : p) }); }
  useEffect(() => {
    if (!cluster || !plan || (mode !== "new" && mode !== "update") || !unresolvedTokens.length || hintsPlan === plan.id) return;
    setHintsPlan(plan.id);
    work("클러스터 서버의 /etc/hosts 에서 { } 값의 서버 찾는 중…", async () => {
      setHintsError("");
      let list: statsreg.PlacementHint[] = [];
      try { list = await StatsPlacementHints(cluster.id, plan.id) || []; } catch (e) { setHintsError(String(e)); return; }
      const byToken: Record<string, statsreg.PlacementHint> = {};
      list.forEach(h => { byToken[h.token] = h; });
      setHints(byToken);
      setSelection(s => s ? new statsreg.Selection({ ...s, points: s.points.map(p => { const h = byToken[firstToken(p.key) || ""]; return unplaced(p) && !p.host && h?.host ? { ...p, host: h.host, selected: !p.badValue && !p.registered } : p; }) }) : s);
      setChecked(false);
    });
  }, [plan?.id, mode]);
  useEffect(() => EventsOn("statsProgress", (m: string) => setProgress(m)), []);
  // Tester-only build (statsreg.TesterPrefix): "" in a product build.
  const [tester, setTester] = useState("");
  useEffect(() => { StatsTesterPrefix().then(setTester).catch(() => {}); }, []);
  const [saved,setSaved] = useState<statsreg.SavedConnection[]>([]);
  const [clusterConn, setClusterConn] = useState<statsreg.SavedConnection | null>(null);
  const [discovery, setDiscovery] = useState<DiscoverResult | null>(null);
  const choices = clusterConn ? [clusterConn, ...saved] : saved;
  // Where the fields came from when the dialog opened: the cluster's own saved
  // connection, the most recent one, or nothing.
  const [filledFrom, setFilledFrom] = useState<"" | "cluster" | "recent">("");
  // Opened for a cluster without its own connection: save the one that checks out to it.
  const [saveToCluster, setSaveToCluster] = useState(true);
  // The automatic 「연결 확인」 started on opening (can be cancelled).
  const [auto, setAuto] = useState(false);
  const autoCancelled = useRef(false);
  function rowsOf(c: statsreg.SavedConnection): KafkaRow[] | null { return c.kafkaRows?.length ? c.kafkaRows.map(r => ({ hostname: r.hostname, ip: r.ip, port: r.port })) : null; }
  function applySaved(c: statsreg.SavedConnection) { setConfig(c.config); const rows = rowsOf(c); if (rows) setKafkaRows(rows); }
  // The most recent connection that inspected successfully is filled in.
  // The cluster's own connection wins over the most recent one. A connection
  // saved for this cluster (or, outside a cluster, the most recent one) is
  // checked right away; a failure stays on this screen with the input marked.
  useEffect(() => {
    Promise.all([StatsConnections().catch(() => []), cluster ? StatsClusterConnections().catch(() => ({})) : Promise.resolve({})]).then(([list, byCluster]) => {
      const own = cluster ? (byCluster as Record<string, statsreg.SavedConnection>)[cluster.id] : undefined;
      setSaved(list || []);
      setClusterConn(own || null);
      const first = own || list?.[0];
      if (!first) return;
      applySaved(first);
      setFilledFrom(own ? "cluster" : "recent");
      if (own || !cluster) {
        setAuto(true);
        autoCancelled.current = false;
        work("저장된 접속 정보로 연결 확인 중… (취소할 수 있습니다)", () => connect(first.config, rowsOf(first) || kafkaRows, !!own)).finally(() => setAuto(false));
      }
    });
  }, []);

  function edit(patch: Partial<statsreg.Selection>) { setSelection(s => s ? new statsreg.Selection({ ...s, ...patch }) : s); setChecked(false); setError(""); }
  function point(index: number, patch: Partial<statsreg.Point>) { if (selection) edit({ points: selection.points.map((p, i) => i === index ? { ...p, ...patch } : p) }); }
  async function work(label: string, fn: () => Promise<void>) { setError(""); setProgress(""); setBusy(label); try { await fn(); } catch (e) { setError(String(e)); } finally { setBusy(""); setProgress(""); } }
  const connectionConfig = () => statsConnection(config, kafkaRows);
  function adopt(p: statsreg.Plan) { setPlan(p); setAskGroup(p.existing?.[0]?.groupId || 0); setSelection(new statsreg.Selection({ planId: p.id, groupName: p.groupName, explorerId: p.explorers[0].id, clusterId: p.clusters[0].id, servers: p.servers, points: p.points.map(pt => pt.registered ? { ...pt, selected: false } : pt), updateGroupId: 0 })); }
  // reinspect: 「연결 확인」 again with the same connection (registrations changed).
  async function reinspect() { const p = await StatsInspect(config); adopt(p); return p; }
  // connect: 「연결 확인」. A successful connection is remembered; for the cluster
  // too when it has its own connection or the user keeps 「이 클러스터에 저장」 ticked.
  async function connect(base: statsreg.Config, rows: KafkaRow[], toCluster: boolean) {
    const c = statsConnection(base, rows);
    setConfig(c);
    let p: statsreg.Plan;
    try { p = await StatsInspect(c); } catch (e) { if (autoCancelled.current) throw new Error("저장된 접속 정보로 하던 연결 확인을 취소했습니다 — 값을 확인하고 「연결 확인」을 누르세요."); throw e; }
    const conn = new statsreg.SavedConnection({ config: c, kafkaRows: rows, savedAt: "" });
    StatsSaveConnection(conn).catch(() => {});
    if (cluster && toCluster) { StatsSetClusterConnection(cluster.id, conn).then(() => { setClusterConn(conn); setFilledFrom("cluster"); }).catch(() => {}); }
    adopt(p); setResult(null); setMode("");
  }

  return <div className="scrim stats-scrim"><div className="stats-dialog" role="dialog" aria-modal="true" aria-labelledby="stats-title" onKeyDown={e => { e.stopPropagation(); if (e.key === "Escape" && !busy && !inner) { e.preventDefault(); onClose(); } }}>
    <div className="stats-head"><div><h2 id="stats-title">MK119 v10 리소스 수집 등록</h2><p>연결 확인 → 「새로 등록」(서버별로 고르기 → 등록 전 검사 → 등록: 장비 저장 → 수집 서버가 새 장비를 알아차릴 때까지 기다림 → 체크포인트 저장 → 값이 들어오는지 확인) 또는 「기존 등록 관리」(이름·주기 수정, 장비·그룹 삭제)</p>{tester && <p className="stats-warning" role="note">🧪 테스트 빌드: 이름 앞에 「{tester}」 — 새로 만드는 그룹은 「{tester}_…」, 장비는 「{tester} …」이고, 이름 앞에 「{tester}」가 붙은 그룹·장비만 기존 등록으로 보고 다룹니다</p>}</div><button className="toolbtn" disabled={!!busy} onClick={onClose}>닫기</button></div>
    <div className="stats-body">
    {!plan && <>
      {cluster && (clusterConn ? <p className="stats-muted">📦 클러스터 <b>{cluster.name}</b>에 저장해 둔 접속 정보로 채웠습니다. 연결 확인에 성공하면 고친 내용도 이 클러스터에 저장합니다.</p>
        : <p className="stats-muted">📦 클러스터 <b>{cluster.name}</b>에는 아직 저장된 접속 정보가 없습니다 — {filledFrom === "recent" ? <>아래 칸은 <b>최근에 쓴 다른 연결</b>로 채운 것이니 이 클러스터 것이 맞는지 확인하세요(「서버에서 불러오기」로 이 클러스터 서버에서 읽어 올 수도 있습니다).</> : <>「서버에서 불러오기」로 채우거나 직접 입력하세요.</>} <label className="stats-check stats-inline-check"><input type="checkbox" checked={saveToCluster} disabled={!!busy} onChange={e => setSaveToCluster(e.target.checked)} />연결 확인에 성공하면 이 클러스터에 저장</label></p>)}
      {choices.length > 1 && <label className="stats-recent">최근 연결<select disabled={!!busy} defaultValue={0} onChange={e => applySaved(choices[Number(e.target.value)])}>{choices.map((c, i) => <option key={i} value={i}>{c === clusterConn ? `📦 ${cluster?.name} · ` : ""}{c.config.dbHost}:{c.config.dbPort} · Redis {c.config.redisHost} · {c.savedAt.slice(0, 16).replace("T", " ")}</option>)}</select></label>}
      {discovery && <StatsDiscoverReview result={discovery} config={config} baseline={defaultStatsConfig(initialHost)} kafkaRows={kafkaRows} onApply={(c, rows) => { setConfig(c); if (rows) setKafkaRows(() => rows); setDiscovery(null); }} onClose={() => setDiscovery(null)} />}
      <StatsConnectionFields bad={parseStatsMessage(error).field} config={config} setConfig={setConfig} kafkaRows={kafkaRows} setKafkaRows={setKafkaRows} disabled={!!busy} onDiscover={cluster ? () => work("서버에서 접속 정보 읽는 중…", async () => setDiscovery(await discoverFromCluster(cluster.id))) : undefined} discovering={busy === "서버에서 접속 정보 읽는 중…"} />
      <p className="stats-muted">연결 확인에 성공한 접속 정보는 비밀번호를 포함해 이 PC의 ~/.rtaskmgr/stats-connections.json에 저장되고 다음에 자동으로 채워집니다. Kafka 토픽: liz.message.pipeline</p>
      <button className="toolbtn primary" disabled={!!busy} onClick={() => { autoCancelled.current = false; work("연결 및 Redis 키 분석 중…", () => connect(config, kafkaRows, !!clusterConn || saveToCluster)); }}>연결 확인 및 리소스 수집 키 찾기</button>
      <details className="stats-recovery"><summary>마무리 안 된 등록 이어서 하기 / 수집 다시 확인</summary><p className="stats-muted">언제 쓰나: 등록하다가 창을 닫았거나, 수집 서버에 알리지 못했거나, 기다리는 중에 멈췄을 때. 이미 저장된 것은 다시 만들지 않고 남은 단계만 합니다. 다 끝난 등록이면 수집 확인만 다시 합니다.</p>
        {recent.length > 0 ? <ul>{recent.map(r => <li key={r.id}>{r.at} · 그룹 {r.groupName} · {r.stage} <button className="linkbtn" disabled={!!busy} onClick={() => work("저장 상태 확인 후 남은 단계 하는 중…", async () => { const c = connectionConfig(); setConfig(c); setRetryID(r.id); setResult(await StatsRetry(c, r.id)); })}>남은 단계 마저 하기</button>{r.message && <div className="stats-muted"><StatsMessage text={r.message} /></div>}</li>)}</ul> : <p className="stats-muted">이 PC에 마무리 안 된 등록이 없습니다(최근 30일).</p>}
        <details className="stats-more-block"><summary>자세히 — 등록 기록 번호로 하기</summary><p className="stats-muted">등록 기록 번호는 등록 결과 아래에 나오고, 기록은 이 PC의 ~/.rtaskmgr/stats-registration 폴더에 있습니다.</p><label>등록 기록 번호<input value={retryID} onChange={e => setRetryID(e.target.value)} /></label><button className="toolbtn" disabled={!!busy || !retryID} onClick={() => work("저장 상태 확인 후 남은 단계 하는 중…", async () => { const c = connectionConfig(); setConfig(c); setResult(await StatsRetry(c, retryID)); })}>남은 단계 마저 하기 (이미 저장된 건 다시 만들지 않음)</button><button className="toolbtn" title="그 등록에 쓴 체크포인트 이름·주기를 이 PC에 기억해, 다음 조회 때 같은 종류의 키에 미리 채웁니다(장비 이름은 기억하지 않음)" disabled={!!busy || !retryID} onClick={() => work("포인트 이름·주기 기억 중…", async () => { const ev = await StatsImportPatterns(retryID); setError(`포인트 이름·주기 ${ev.changes?.length || 0}건 기억${ev.notes?.length ? ` · 장비마다 이름이 다른 키 ${ev.notes.length}종(가장 많이 쓴 이름 채택)` : ""}`); })}>이 등록에 쓴 포인트 이름·주기 기억</button></details></details>
    </>}
    {plan && selection && mode === "" && <div className="stats-update-ask">
      <p><b>무엇을 할까요?</b></p>
      {plan.existing?.length ? <ul>{plan.existing.map(g => <li key={g.groupId}>{g.name} — 장비 {g.devices.length}대 · 체크포인트 {g.points}개</li>)}</ul> : null}
      {plan.existing?.length > 0 && plan.orphans > 0 && <p className="stats-warning">그룹에 없는 리소스 수집 장비 {plan.orphans}대 — 「기존 등록 관리」에서 확인·정리할 수 있습니다.</p>}
      <div className="stats-update-buttons"><button className="toolbtn primary" onClick={() => setMode(plan.existing?.length ? "ask" : "new")}>{plan.existing?.length ? "새로 등록 / 새 키 추가" : "새로 등록"}</button><button className="toolbtn" disabled={!plan.existing?.length && !plan.orphans} onClick={() => { setResult(null); setMode("manage"); }}>기존 등록 관리 (이름·주기 수정, 삭제)</button></div>
      {!plan.existing?.length && plan.orphans > 0 && <p className="stats-warning">관리할 그룹은 없지만 그룹에 없는 리소스 수집 장비가 {plan.orphans}대 있습니다(admin 에서 그룹만 지운 경우 등) — 「기존 등록 관리」에서 확인·정리하세요.</p>}
      {!plan.existing?.length && !plan.orphans && <p className="stats-muted">「기존 등록 관리」를 쓸 수 없습니다 — 이 환경에 MK119 v10 System Stats 체크포인트가 든 그룹이 없습니다{tester ? ` (테스트 빌드는 이름 앞에 「${tester}」가 붙은 그룹만 봅니다)` : ""}.</p>}
    </div>}
    {plan && selection && mode === "ask" && <div className="stats-update-ask">
      <p><b>이 환경에 이미 등록된 작업이 있습니다.</b></p>
      <ul>{plan.existing.map(g => <li key={g.groupId}>{g.name} — 장비 {g.devices.length}개 · 체크포인트 {g.points}개{g.vanished ? ` · 원본 키가 사라진 체크포인트 ${g.vanished}개` : ""}</li>)}</ul>
      <p>기존에 등록된 작업을 업데이트하겠습니까? 새로 생긴 키를 추가하고, 다른 서버로 옮겨 간 모듈의 체크포인트는 지금 서버의 장비로 옮깁니다(선택). 원본 키가 사라진 체크포인트는 그대로 둡니다.</p>
      {plan.existing.length > 1 && <label>업데이트할 그룹<select value={askGroup} onChange={e => setAskGroup(Number(e.target.value))}>{plan.existing.map(g => <option key={g.groupId} value={g.groupId}>{g.name} (ID {g.groupId})</option>)}</select></label>}
      <div className="stats-update-buttons"><button className="toolbtn primary" onClick={() => { const g = plan.existing.find(x => x.groupId === askGroup); if (g) { edit({ updateGroupId: g.groupId, groupName: g.name, clusterId: g.clusterId || selection.clusterId, points: selection.points.map(p => ({ ...p, move: p.moved && p.checkpointId > 0 && p.moveGroupId === g.groupId })) }); setMode("update"); } }}>예, 업데이트</button><button className="toolbtn" onClick={() => { edit({ updateGroupId: 0, groupName: plan.groupName, points: selection.points.map(p => ({ ...p, move: false })) }); setMode("new"); }}>아니오, 새 그룹으로 등록</button></div>
    </div>}
    {plan && mode === "manage" && <StatsManage plan={plan} busy={busy} work={work} reinspect={reinspect} onDialog={setInner} onStatus={setManageStatus} />}
    {plan && selection && (mode === "new" || mode === "update") && <>
      {result && <div className={result.verified ? "stats-success stats-result" : "stats-warning stats-result"} role="status"><div><b>등록 결과</b> — <StatsMessage text={result.message} /></div>{result.notified && result.checkpointIds.length + (result.movedIds?.length || 0) > 0 && <p>수집 확인: {result.collected}/{result.checkpointIds.length + (result.movedIds?.length || 0)}개{result.missing?.length ? ` · 값이 안 들어온 체크포인트 ${result.missing.length}개` : ""}</p>}<p className="stats-muted">이 등록이 중간에 멈춰도 「연결 설정」 → 「마무리 안 된 등록 이어서 하기」에 자동으로 나옵니다(등록 기록 번호 <code>{result.id}</code>).</p>{result.committed && <div className="stats-update-buttons"><button className="toolbtn primary" disabled={!!busy} onClick={() => work("기존 등록 관리 화면 준비 중…", async () => { await reinspect(); setResult(null); setMode("manage"); })}>기존 등록 관리로 (이름·주기 정리)</button><span className="stats-muted">방금 등록한 이름이 키 그대로인 것을 바로 고칠 수 있습니다. 같은 접속 정보로 다시 조회하므로 연결 확인을 누르지 않아도 됩니다.</span></div>}{!result.verified && <button className="toolbtn" disabled={!!busy} onClick={() => work(result.notified ? "수집 다시 확인 중…" : "저장 상태 확인 후 남은 단계 하는 중…", async () => setResult(await StatsRetry(config, result.id)))}>{result.notified ? "수집 다시 확인" : "남은 단계 마저 하기 (이미 저장된 건 다시 만들지 않음)"}</button>}</div>}
      <fieldset disabled={!!busy || !!result} className="stats-settings"><legend>{mode === "update" ? "업데이트 대상과 새 장비 이름" : "등록 위치와 이름"}</legend>
        <label>장비 그룹 (Device Group){mode === "update" ? " — 새 키를 넣을 그룹" : ""}<input value={selection.groupName} disabled={mode === "update"} onChange={e => edit({ groupName: e.target.value })} /></label>
        <label>장비 탐색기<select value={selection.explorerId} onChange={e => edit({ explorerId: Number(e.target.value) })}>{plan.explorers.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}</select></label>
        <label title="admin 의 「Collector 클러스터」 — 이 장비들의 값을 모을 수집 서버 묶음">수집 서버 묶음 (Collector 클러스터){mode === "update" ? " (그룹 기존 장비와 같게)" : ""}<select value={selection.clusterId} disabled={mode === "update" && !!plan.existing.find(x => x.groupId === selection.updateGroupId)?.clusterId} onChange={e => edit({ clusterId: Number(e.target.value) })}>{plan.clusters.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}</select></label>
        {selection.servers.map((h, i) => <label key={h.hostname}>{h.hostname === "common" ? "공통 항목" : `실제 호스트: ${h.hostname}`}{existingDevice(h.hostname) ? " — 기존 장비에 추가" : mode === "update" && selection.points.some(p => p.selected && p.host === h.hostname) || selection.points.some(p => p.move && p.host === h.hostname) ? " — 새 장비" : ""}<input value={existingDevice(h.hostname)?.name ?? h.name} disabled={!!existingDevice(h.hostname)} onChange={e => edit({ servers: selection.servers.map((v, n) => n === i ? { ...v, name: e.target.value } : v) })} /><small>{selection.points.filter(p => p.selected && p.host === h.hostname).length}개 선택{selection.points.some(p => p.move && p.host === h.hostname) ? ` · ${selection.points.filter(p => p.move && p.host === h.hostname).length}개 옮겨 옴` : ""}{selection.points.some(p => !p.selected && !p.registered && p.host === h.hostname) && <> · <button type="button" className="linkbtn" onClick={() => edit({ points: selection.points.map(p => p.host === h.hostname && !p.registered && !numberLocked(p) ? { ...p, selected: true } : p) })}>이 호스트 모두 선택{selection.points.some(p => p.deleted && p.host === h.hostname) ? "(삭제됐던 키 포함)" : ""}</button></>}{selection.points.some(p => p.selected && p.host === h.hostname) && <> · <button type="button" className="linkbtn" onClick={() => edit({ points: selection.points.map(p => p.host === h.hostname ? { ...p, selected: false } : p) })}>이 호스트 제외</button></>}</small></label>)}
      </fieldset>
      <div className="stats-muted">고른 체크포인트가 있는 서버만 장비를 만듭니다. 이름이 기존 그룹·장비와 같아도 등록합니다(이미 등록된 키만 막음). <details className="stats-more"><summary>자세히</summary><code>장비 종류 ETC / MK119 System · 인터페이스 MK119 Cache · 드라이버 MK119 v10 System Stats</code></details></div>
      {plan.redisInfo && <div className="stats-muted"><StatsMessage text={plan.redisInfo} /></div>}
      {registeredCount > 0 && <p className="stats-muted">이미 등록된 키 {registeredCount}개는 목록에서 숨겼습니다(중복 등록 방지){movedCount ? ` · 그중 ${movedCount}개는 등록된 장비와 지금 서버가 달라 옮겨진 것으로 보입니다${movable.length ? " — 아래 「옮겨 간 모듈」에서 옮길 수 있습니다" : "(이 그룹 업데이트가 아니거나 체크포인트가 여러 개라 그대로 둠)"}` : ""}.</p>}
      {plan.patternInfo && <p className="stats-muted">{plan.patternInfo}</p>}
      {badCount > 0 && <details className="stats-recovery"><summary>값이 숫자가 아니라 뺀 키 {badCount}개 — 왜 빼나요?</summary><p className="stats-muted">숫자로 수집하는 키인데 지금 값이 빈 값이나 글자입니다(예: SNMP 요청이 아직 없어 빈 값인 API 분당 요청 수). 이대로 등록하면 수집 서버가 매 주기 이 키에서 오류를 내며 값을 하나도 모으지 못하고, 오류마다 0.1초씩 쉬어 다른 MK119 체크포인트 수집까지 늦어집니다(trunk 실측: 76분에 오류 2,730건). 그래서 빼 두는 것이 안전합니다. 값이 숫자가 되면 다음 「새 키 추가」에서 다시 보이니 그때 넣으세요.</p><ul>{selection.points.filter(p => p.badValue && !p.registered).map(p => <li key={p.key}><code>{p.key}</code></li>)}</ul></details>}
      {(plan.warnings || []).map((w, i) => <p className="stats-warning" key={i}>{w}</p>)}
      {unresolvedTokens.length > 0 && <details className="stats-recovery" open><summary>어느 서버인지 모르는 {"{ }"} 값 — 남은 {unresolvedLeft}/{unresolvedTokens.length}개: 서버를 고르면 그 값이 든 키를 한꺼번에 그 서버에 놓습니다</summary>
        <p className="stats-muted">{"{liz_log_db_1}"} 처럼 서버 이름이 아닌 별명입니다. <b>각 서버의 /etc/hosts 에서 이 이름이 가리키는 IP 를 보고, 그 IP 의 서버를 고르세요.</b>{cluster ? " 클러스터 서버들의 /etc/hosts 로 찾은 서버를 미리 골라 두었습니다(「hosts 로 추천」) — 다르면 바꾸세요." : " (클러스터에서 연 창이면 클러스터 서버의 /etc/hosts 로 찾아 미리 골라 줍니다.)"} 확인이 안 되면 고르지 마세요 — 그 키는 이번 등록에서 빠지고, 나중에 「새 키 추가」로 넣을 수 있습니다.</p>
        {hintsError && <div className="stats-warning">/etc/hosts 로 추천하지 못했습니다 — 직접 고르세요. <StatsMessage text={hintsError} /></div>}
        <div className="stats-settings">{unresolvedTokens.map(token => { const value = tokenHost(token); const hint = hints[token]; return <label key={token}>{`{${token}}`}<select disabled={!!busy || !!result} value={value} onChange={e => placeToken(token, e.target.value)}><option value="">고르지 않음 (빼기)</option>{selection.servers.filter(h => h.hostname !== "common").map(h => <option key={h.hostname} value={h.hostname}>{h.name} ({h.hostname})</option>)}</select>{hint?.host && (value === hint.host ? <small className="stats-badge" title={hint.source}>hosts 로 추천 · {hint.source}</small> : <small className="stats-muted">hosts 로 추천한 서버는 {hint.host} ({hint.source})</small>)}{hint && !hint.host && hint.note && <small className="stats-muted">{hint.note}</small>}</label>; })}</div></details>}
      {movable.length > 0 && <details className="stats-recovery stats-moved" open><summary>옮겨 간 모듈 — 체크포인트 {movable.length}개 · 옮기기 {moveCount}개 선택</summary><p className="stats-muted">모듈이 다른 서버로 옮겨 가서, 등록된 장비와 지금 서버가 다른 체크포인트입니다. 체크하면 체크포인트를 지금 서버의 장비로 옮깁니다(같은 체크포인트 ID·이력 유지). 체크 해제하면 그대로 둡니다.</p><div className="stats-toolbar"><button className="toolbtn" disabled={!!busy || !!result} onClick={() => edit({ points: selection.points.map(p => movable.some(m => m.p.key === p.key) ? { ...p, move: true } : p) })}>모두 옮기기</button><button className="toolbtn" disabled={!!busy || !!result} onClick={() => edit({ points: selection.points.map(p => ({ ...p, move: false })) })}>옮기지 않음</button></div><div className="stats-table-wrap"><table className="stats-table"><thead><tr><th>옮기기</th><th>Redis 키 / 체크포인트</th><th>지금 등록된 곳 → 옮길 서버</th></tr></thead><tbody>{movable.map(({ p, i }) => <tr key={p.key}>
        <td><input aria-label={`${p.key} 옮기기`} type="checkbox" checked={p.move} disabled={!!busy || !!result} onChange={e => point(i, { move: e.target.checked })} /></td>
        <td><code>{p.key}</code><small className="stats-muted">체크포인트 ID {p.checkpointId}</small></td>
        <td>{p.registeredOn} → <b>{existingDevice(p.host)?.name ?? selection.servers.find(h => h.hostname === p.host)?.name ?? p.host}</b>{existingDevice(p.host) ? "" : " (새 장비)"}</td>
      </tr>)}</tbody></table></div></details>}
      <div className="stats-toolbar"><input aria-label="키 검색" placeholder="Redis 키 또는 이름 검색" value={filter} onChange={e => setFilter(e.target.value)} /><span>{mode === "update" ? <>등록 안 된 키 {selection.points.length - registeredCount}개{newCount ? <> · <b className="stats-new" title="이 환경에서 체크포인트로 등록된 적이 없는 키">🆕 처음 보는 키 {newCount}개</b>(맨 위)</> : ""}</> : `발견 ${selection.points.length - registeredCount}개`} · 선택 {count}개{moveCount ? ` · 옮기기 ${moveCount}개` : ""} · 장비 {deviceCount}개</span><button className="toolbtn" disabled={!!busy || !!result} onClick={() => edit({ points: selection.points.map(p => ({ ...p, selected: !!p.host && !p.registered && !numberLocked(p) })) })}>서버가 정해진 키 모두 선택</button><button className="toolbtn" disabled={!!busy || !!result} onClick={() => edit({ points: selection.points.map(p => ({ ...p, selected: false })) })}>모두 해제</button></div>
      <div className="stats-table-wrap"><table className="stats-table"><thead><tr><th>등록</th><th>Redis 키 / 체크포인트 이름</th><th>대상 Device</th><th>자료형 / 주기</th></tr></thead><tbody>{listed.map(({ p, i }) => (!filter || (p.key + p.name).toLowerCase().includes(filter.toLowerCase())) && <tr key={p.key} className={isNew(p) ? "stats-new-row" : undefined}>
        <td><input aria-label={`${p.key} 등록`} type="checkbox" checked={p.selected && !numberLocked(p)} disabled={!!busy || !!result || numberLocked(p)} title={numberLocked(p) ? numberLockedWhy : undefined} onChange={e => point(i, { selected: e.target.checked })} /></td>
        <td>{isNew(p) && <span className="stats-new" title="이 환경에서 체크포인트로 등록된 적이 없는 키">🆕 처음 보는 키</span>}<code>{p.key}</code><input aria-label={`${p.key} 이름`} disabled={!!busy || !!result} value={p.name} onChange={e => point(i, { name: e.target.value })} />{p.deleted && <small className="stats-muted">이전에 등록했다가 삭제된 키 — 기본 선택, 새로 생성합니다</small>}{unplaced(p) && p.host && hints[firstToken(p.key) || ""]?.host === p.host && <small className="stats-badge">hosts 로 추천</small>}{p.remembered && <small className="stats-muted">전에 쓴 포인트 이름·주기를 채움</small>}{p.warning && <small className="stats-warning">{p.warning}</small>}</td>
        <td><select aria-label={`${p.key} 대상 서버`} value={p.host} disabled={!!busy || !!result || !p.key.includes("{")} onChange={e => point(i, { host: e.target.value })}><option value="">서버 고르기 (안 고르면 빠짐)</option>{selection.servers.filter(h => p.key.includes("{") ? h.hostname !== "common" : h.hostname === "common").map(h => <option key={h.hostname} value={h.hostname}>{h.name} ({h.hostname})</option>)}</select></td>
        <td><select aria-label={`${p.key} 자료형`} value={p.format} disabled={!!busy || !!result || !!p.templateId} onChange={e => { const format = Number(e.target.value); point(i, p.badValue && format !== 4 ? { format, selected: false } : { format }); }}>{p.templateId !== 0 && p.format < 3 && <option value={p.format}>제품이 정한 자료형: {formatNames[p.format]}</option>}<option value={3}>숫자 (Double)</option><option value={4}>문자열 (String)</option></select><input aria-label={`${p.key} 주기 ms`} type="number" min={1000} step={1000} value={p.interval} disabled={!!busy || !!result} onChange={e => point(i, { interval: Number(e.target.value) })} /><small>ms {p.measure && `· ${p.measure}`}</small></td>
      </tr>)}</tbody></table></div>
    </>}
    {result && !plan && <div className={result.verified ? "stats-success stats-result" : "stats-warning stats-result"} role="status"><div><StatsMessage text={result.message} /></div><p className="stats-muted">등록 기록 번호 <code>{result.id}</code></p>{!result.verified && <button className="toolbtn" disabled={!!busy} onClick={() => work(result.notified ? "수집 다시 확인 중…" : "저장 상태 확인 후 남은 단계 하는 중…", async () => setResult(await StatsRetry(config, result.id)))}>{result.notified ? "수집 다시 확인" : "남은 단계 마저 하기 (이미 저장된 건 다시 만들지 않음)"}</button>}</div>}
    </div>
    <div className="stats-footer">{auto && busy && <button className="toolbtn" onClick={() => { autoCancelled.current = true; StatsCancelInspect().catch(() => {}); }}>취소</button>}<div role="status" className={!busy && !error && mode === "manage" && manageStatus && !manageStatus.warn ? "stats-status-ok" : undefined}>{(busy && (progress || busy)) || (error && <StatsMessage text={error} />) || (mode === "manage" && manageStatus?.text) || (checked ? "등록 전 검사 통과 — 이름(1~100자)·서버·자료형·주기·장비 탐색기·Collector 클러스터를 확인했습니다. 아직 저장하지 않았습니다." : "")}</div>{plan && (mode === "ask" || mode === "manage") && <><button className="toolbtn" disabled={!!busy} onClick={() => mode === "manage" ? work("다시 조회 중…", async () => { await reinspect(); setMode(""); }) : setMode("")}>← 처음 선택으로</button><button className="toolbtn" disabled={!!busy} onClick={() => { setPlan(null); setSelection(null); setChecked(false); setMode(""); }}>연결 설정</button></>}{plan && selection && !result && (mode === "new" || mode === "update") && <><button className="toolbtn" disabled={!!busy} onClick={() => { setPlan(null); setSelection(null); setChecked(false); setMode(""); }}>연결 설정</button><button className="toolbtn" title="저장하지 않고 이름(1~100자)·서버 배치·자료형·주기·장비 탐색기·Collector 클러스터만 확인합니다" disabled={!!busy || !(count + moveCount)} onClick={() => work("등록 전 검사 중…", async () => { setChecked(false); await StatsValidate(uniqueServers(selection)); setChecked(true); })}>등록 전 검사</button><button className="toolbtn primary" disabled={!!busy || !checked || !(count + moveCount)} onClick={() => work("등록 중…", async () => { setChecked(false); setResult(await StatsRegister(uniqueServers(selection))); })}>{mode === "update" ? `업데이트: 새 장비 ${newDeviceCount}개 · 체크포인트 ${count}개 추가${moveCount ? ` · ${moveCount}개 옮기기` : ""}` : `장비 ${deviceCount}개 · 체크포인트 ${count}개 등록`}</button></>}</div>
  </div></div>;
}
