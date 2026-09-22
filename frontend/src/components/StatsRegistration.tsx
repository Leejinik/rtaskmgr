import { useEffect, useState } from "react";
import { StatsInspect, StatsValidate, StatsRegister, StatsRetry, StatsImportPatterns, StatsConnections, StatsSaveConnection } from "../../wailsjs/go/main/App";
import { EventsOn } from "../../wailsjs/runtime";
import { statsreg } from "../../wailsjs/go/models";
import { useEsc } from "../useEsc";
import "./StatsRegistration.css";

interface KafkaRow { hostname: string; ip: string; port: number }

// liz_server can hold the same hostname more than once; the server side rejects
// a repeated host, so only the first row of each hostname is sent.
function uniqueServers(s: statsreg.Selection): statsreg.Selection {
  const seen = new Set<string>();
  return new statsreg.Selection({ ...s, servers: s.servers.filter(h => !seen.has(h.hostname) && !!seen.add(h.hostname)) });
}

export default function StatsRegistration({ initialHost, onClose }: { initialHost: string; onClose: () => void }) {
  const [config, setConfig] = useState<statsreg.Config>({ dbHost: initialHost, dbPort: 3306, dbUser: "root", dbPassword: "", redisHost: initialHost, redisPort: 5000, redisUser: "", redisPassword: "", redisDB: 0, brokers: "", kafkaSecurity: "PLAINTEXT", kafkaUser: "", kafkaPassword: "", kafkaHosts: "" });
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
  // "" = ask whether to update an existing registration; "new" / "update" = chosen.
  const [mode, setMode] = useState<"" | "new" | "update">("");
  const [askGroup, setAskGroup] = useState(0);
  const count = selection?.points.filter(p => p.selected).length || 0;
  const deviceCount = new Set(selection?.points.filter(p => p.selected).map(p => p.host)).size;
  const group = mode === "update" ? plan?.existing?.find(g => g.groupId === selection?.updateGroupId) : undefined;
  const existingDevice = (host: string) => group?.devices.filter(d => d.host === host).sort((a, b) => b.points - a.points)[0];
  const registeredCount = plan?.points.filter(p => p.registered).length || 0;
  const movedCount = plan?.points.filter(p => p.moved).length || 0;
  const newDeviceCount = new Set(selection?.points.filter(p => p.selected && !existingDevice(p.host)).map(p => p.host)).size;
  const unresolvedTokens = Array.from(new Set((plan?.points || []).filter(p => !p.host).map(p => p.key.match(/\{([^{}]+)\}/)?.[1]).filter((v): v is string => !!v)));
  useEsc(onClose, !busy);
  useEffect(() => EventsOn("statsProgress", (m: string) => setProgress(m)), []);
  const [saved, setSaved] = useState<statsreg.SavedConnection[]>([]);
  function applySaved(c: statsreg.SavedConnection) { setConfig(c.config); if (c.kafkaRows?.length) setKafkaRows(c.kafkaRows.map(r => ({ hostname: r.hostname, ip: r.ip, port: r.port }))); }
  // The most recent connection that inspected successfully is filled in.
  useEffect(() => { StatsConnections().then(list => { setSaved(list || []); if (list?.length) applySaved(list[0]); }).catch(() => {}); }, []);

  function edit(patch: Partial<statsreg.Selection>) { setSelection(s => s ? new statsreg.Selection({ ...s, ...patch }) : s); setChecked(false); setError(""); }
  function point(index: number, patch: Partial<statsreg.Point>) { if (selection) edit({ points: selection.points.map((p, i) => i === index ? { ...p, ...patch } : p) }); }
  async function work(label: string, fn: () => Promise<void>) { setError(""); setProgress(""); setBusy(label); try { await fn(); } catch (e) { setError(String(e)); } finally { setBusy(""); setProgress(""); } }
  function connectionConfig(): statsreg.Config {
    const rows = kafkaRows.map(r => ({ hostname: r.hostname.trim(), ip: r.ip.trim(), port: r.port }));
    if (rows.some(r => !r.hostname || !r.ip || !Number.isInteger(r.port) || r.port < 1 || r.port > 65535)) throw new Error("Kafka의 호스트 이름, IP, 포트(1~65535)를 모두 입력하세요.");
    const names = rows.map(r => r.hostname.toLowerCase().replace(/\.$/, ""));
    if (new Set(names).size !== names.length) throw new Error("Kafka 호스트 이름이 중복됩니다.");
    const address = (r: KafkaRow) => `${r.ip.includes(":") && !r.ip.startsWith("[") ? `[${r.ip}]` : r.ip}:${r.port}`;
    return { ...config, brokers: rows.map(r => `${r.hostname}:${r.port}`).join(","), kafkaHosts: rows.map(r => `${r.hostname} = ${address(r)}`).join("\n") };
  }
  const input = (label: string, key: keyof statsreg.Config, type = "text") => <label>{label}<input type={type} value={String(config[key])} autoComplete="off" onChange={e => setConfig({ ...config, [key]: type === "number" ? Number(e.target.value) : e.target.value })} /></label>;

  return <div className="scrim stats-scrim"><div className="stats-dialog" role="dialog" aria-modal="true" aria-labelledby="stats-title" onKeyDown={e => { e.stopPropagation(); if (e.key === "Escape" && !busy) { e.preventDefault(); onClose(); } }}>
    <div className="stats-head"><div><h2 id="stats-title">MK119 v10 리소스 수집 등록</h2><p>연결 확인 → 서버별 체크포인트 선택 → 등록 전 검사 → 등록(장비 → 반영 대기 → 체크포인트 → 수집 확인)</p></div><button className="toolbtn" disabled={!!busy} onClick={onClose}>닫기</button></div>
    <div className="stats-body">
    {!plan && <>
      {saved.length > 1 && <label className="stats-recent">최근 연결<select disabled={!!busy} defaultValue={0} onChange={e => applySaved(saved[Number(e.target.value)])}>{saved.map((c, i) => <option key={i} value={i}>{c.config.dbHost}:{c.config.dbPort} · Redis {c.config.redisHost} · {c.savedAt.slice(0, 16).replace("T", " ")}</option>)}</select></label>}
      <div className="stats-connections">
        <fieldset disabled={!!busy}><legend>MariaDB · liz</legend>{input("서버 주소", "dbHost")}{input("포트", "dbPort", "number")}{input("계정", "dbUser")}{input("비밀번호", "dbPassword", "password")}</fieldset>
        <fieldset disabled={!!busy}><legend>Redis</legend>{input("서버 주소 (Cluster면 전부, 쉼표로)", "redisHost")}{input("포트", "redisPort", "number")}{input("ACL 계정 (선택)", "redisUser")}{input("비밀번호", "redisPassword", "password")}{input("DB 번호", "redisDB", "number")}</fieldset>
        <fieldset disabled={!!busy}><legend>Kafka 보안</legend><label>보안<select value={config.kafkaSecurity} onChange={e => setConfig({ ...config, kafkaSecurity: e.target.value })}><option>PLAINTEXT</option><option>SSL</option><option>SASL_PLAINTEXT</option><option>SASL_SSL</option></select></label>{config.kafkaSecurity.startsWith("SASL") && <>{input("SASL/PLAIN 계정", "kafkaUser")}{input("비밀번호", "kafkaPassword", "password")}</>}<p className="stats-muted">아래에 Kafka 서버를 추가하세요. 입력한 호스트 이름을 IP와 포트에 바인딩합니다.</p></fieldset>
      </div>
      <fieldset disabled={!!busy} className="stats-host-bindings"><legend>Kafka 서버</legend>{kafkaRows.map((row, index) => <div className="stats-kafka-row" key={index}><label>호스트 이름<input aria-label={`Kafka ${index + 1} 호스트 이름`} placeholder="server_1 또는 server01" value={row.hostname} onChange={e => setKafkaRows(rows => rows.map((r, i) => i === index ? { ...r, hostname: e.target.value } : r))} /></label><label>IP<input aria-label={`Kafka ${index + 1} IP`} placeholder="192.0.2.11" value={row.ip} onChange={e => setKafkaRows(rows => rows.map((r, i) => i === index ? { ...r, ip: e.target.value } : r))} /></label><label>포트<input aria-label={`Kafka ${index + 1} 포트`} type="number" min={1} max={65535} value={row.port} onChange={e => setKafkaRows(rows => rows.map((r, i) => i === index ? { ...r, port: Number(e.target.value) } : r))} /></label><button className="toolbtn" aria-label={`Kafka ${index + 1} 삭제`} disabled={kafkaRows.length === 1} onClick={() => setKafkaRows(rows => rows.filter((_, i) => i !== index))}>−</button></div>)}<button className="toolbtn" onClick={() => setKafkaRows(rows => [...rows, { hostname: "", ip: "", port: 9092 }])}>+ 서버 추가</button><p className="stats-muted">호스트 이름은 Kafka가 알려주는 이름 그대로 입력하세요. 연결 확인·알림 전송·재시도에 동일하게 적용합니다.</p></fieldset>
      <p className="stats-muted">연결 확인에 성공한 접속 정보는 비밀번호를 포함해 이 PC의 ~/.rtaskmgr/stats-connections.json에 저장되고 다음에 자동으로 채워집니다. Kafka 토픽: liz.message.pipeline</p>
      <button className="toolbtn primary" disabled={!!busy} onClick={() => work("연결 및 Redis 키 분석 중…", async () => { const c = connectionConfig(); setConfig(c); const p = await StatsInspect(c); StatsSaveConnection(new statsreg.SavedConnection({ config: c, kafkaRows, savedAt: "" })).catch(() => {}); setPlan(p); setMode(p.existing?.length ? "" : "new"); setAskGroup(p.existing?.[0]?.groupId || 0); setSelection(new statsreg.Selection({ planId: p.id, groupName: p.groupName, explorerId: p.explorers[0].id, clusterId: p.clusters[0].id, servers: p.servers, points: p.points.map(pt => pt.registered ? { ...pt, selected: false } : pt), updateGroupId: 0 })); })}>연결 확인 및 liz.stats.* 조회</button>
      <details className="stats-recovery"><summary>이전 등록 이어서 진행 / 수집 다시 확인</summary><label>복구 ID<input value={retryID} onChange={e => setRetryID(e.target.value)} /></label><button className="toolbtn" disabled={!!busy || !retryID} onClick={() => work("저장 상태 확인 후 이어서 진행…", async () => { const c = connectionConfig(); setConfig(c); setResult(await StatsRetry(c, retryID)); })}>이어서 진행 (DB 재등록 없음)</button><button className="toolbtn" disabled={!!busy || !retryID} onClick={() => work("등록 패턴 기억 중…", async () => { const ev = await StatsImportPatterns(retryID); setError(`등록 패턴 ${ev.changes?.length || 0}건 기억${ev.notes?.length ? ` · 장비마다 이름이 다른 키 ${ev.notes.length}종(다수 이름 채택)` : ""}`); })}>이 등록의 이름 패턴 기억</button></details>
    </>}
    {plan && selection && mode === "" && <div className="stats-update-ask">
      <p><b>이 환경에 이미 등록된 작업이 있습니다.</b></p>
      <ul>{plan.existing.map(g => <li key={g.groupId}>{g.name} — 장비 {g.devices.length}개 · 체크포인트 {g.points}개{g.vanished ? ` · 원본 키가 사라진 체크포인트 ${g.vanished}개` : ""}</li>)}</ul>
      <p>기존에 등록된 작업을 업데이트하겠습니까? 새로 생긴 키만 추가하고, 기존 체크포인트는 원본 키가 사라진 것까지 그대로 둡니다.</p>
      {plan.existing.length > 1 && <label>업데이트할 그룹<select value={askGroup} onChange={e => setAskGroup(Number(e.target.value))}>{plan.existing.map(g => <option key={g.groupId} value={g.groupId}>{g.name} (ID {g.groupId})</option>)}</select></label>}
      <div className="stats-update-buttons"><button className="toolbtn primary" onClick={() => { const g = plan.existing.find(x => x.groupId === askGroup); if (g) { edit({ updateGroupId: g.groupId, groupName: g.name, clusterId: g.clusterId || selection.clusterId }); setMode("update"); } }}>예, 업데이트</button><button className="toolbtn" onClick={() => { edit({ updateGroupId: 0, groupName: plan.groupName }); setMode("new"); }}>아니오, 새 그룹으로 등록</button></div>
    </div>}
    {plan && selection && mode !== "" && <>
      <fieldset disabled={!!busy || !!result} className="stats-settings"><legend>{mode === "update" ? "업데이트 대상과 새 장비 이름" : "등록 위치와 이름"}</legend>
        <label>Device Group{mode === "update" ? " (업데이트)" : ""}<input value={selection.groupName} disabled={mode === "update"} onChange={e => edit({ groupName: e.target.value })} /></label>
        <label>장비 탐색기<select value={selection.explorerId} onChange={e => edit({ explorerId: Number(e.target.value) })}>{plan.explorers.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}</select></label>
        <label>Collector 클러스터{mode === "update" ? " (그룹 기존 장비와 같게)" : ""}<select value={selection.clusterId} disabled={mode === "update" && !!plan.existing.find(x => x.groupId === selection.updateGroupId)?.clusterId} onChange={e => edit({ clusterId: Number(e.target.value) })}>{plan.clusters.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}</select></label>
        {selection.servers.map((h, i) => <label key={h.hostname}>{h.hostname === "common" ? "공통 항목" : `실제 호스트: ${h.hostname}`}{existingDevice(h.hostname) ? " — 기존 장비에 추가" : mode === "update" && selection.points.some(p => p.selected && p.host === h.hostname) ? " — 새 장비" : ""}<input value={existingDevice(h.hostname)?.name ?? h.name} disabled={!!existingDevice(h.hostname)} onChange={e => edit({ servers: selection.servers.map((v, n) => n === i ? { ...v, name: e.target.value } : v) })} /><small>{selection.points.filter(p => p.selected && p.host === h.hostname).length}개 선택{selection.points.some(p => !p.selected && !p.registered && p.host === h.hostname) && <> · <button type="button" className="linkbtn" onClick={() => edit({ points: selection.points.map(p => p.host === h.hostname && !p.registered ? { ...p, selected: true } : p) })}>이 호스트 모두 선택{selection.points.some(p => p.deleted && p.host === h.hostname) ? "(삭제됐던 키 포함)" : ""}</button></>}{selection.points.some(p => p.selected && p.host === h.hostname) && <> · <button type="button" className="linkbtn" onClick={() => edit({ points: selection.points.map(p => p.host === h.hostname ? { ...p, selected: false } : p) })}>이 호스트 제외</button></>}</small></label>)}
      </fieldset>
      <p className="stats-muted">ETC / MK119 System / MK119 Cache / MK119 v10 System Stats · 선택한 체크포인트가 있는 장비만 생성합니다. 이름이 기존 그룹·장비와 같아도 등록합니다(이미 등록된 키만 막음).</p>
      {plan.redisInfo && <p className="stats-muted">{plan.redisInfo}</p>}
      {registeredCount > 0 && <p className="stats-muted">이미 등록된 키 {registeredCount}개는 목록에서 숨겼습니다(중복 등록 방지){movedCount ? ` · 그중 ${movedCount}개는 등록된 장비와 지금 서버가 달라 옮겨진 것으로 보입니다(그대로 둠)` : ""}.</p>}
      {plan.patternInfo && <p className="stats-muted">{plan.patternInfo}</p>}
      {(plan.warnings || []).map((w, i) => <p className="stats-warning" key={i}>{w}</p>)}
      {unresolvedTokens.length > 0 && <details className="stats-recovery"><summary>서버를 찾지 못한 식별자 {unresolvedTokens.length}개 — 일괄 배치 또는 아래 목록에서 제외</summary><div className="stats-settings">{unresolvedTokens.map(token => <label key={token}>{`{${token}}`}<select disabled={!!busy || !!result} defaultValue="" onChange={e => { const host = e.target.value; edit({ points: selection.points.map(p => !plan.points.find(original => original.key === p.key)?.host && p.key.match(/\{([^{}]+)\}/)?.[1] === token ? { ...p, host, selected: !!host } : p) }); }}><option value="">미배치 / 제외</option>{selection.servers.filter(h => h.hostname !== "common").map(h => <option key={h.hostname} value={h.hostname}>{h.name} ({h.hostname})</option>)}</select></label>)}</div></details>}
      <div className="stats-toolbar"><input aria-label="키 검색" placeholder="Redis 키 또는 이름 검색" value={filter} onChange={e => setFilter(e.target.value)} /><span>{mode === "update" ? "새 키" : "발견"} {selection.points.length - registeredCount}개 · 선택 {count}개 · 장비 {deviceCount}개</span><button className="toolbtn" disabled={!!busy || !!result} onClick={() => edit({ points: selection.points.map(p => ({ ...p, selected: !!p.host && !p.registered })) })}>배치된 키 모두 선택</button><button className="toolbtn" disabled={!!busy || !!result} onClick={() => edit({ points: selection.points.map(p => ({ ...p, selected: false })) })}>모두 해제</button></div>
      <div className="stats-table-wrap"><table className="stats-table"><thead><tr><th>등록</th><th>Redis 키 / 체크포인트 이름</th><th>대상 Device</th><th>자료형 / 주기</th></tr></thead><tbody>{selection.points.map((p, i) => !p.registered && (!filter || (p.key + p.name).toLowerCase().includes(filter.toLowerCase())) && <tr key={p.key}>
        <td><input aria-label={`${p.key} 등록`} type="checkbox" checked={p.selected} disabled={!!busy || !!result} onChange={e => point(i, { selected: e.target.checked })} /></td>
        <td><code>{p.key}</code><input aria-label={`${p.key} 이름`} disabled={!!busy || !!result} value={p.name} onChange={e => point(i, { name: e.target.value })} />{p.deleted && <small className="stats-muted">이전에 등록했다가 삭제된 키 — 새로 생성합니다</small>}{p.remembered && <small className="stats-muted">기억된 패턴 적용</small>}{p.warning && <small className="stats-warning">{p.warning}</small>}</td>
        <td><select aria-label={`${p.key} 대상 서버`} value={p.host} disabled={!!busy || !!result || !p.key.includes("{")} onChange={e => point(i, { host: e.target.value })}><option value="">배치 필요</option>{selection.servers.filter(h => p.key.includes("{") ? h.hostname !== "common" : h.hostname === "common").map(h => <option key={h.hostname} value={h.hostname}>{h.name} ({h.hostname})</option>)}</select></td>
        <td><select aria-label={`${p.key} 자료형`} value={p.format} disabled={!!busy || !!result || !!p.templateId} onChange={e => point(i, { format: Number(e.target.value) })}>{p.templateId !== 0 && p.format < 3 && <option value={p.format}>템플릿 자료형 ({p.format})</option>}<option value={3}>숫자 (Double)</option><option value={4}>문자열 (String)</option></select><input aria-label={`${p.key} 주기 ms`} type="number" min={1000} step={1000} value={p.interval} disabled={!!busy || !!result} onChange={e => point(i, { interval: Number(e.target.value) })} /><small>ms {p.measure && `· ${p.measure}`}</small></td>
      </tr>)}</tbody></table></div>
    </>}
    {result && <div className={result.verified ? "stats-success" : "stats-warning"}><p>{result.message}</p>{result.notified && result.checkpointIds.length > 0 && <p>수집 확인: {result.collected}/{result.checkpointIds.length}개{result.missing?.length ? ` · 미수신 ${result.missing.length}개` : ""}</p>}<p>복구 ID: <code>{result.id}</code></p>{!result.verified && <button className="toolbtn" disabled={!!busy} onClick={() => work(result.notified ? "수집 다시 확인 중…" : "저장 상태 확인 후 이어서 진행…", async () => setResult(await StatsRetry(config, result.id)))}>{result.notified ? "수집 다시 확인" : "이어서 진행 (DB 재등록 없음)"}</button>}</div>}
    </div>
    <div className="stats-footer"><div role="status">{(busy && (progress || busy)) || error || (checked ? "등록 전 검사 통과." : "")}</div>{plan && selection && !result && mode !== "" && <><button className="toolbtn" disabled={!!busy} onClick={() => { setPlan(null); setSelection(null); setChecked(false); setMode(""); }}>연결 설정</button><button className="toolbtn" disabled={!!busy || !count} onClick={() => work("등록 전 검사 중…", async () => { setChecked(false); await StatsValidate(uniqueServers(selection)); setChecked(true); })}>등록 전 검사</button><button className="toolbtn primary" disabled={!!busy || !checked || !count} onClick={() => work("등록 중…", async () => { setChecked(false); setResult(await StatsRegister(uniqueServers(selection))); })}>{mode === "update" ? `업데이트: 새 장비 ${newDeviceCount}개 · 체크포인트 ${count}개 추가` : `장비 ${deviceCount}개 · 체크포인트 ${count}개 등록`}</button></>}</div>
  </div></div>;
}
