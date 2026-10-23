import { useEffect, useState } from "react";
import { StatsInspect, StatsValidate, StatsRegister, StatsRetry, StatsImportPatterns, StatsConnections, StatsSaveConnection, StatsClusterConnections, StatsSetClusterConnection, StatsTesterPrefix } from "../../wailsjs/go/main/App";
import { EventsOn } from "../../wailsjs/runtime";
import { statsreg } from "../../wailsjs/go/models";
import { useEsc } from "../useEsc";
import StatsConnectionFields, { KafkaRow, defaultStatsConfig, statsConnection } from "./StatsConnectionFields";
import StatsDiscoverReview, { DiscoverResult, discoverFromCluster } from "./StatsDiscoverReview";
import StatsManage from "./StatsManage";
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
  useEffect(() => EventsOn("statsProgress", (m: string) => setProgress(m)), []);
  // Tester-only build (statsreg.TesterPrefix): "" in a product build.
  const [tester, setTester] = useState("");
  useEffect(() => { StatsTesterPrefix().then(setTester).catch(() => {}); }, []);
  const [saved,setSaved] = useState<statsreg.SavedConnection[]>([]);
  const [clusterConn, setClusterConn] = useState<statsreg.SavedConnection | null>(null);
  const [discovery, setDiscovery] = useState<DiscoverResult | null>(null);
  const choices = clusterConn ? [clusterConn, ...saved] : saved;
  function applySaved(c: statsreg.SavedConnection) { setConfig(c.config); if (c.kafkaRows?.length) setKafkaRows(c.kafkaRows.map(r => ({ hostname: r.hostname, ip: r.ip, port: r.port }))); }
  // The most recent connection that inspected successfully is filled in.
  // The cluster's own connection wins over the most recent one.
  useEffect(() => {
    Promise.all([StatsConnections().catch(() => []), cluster ? StatsClusterConnections().catch(() => ({})) : Promise.resolve({})]).then(([list, byCluster]) => {
      const own = cluster ? (byCluster as Record<string, statsreg.SavedConnection>)[cluster.id] : undefined;
      setSaved(list || []);
      setClusterConn(own || null);
      const first = own || list?.[0];
      if (first) applySaved(first);
    });
  }, []);

  function edit(patch: Partial<statsreg.Selection>) { setSelection(s => s ? new statsreg.Selection({ ...s, ...patch }) : s); setChecked(false); setError(""); }
  function point(index: number, patch: Partial<statsreg.Point>) { if (selection) edit({ points: selection.points.map((p, i) => i === index ? { ...p, ...patch } : p) }); }
  async function work(label: string, fn: () => Promise<void>) { setError(""); setProgress(""); setBusy(label); try { await fn(); } catch (e) { setError(String(e)); } finally { setBusy(""); setProgress(""); } }
  const connectionConfig = () => statsConnection(config, kafkaRows);
  function adopt(p: statsreg.Plan) { setPlan(p); setAskGroup(p.existing?.[0]?.groupId || 0); setSelection(new statsreg.Selection({ planId: p.id, groupName: p.groupName, explorerId: p.explorers[0].id, clusterId: p.clusters[0].id, servers: p.servers, points: p.points.map(pt => pt.registered ? { ...pt, selected: false } : pt), updateGroupId: 0 })); }
  // reinspect: 「연결 확인」 again with the same connection (registrations changed).
  async function reinspect() { const p = await StatsInspect(config); adopt(p); return p; }

  return <div className="scrim stats-scrim"><div className="stats-dialog" role="dialog" aria-modal="true" aria-labelledby="stats-title" onKeyDown={e => { e.stopPropagation(); if (e.key === "Escape" && !busy && !inner) { e.preventDefault(); onClose(); } }}>
    <div className="stats-head"><div><h2 id="stats-title">MK119 v10 리소스 수집 등록</h2><p>연결 확인 → 「새로 등록」(서버별 체크포인트 선택 → 등록 전 검사 → 등록: 장비 → 반영 대기 → 체크포인트 → 수집 확인) 또는 「기존 등록 관리」(이름·주기 수정, 장비·그룹 삭제)</p>{tester && <p className="stats-warning" role="note">🧪 테스트 빌드: {tester} — 그룹·장비 이름에 {tester} 접두어가 붙고, {tester} 그룹만 기존 등록으로 봅니다</p>}</div><button className="toolbtn" disabled={!!busy} onClick={onClose}>닫기</button></div>
    <div className="stats-body">
    {!plan && <>
      {cluster && <p className="stats-muted">{clusterConn ? <>📦 클러스터 <b>{cluster.name}</b>에 저장된 접속 정보를 채웠습니다. 연결 확인에 성공하면 고친 내용도 클러스터에 저장됩니다.</> : <>📦 클러스터 <b>{cluster.name}</b>에 저장된 접속 정보가 없습니다. 클러스터 편집에서 「MK119 리소스 수집 등록」을 체크하면 저장해 둘 수 있습니다.</>}</p>}
      {choices.length > 1 && <label className="stats-recent">최근 연결<select disabled={!!busy} defaultValue={0} onChange={e => applySaved(choices[Number(e.target.value)])}>{choices.map((c, i) => <option key={i} value={i}>{c === clusterConn ? `📦 ${cluster?.name} · ` : ""}{c.config.dbHost}:{c.config.dbPort} · Redis {c.config.redisHost} · {c.savedAt.slice(0, 16).replace("T", " ")}</option>)}</select></label>}
      {discovery && <StatsDiscoverReview result={discovery} config={config} baseline={defaultStatsConfig(initialHost)} kafkaRows={kafkaRows} onApply={(c, rows) => { setConfig(c); if (rows) setKafkaRows(() => rows); setDiscovery(null); }} onClose={() => setDiscovery(null)} />}
      <StatsConnectionFields bad={parseStatsMessage(error).field} config={config} setConfig={setConfig} kafkaRows={kafkaRows} setKafkaRows={setKafkaRows} disabled={!!busy} onDiscover={cluster ? () => work("서버에서 접속 정보 읽는 중…", async () => setDiscovery(await discoverFromCluster(cluster.id))) : undefined} discovering={busy === "서버에서 접속 정보 읽는 중…"} />
      <p className="stats-muted">연결 확인에 성공한 접속 정보는 비밀번호를 포함해 이 PC의 ~/.rtaskmgr/stats-connections.json에 저장되고 다음에 자동으로 채워집니다. Kafka 토픽: liz.message.pipeline</p>
      <button className="toolbtn primary" disabled={!!busy} onClick={() => work("연결 및 Redis 키 분석 중…", async () => { const c = connectionConfig(); setConfig(c); const p = await StatsInspect(c); StatsSaveConnection(new statsreg.SavedConnection({ config: c, kafkaRows, savedAt: "" })).catch(() => {}); if (cluster && clusterConn) StatsSetClusterConnection(cluster.id, new statsreg.SavedConnection({ config: c, kafkaRows, savedAt: "" })).catch(() => {}); adopt(p); setResult(null); setMode(""); })}>연결 확인 및 liz.stats.* 조회</button>
      <details className="stats-recovery"><summary>이전 등록 이어서 진행 / 수집 다시 확인</summary><p className="stats-muted">등록이 중간에 멈췄을 때(창을 닫음, 알림 실패, 반영 대기 중 중단 등) 등록 결과에 나온 「이어서 진행 번호」를 넣으면 이미 저장된 것은 다시 만들지 않고 남은 단계만 합니다. 다 끝난 등록이면 수집 확인만 다시 합니다. 번호별 기록은 이 PC의 ~/.rtaskmgr/stats-registration 폴더에 있습니다.</p><label>이어서 진행 번호<input value={retryID} onChange={e => setRetryID(e.target.value)} /></label><button className="toolbtn" disabled={!!busy || !retryID} onClick={() => work("저장 상태 확인 후 이어서 진행…", async () => { const c = connectionConfig(); setConfig(c); setResult(await StatsRetry(c, retryID)); })}>이어서 진행 (DB 재등록 없음)</button><button className="toolbtn" title="그 등록에 쓴 체크포인트 이름·주기를 키 모양별로 이 PC에 기억해, 다음 조회 때 같은 모양의 키에 미리 채웁니다" disabled={!!busy || !retryID} onClick={() => work("이름·주기 기억 중…", async () => { const ev = await StatsImportPatterns(retryID); setError(`이름·주기 ${ev.changes?.length || 0}건 기억${ev.notes?.length ? ` · 장비마다 이름이 다른 키 ${ev.notes.length}종(가장 많이 쓴 이름 채택)` : ""}`); })}>이 등록에 쓴 이름·주기 기억</button></details>
    </>}
    {plan && selection && mode === "" && <div className="stats-update-ask">
      <p><b>무엇을 할까요?</b></p>
      {plan.existing?.length ? <ul>{plan.existing.map(g => <li key={g.groupId}>{g.name} — 장비 {g.devices.length}대 · 체크포인트 {g.points}개</li>)}</ul> : null}
      {plan.existing?.length > 0 && plan.orphans > 0 && <p className="stats-warning">그룹에 없는 리소스 수집 장비 {plan.orphans}대 — 「기존 등록 관리」에서 확인·정리할 수 있습니다.</p>}
      <div className="stats-update-buttons"><button className="toolbtn primary" onClick={() => setMode(plan.existing?.length ? "ask" : "new")}>{plan.existing?.length ? "새로 등록 / 새 키 추가" : "새로 등록"}</button><button className="toolbtn" disabled={!plan.existing?.length && !plan.orphans} onClick={() => { setResult(null); setMode("manage"); }}>기존 등록 관리 (이름·주기 수정, 삭제)</button></div>
      {!plan.existing?.length && plan.orphans > 0 && <p className="stats-warning">관리할 그룹은 없지만 그룹에 없는 리소스 수집 장비가 {plan.orphans}대 있습니다(admin 에서 그룹만 지운 경우 등) — 「기존 등록 관리」에서 확인·정리하세요.</p>}
      {!plan.existing?.length && !plan.orphans && <p className="stats-muted">「기존 등록 관리」를 쓸 수 없습니다 — 이 환경에 MK119 v10 System Stats 체크포인트가 든 그룹이 없습니다{tester ? ` (테스트 빌드는 ${tester}_ 로 시작하는 그룹만 봅니다)` : ""}.</p>}
    </div>}
    {plan && selection && mode === "ask" && <div className="stats-update-ask">
      <p><b>이 환경에 이미 등록된 작업이 있습니다.</b></p>
      <ul>{plan.existing.map(g => <li key={g.groupId}>{g.name} — 장비 {g.devices.length}개 · 체크포인트 {g.points}개{g.vanished ? ` · 원본 키가 사라진 체크포인트 ${g.vanished}개` : ""}</li>)}</ul>
      <p>기존에 등록된 작업을 업데이트하겠습니까? 새로 생긴 키를 추가하고, 다른 서버로 옮겨 간 모듈의 체크포인트는 지금 서버의 장비로 옮깁니다(선택). 원본 키가 사라진 체크포인트는 그대로 둡니다.</p>
      {plan.existing.length > 1 && <label>업데이트할 그룹<select value={askGroup} onChange={e => setAskGroup(Number(e.target.value))}>{plan.existing.map(g => <option key={g.groupId} value={g.groupId}>{g.name} (ID {g.groupId})</option>)}</select></label>}
      <div className="stats-update-buttons"><button className="toolbtn primary" onClick={() => { const g = plan.existing.find(x => x.groupId === askGroup); if (g) { edit({ updateGroupId: g.groupId, groupName: g.name, clusterId: g.clusterId || selection.clusterId, points: selection.points.map(p => ({ ...p, move: p.moved && p.checkpointId > 0 && p.moveGroupId === g.groupId })) }); setMode("update"); } }}>예, 업데이트</button><button className="toolbtn" onClick={() => { edit({ updateGroupId: 0, groupName: plan.groupName, points: selection.points.map(p => ({ ...p, move: false })) }); setMode("new"); }}>아니오, 새 그룹으로 등록</button></div>
    </div>}
    {plan && mode === "manage" && <StatsManage plan={plan} busy={busy} work={work} reinspect={reinspect} onDialog={setInner} />}
    {plan && selection && (mode === "new" || mode === "update") && <>
      {result && <div className={result.verified ? "stats-success stats-result" : "stats-warning stats-result"} role="status"><p><b>등록 결과</b> — {result.message}</p>{result.notified && result.checkpointIds.length + (result.movedIds?.length || 0) > 0 && <p>수집 확인: {result.collected}/{result.checkpointIds.length + (result.movedIds?.length || 0)}개{result.missing?.length ? ` · 값이 안 들어온 체크포인트 ${result.missing.length}개` : ""}</p>}<p>이어서 진행 번호: <code>{result.id}</code> <small>등록이 중간에 멈췄을 때 「연결 설정」 → 「이전 등록 이어서 진행」에 넣으면 남은 단계만 다시 합니다(이 PC에 기록됨).</small></p>{!result.verified && <button className="toolbtn" disabled={!!busy} onClick={() => work(result.notified ? "수집 다시 확인 중…" : "저장 상태 확인 후 이어서 진행…", async () => setResult(await StatsRetry(config, result.id)))}>{result.notified ? "수집 다시 확인" : "이어서 진행 (DB 재등록 없음)"}</button>}</div>}
      <fieldset disabled={!!busy || !!result} className="stats-settings"><legend>{mode === "update" ? "업데이트 대상과 새 장비 이름" : "등록 위치와 이름"}</legend>
        <label>Device Group{mode === "update" ? " (업데이트)" : ""}<input value={selection.groupName} disabled={mode === "update"} onChange={e => edit({ groupName: e.target.value })} /></label>
        <label>장비 탐색기<select value={selection.explorerId} onChange={e => edit({ explorerId: Number(e.target.value) })}>{plan.explorers.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}</select></label>
        <label>Collector 클러스터{mode === "update" ? " (그룹 기존 장비와 같게)" : ""}<select value={selection.clusterId} disabled={mode === "update" && !!plan.existing.find(x => x.groupId === selection.updateGroupId)?.clusterId} onChange={e => edit({ clusterId: Number(e.target.value) })}>{plan.clusters.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}</select></label>
        {selection.servers.map((h, i) => <label key={h.hostname}>{h.hostname === "common" ? "공통 항목" : `실제 호스트: ${h.hostname}`}{existingDevice(h.hostname) ? " — 기존 장비에 추가" : mode === "update" && selection.points.some(p => p.selected && p.host === h.hostname) || selection.points.some(p => p.move && p.host === h.hostname) ? " — 새 장비" : ""}<input value={existingDevice(h.hostname)?.name ?? h.name} disabled={!!existingDevice(h.hostname)} onChange={e => edit({ servers: selection.servers.map((v, n) => n === i ? { ...v, name: e.target.value } : v) })} /><small>{selection.points.filter(p => p.selected && p.host === h.hostname).length}개 선택{selection.points.some(p => p.move && p.host === h.hostname) ? ` · ${selection.points.filter(p => p.move && p.host === h.hostname).length}개 옮겨 옴` : ""}{selection.points.some(p => !p.selected && !p.registered && p.host === h.hostname) && <> · <button type="button" className="linkbtn" onClick={() => edit({ points: selection.points.map(p => p.host === h.hostname && !p.registered ? { ...p, selected: true } : p) })}>이 호스트 모두 선택{selection.points.some(p => p.deleted && p.host === h.hostname) ? "(삭제됐던 키 포함)" : ""}</button></>}{selection.points.some(p => p.selected && p.host === h.hostname) && <> · <button type="button" className="linkbtn" onClick={() => edit({ points: selection.points.map(p => p.host === h.hostname ? { ...p, selected: false } : p) })}>이 호스트 제외</button></>}</small></label>)}
      </fieldset>
      <p className="stats-muted">ETC / MK119 System / MK119 Cache / MK119 v10 System Stats · 선택한 체크포인트가 있는 장비만 생성합니다. 이름이 기존 그룹·장비와 같아도 등록합니다(이미 등록된 키만 막음).</p>
      {plan.redisInfo && <p className="stats-muted"><StatsMessage text={plan.redisInfo} /></p>}
      {registeredCount > 0 && <p className="stats-muted">이미 등록된 키 {registeredCount}개는 목록에서 숨겼습니다(중복 등록 방지){movedCount ? ` · 그중 ${movedCount}개는 등록된 장비와 지금 서버가 달라 옮겨진 것으로 보입니다${movable.length ? " — 아래 「옮겨 간 모듈」에서 옮길 수 있습니다" : "(이 그룹 업데이트가 아니거나 체크포인트가 여러 개라 그대로 둠)"}` : ""}.</p>}
      {plan.patternInfo && <p className="stats-muted">{plan.patternInfo}</p>}
      {badCount > 0 && <details className="stats-recovery"><summary>값이 숫자가 아니라 뺀 키 {badCount}개 — 왜 빼나요?</summary><p className="stats-muted">숫자로 수집하는 키인데 지금 값이 빈 값이나 글자입니다(예: SNMP 요청이 아직 없어 빈 값인 API 분당 요청 수). 이대로 등록하면 Collector가 매 주기 이 키에서 오류를 내며 값을 하나도 모으지 못하고, 오류마다 0.1초씩 쉬어 다른 MK119 체크포인트 수집까지 늦어집니다(trunk 실측: 76분에 오류 2,730건). 그래서 빼 두는 것이 안전합니다. 값이 숫자가 되면 다음 「새 키 추가」에서 다시 보이니 그때 넣으세요.</p><ul>{selection.points.filter(p => p.badValue && !p.registered).map(p => <li key={p.key}><code>{p.key}</code></li>)}</ul></details>}
      {(plan.warnings || []).map((w, i) => <p className="stats-warning" key={i}>{w}</p>)}
      {unresolvedTokens.length > 0 && <details className="stats-recovery"><summary>어느 서버인지 모르는 {"{ }"} 값 — 남은 {unresolvedLeft}/{unresolvedTokens.length}개: 서버를 고르면 그 값이 든 키를 한꺼번에 그 서버에 놓습니다(안 고르면 그 키는 빠짐)</summary><div className="stats-settings">{unresolvedTokens.map(token => <label key={token}>{`{${token}}`}<select disabled={!!busy || !!result} defaultValue="" onChange={e => { const host = e.target.value; edit({ points: selection.points.map(p => !plan.points.find(original => original.key === p.key)?.host && p.key.match(/\{([^{}]+)\}/)?.[1] === token ? { ...p, host, selected: !!host } : p) }); }}><option value="">고르지 않음 (빼기)</option>{selection.servers.filter(h => h.hostname !== "common").map(h => <option key={h.hostname} value={h.hostname}>{h.name} ({h.hostname})</option>)}</select></label>)}</div></details>}
      {movable.length > 0 && <details className="stats-recovery stats-moved" open><summary>옮겨 간 모듈 — 체크포인트 {movable.length}개 · 옮기기 {moveCount}개 선택</summary><p className="stats-muted">모듈이 다른 서버로 옮겨 가서, 등록된 장비와 지금 서버가 다른 체크포인트입니다. 체크하면 체크포인트를 지금 서버의 장비로 옮깁니다(같은 체크포인트 ID·이력 유지). 체크 해제하면 그대로 둡니다.</p><div className="stats-toolbar"><button className="toolbtn" disabled={!!busy || !!result} onClick={() => edit({ points: selection.points.map(p => movable.some(m => m.p.key === p.key) ? { ...p, move: true } : p) })}>모두 옮기기</button><button className="toolbtn" disabled={!!busy || !!result} onClick={() => edit({ points: selection.points.map(p => ({ ...p, move: false })) })}>옮기지 않음</button></div><div className="stats-table-wrap"><table className="stats-table"><thead><tr><th>옮기기</th><th>Redis 키 / 체크포인트</th><th>지금 등록된 곳 → 옮길 서버</th></tr></thead><tbody>{movable.map(({ p, i }) => <tr key={p.key}>
        <td><input aria-label={`${p.key} 옮기기`} type="checkbox" checked={p.move} disabled={!!busy || !!result} onChange={e => point(i, { move: e.target.checked })} /></td>
        <td><code>{p.key}</code><small className="stats-muted">체크포인트 ID {p.checkpointId}</small></td>
        <td>{p.registeredOn} → <b>{existingDevice(p.host)?.name ?? selection.servers.find(h => h.hostname === p.host)?.name ?? p.host}</b>{existingDevice(p.host) ? "" : " (새 장비)"}</td>
      </tr>)}</tbody></table></div></details>}
      <div className="stats-toolbar"><input aria-label="키 검색" placeholder="Redis 키 또는 이름 검색" value={filter} onChange={e => setFilter(e.target.value)} /><span>{mode === "update" ? <>등록 안 된 키 {selection.points.length - registeredCount}개{newCount ? <> · <b className="stats-new" title="이 환경에서 체크포인트로 등록된 적이 없는 키">🆕 처음 보는 키 {newCount}개</b>(맨 위)</> : ""}</> : `발견 ${selection.points.length - registeredCount}개`} · 선택 {count}개{moveCount ? ` · 옮기기 ${moveCount}개` : ""} · 장비 {deviceCount}개</span><button className="toolbtn" disabled={!!busy || !!result} onClick={() => edit({ points: selection.points.map(p => ({ ...p, selected: !!p.host && !p.registered })) })}>서버가 정해진 키 모두 선택</button><button className="toolbtn" disabled={!!busy || !!result} onClick={() => edit({ points: selection.points.map(p => ({ ...p, selected: false })) })}>모두 해제</button></div>
      <div className="stats-table-wrap"><table className="stats-table"><thead><tr><th>등록</th><th>Redis 키 / 체크포인트 이름</th><th>대상 Device</th><th>자료형 / 주기</th></tr></thead><tbody>{listed.map(({ p, i }) => (!filter || (p.key + p.name).toLowerCase().includes(filter.toLowerCase())) && <tr key={p.key} className={isNew(p) ? "stats-new-row" : undefined}>
        <td><input aria-label={`${p.key} 등록`} type="checkbox" checked={p.selected} disabled={!!busy || !!result} onChange={e => point(i, { selected: e.target.checked })} /></td>
        <td>{isNew(p) && <span className="stats-new" title="이 환경에서 체크포인트로 등록된 적이 없는 키">🆕 처음 보는 키</span>}<code>{p.key}</code><input aria-label={`${p.key} 이름`} disabled={!!busy || !!result} value={p.name} onChange={e => point(i, { name: e.target.value })} />{p.deleted && <small className="stats-muted">이전에 등록했다가 삭제된 키 — 기본 선택, 새로 생성합니다</small>}{p.remembered && <small className="stats-muted">전에 쓴 이름·주기를 채움</small>}{p.warning && <small className="stats-warning">{p.warning}</small>}</td>
        <td><select aria-label={`${p.key} 대상 서버`} value={p.host} disabled={!!busy || !!result || !p.key.includes("{")} onChange={e => point(i, { host: e.target.value })}><option value="">서버 고르기 (안 고르면 빠짐)</option>{selection.servers.filter(h => p.key.includes("{") ? h.hostname !== "common" : h.hostname === "common").map(h => <option key={h.hostname} value={h.hostname}>{h.name} ({h.hostname})</option>)}</select></td>
        <td><select aria-label={`${p.key} 자료형`} value={p.format} disabled={!!busy || !!result || !!p.templateId} onChange={e => point(i, { format: Number(e.target.value) })}>{p.templateId !== 0 && p.format < 3 && <option value={p.format}>제품 정의대로: {formatNames[p.format]}</option>}<option value={3}>숫자 (Double)</option><option value={4}>문자열 (String)</option></select><input aria-label={`${p.key} 주기 ms`} type="number" min={1000} step={1000} value={p.interval} disabled={!!busy || !!result} onChange={e => point(i, { interval: Number(e.target.value) })} /><small>ms {p.measure && `· ${p.measure}`}</small></td>
      </tr>)}</tbody></table></div>
    </>}
    {result && !plan && <div className={result.verified ? "stats-success stats-result" : "stats-warning stats-result"} role="status"><p>{result.message}</p><p>이어서 진행 번호: <code>{result.id}</code></p>{!result.verified && <button className="toolbtn" disabled={!!busy} onClick={() => work(result.notified ? "수집 다시 확인 중…" : "저장 상태 확인 후 이어서 진행…", async () => setResult(await StatsRetry(config, result.id)))}>{result.notified ? "수집 다시 확인" : "이어서 진행 (DB 재등록 없음)"}</button>}</div>}
    </div>
    <div className="stats-footer"><div role="status">{(busy && (progress || busy)) || (error && <StatsMessage text={error} />) || (checked ? "등록 전 검사 통과 — 이름(1~100자)·서버·자료형·주기·장비 탐색기·Collector 클러스터를 확인했습니다. 아직 저장하지 않았습니다." : "")}</div>{plan && (mode === "ask" || mode === "manage") && <><button className="toolbtn" disabled={!!busy} onClick={() => mode === "manage" ? work("다시 조회 중…", async () => { await reinspect(); setMode(""); }) : setMode("")}>← 처음 선택으로</button><button className="toolbtn" disabled={!!busy} onClick={() => { setPlan(null); setSelection(null); setChecked(false); setMode(""); }}>연결 설정</button></>}{plan && selection && !result && (mode === "new" || mode === "update") && <><button className="toolbtn" disabled={!!busy} onClick={() => { setPlan(null); setSelection(null); setChecked(false); setMode(""); }}>연결 설정</button><button className="toolbtn" title="저장하지 않고 이름(1~100자)·서버 배치·자료형·주기·장비 탐색기·Collector 클러스터만 확인합니다" disabled={!!busy || !(count + moveCount)} onClick={() => work("등록 전 검사 중…", async () => { setChecked(false); await StatsValidate(uniqueServers(selection)); setChecked(true); })}>등록 전 검사</button><button className="toolbtn primary" disabled={!!busy || !checked || !(count + moveCount)} onClick={() => work("등록 중…", async () => { setChecked(false); setResult(await StatsRegister(uniqueServers(selection))); })}>{mode === "update" ? `업데이트: 새 장비 ${newDeviceCount}개 · 체크포인트 ${count}개 추가${moveCount ? ` · ${moveCount}개 옮기기` : ""}` : `장비 ${deviceCount}개 · 체크포인트 ${count}개 등록`}</button></>}</div>
  </div></div>;
}
