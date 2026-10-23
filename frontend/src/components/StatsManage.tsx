import { useEffect, useMemo, useRef, useState } from "react";
import { StatsManageLoad, StatsManagePreview, StatsManageApply, StatsManageResend, StatsManageDeletePreview, StatsManageDelete, StatsManagePending, StatsManageRedisRetry, StatsManageOrphans, StatsManageOrphanDeletePreview } from "../../wailsjs/go/main/App";
import { statsreg } from "../../wailsjs/go/models";
import { useEsc } from "../useEsc";
import StatsMessage, { parseStatsMessage } from "./statsMessage";

// 「기존 등록 관리」: one row per key shape (the server part of the key shown as
// {서버}), one column per device. A new name typed on a row goes to every
// server's checkpoint of that shape; a row can be opened for per-server names.
// Every change is previewed first and only the previewed changes are applied.

type RowInput = { name: string; interval: string };

const rowCurrentName = (r: statsreg.ManagedRow) => new Set(r.points.map(p => p.name)).size === 1 ? r.points[0].name : "";
const rowIntervals = (r: statsreg.ManagedRow) => Array.from(new Set(r.points.map(p => p.interval)));

// The one-line summary of the latest result, for the dialog's bottom status line.
export interface ManageStatus { text: string; warn: boolean }

const short = (text: string) => { const s = parseStatsMessage(text).summary; return s.length > 140 ? s.slice(0, 140) + "…" : s; };

function statusOf(result: statsreg.ManageResult | null, del: statsreg.DeleteResult | null, note: string): ManageStatus | null {
  if (result) {
    if (!result.committed) return { text: "적용하지 않음 — " + short(result.message), warn: true };
    if (result.pending) return { text: "⚠ 이름·주기: DB에는 저장됐지만 수집 서버에 알리지 못했습니다 — 위쪽 결과의 「수집 서버에 다시 알리기」를 누르세요", warn: true };
    return result.verified ? { text: "✓ 적용 끝 — " + short(result.message), warn: false } : { text: "⚠ 적용 결과를 확인하세요 — " + short(result.message), warn: true };
  }
  if (del) {
    if (del.pending) return { text: "⚠ 삭제: DB에서는 지웠지만 수집 서버에 알리지 못했습니다 — 위쪽 결과의 「수집 서버에 다시 알리기」를 누르세요", warn: true };
    return del.verified ? { text: "✓ 삭제 끝 — " + short(del.message), warn: false } : { text: "⚠ 삭제 결과를 확인하세요 — " + short(del.message), warn: true };
  }
  if (note) return { text: note, warn: note.includes("실패") };
  return null;
}

export default function StatsManage({ plan, busy, work, reinspect, onDialog, onStatus }: {
  plan: statsreg.Plan;
  busy: string;
  work: (label: string, fn: () => Promise<void>) => Promise<void>;
  // reinspect runs 「연결 확인」 again (groups changed) and returns the new analysis.
  reinspect: () => Promise<statsreg.Plan>;
  // onDialog tells the registration dialog that a confirmation owns Escape.
  onDialog: (open: boolean) => void;
  // onStatus puts the latest result in the dialog's bottom status line.
  onStatus: (s: ManageStatus | null) => void;
}) {
  const [groupId, setGroupId] = useState(plan.existing?.[0]?.groupId || 0);
  const [group, setGroup] = useState<statsreg.ManagedGroup | null>(null);
  const [rows, setRows] = useState<Record<string, RowInput>>({});
  const [overrides, setOverrides] = useState<Record<number, string>>({});
  const [deviceNames, setDeviceNames] = useState<Record<number, string>>({});
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [search, setSearch] = useState("");
  const [rawOnly, setRawOnly] = useState(false);
  const [preview, setPreview] = useState<statsreg.ManagePreview | null>(null);
  const [result, setResult] = useState<statsreg.ManageResult | null>(null);
  const [target, setTarget] = useState("");
  const [delPreview, setDelPreview] = useState<statsreg.DeletePreview | null>(null);
  const [confirm, setConfirm] = useState("");
  const [delResult, setDelResult] = useState<statsreg.DeleteResult | null>(null);
  // Devices a delete just removed: if a re-read still shows one it is marked 「삭제됨, 반영 대기」.
  const [gone, setGone] = useState<number[]>([]);
  // One line under the results: a cancelled delete, a failed re-read after a delete.
  const [note, setNote] = useState("");
  // Resource-stats devices in no group (admin's group delete leaves them behind).
  const [orphans, setOrphans] = useState<statsreg.OrphanDevice[] | null>(null);
  const loadOrphans = (planID: string) => StatsManageOrphans(planID).then(v => setOrphans(v || [])).catch(() => setOrphans(null));
  useEffect(() => { loadOrphans(plan.id); }, []);
  // Renames / deletes whose notices or Redis cleanup did not finish (kept on disk, also after a restart).
  const [pending, setPending] = useState<statsreg.PendingManage[]>([]);
  const refreshPending = () => StatsManagePending().then(v => setPending(v || [])).catch(() => {});
  useEffect(() => { refreshPending(); }, [result, delResult]);
  function showOutcome(o: statsreg.ManageOutcome) { if (o.kind === "delete") { setDelResult(o.delete); setResult(null); } else { setResult(o.rename); setDelResult(null); } }
  const resend = (id: string) => work("수집 서버에 다시 알리는 중…", async () => showOutcome(await StatsManageResend(plan.id, id)));
  const redisRetry = (id: string) => work("Redis 에 남은 최근값 다시 지우는 중…", async () => { setResult(null); setDelResult(await StatsManageRedisRetry(plan.id, id)); });
  // 3rd round: a result (even 「DB에는 저장됐지만 알림 0/2건…」) appeared at the top,
  // out of sight, while the bottom line stayed empty. Bring it into view and say it below too.
  const resultRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    onStatus(statusOf(result, delResult, note));
    if (result || delResult || note) resultRef.current?.scrollIntoView({ block: "start", behavior: "smooth" });
  }, [result, delResult, note]);
  useEffect(() => () => onStatus(null), []);
  const dialog = !!preview || !!delPreview;
  useEffect(() => { onDialog(dialog); }, [dialog]);
  function cancelDelete() { setDelPreview(null); setConfirm(""); setNote("취소됨 — 아무것도 지우지 않았습니다."); }
  useEsc(() => { setPreview(null); if (delPreview) cancelDelete(); }, dialog && !busy);

  const devices = group?.devices || [];
  const listed = useMemo(() => (group?.rows || []).filter(r => (!rawOnly || r.rawName) && (!search || (r.shape + " " + r.points.map(p => p.name + " " + p.key).join(" ")).toLowerCase().includes(search.toLowerCase()))), [group, rawOnly, search]);
  const edited = (r: statsreg.ManagedRow) => !!rows[r.shape]?.name || !!rows[r.shape]?.interval || r.points.some(p => !!overrides[p.checkpointId]);
  const editedRows = (group?.rows || []).filter(edited).length;
  const editedDevices = devices.filter(d => deviceNames[d.deviceId] !== undefined && deviceNames[d.deviceId] !== d.name).length;
  const hasServers = devices.filter(d => d.host !== "common").length;

  function clearInputs() { setRows({}); setOverrides({}); setDeviceNames({}); }
  async function load(id: number) {
    setGroup(null); clearInputs(); setOpen({}); setTarget("");
    if (id) setGroup(await StatsManageLoad(plan.id, id));
  }
  // The first group is opened right away (usually there is only one).
  useEffect(() => { if (groupId) work("그룹 불러오는 중…", () => load(groupId)); }, []);
  function setRow(shape: string, patch: Partial<RowInput>) { setRows(v => ({ ...v, [shape]: { ...(v[shape] || { name: "", interval: "" }), ...patch } })); }

  function edit(): statsreg.ManageEdit {
    const out: statsreg.RowEdit[] = [];
    for (const r of group?.rows || []) {
      if (!edited(r)) continue;
      const v = rows[r.shape] || { name: "", interval: "" };
      out.push(new statsreg.RowEdit({ shape: r.shape, name: v.name.trim() === "" ? "" : v.name, interval: v.interval ? Number(v.interval) : 0, overrides: r.points.filter(p => !!overrides[p.checkpointId]).map(p => ({ checkpointId: p.checkpointId, name: overrides[p.checkpointId] })) }));
    }
    const devs = devices.filter(d => deviceNames[d.deviceId] !== undefined && deviceNames[d.deviceId] !== d.name).map(d => new statsreg.DeviceEdit({ deviceId: d.deviceId, name: deviceNames[d.deviceId] }));
    return new statsreg.ManageEdit({ manageId: group?.id || "", rows: out, devices: devs });
  }

  const resultBox = <>
    {result && <div className={result.verified ? "stats-success stats-result" : "stats-warning stats-result"} role="status">
      <div><b>{result.committed ? "적용 결과" : "적용하지 않음"}</b> — <StatsMessage text={result.message} /></div>
      {result.committed && result.watched > 0 && <p>수집 확인: {result.collected}/{result.watched}개{result.missing?.length ? ` · 값이 안 들어온 체크포인트 ${result.missing.length}개` : ""}</p>}
      {result.conflicts?.length > 0 && <details open><summary>불러온 뒤 다른 곳(admin 등)에서 바뀌어 건너뛴 {result.conflicts.length}개 — 덮어쓰지 않았습니다</summary><ul>{result.conflicts.map((c, i) => <li key={i}>{c}</li>)}</ul></details>}
      {result.pending && <button className="toolbtn" disabled={!!busy} onClick={() => resend(result.id)}>수집 서버에 다시 알리기 (DB는 다시 바꾸지 않음)</button>}
    </div>}
    {note && <p className="stats-muted stats-result" role="status">{note}</p>}
    {delResult && <div className={delResult.verified ? "stats-success stats-result" : "stats-warning stats-result"} role="status">
      <div><b>삭제 결과</b> — <StatsMessage text={delResult.message} /></div>
      {delResult.pending && <button className="toolbtn" disabled={!!busy} onClick={() => resend(delResult.id)}>수집 서버에 다시 알리기 (DB는 다시 바꾸지 않음)</button>}
      {delResult.committed && delResult.notified && !delResult.verified && <button className="toolbtn" disabled={!!busy} onClick={() => redisRetry(delResult.id)}>Redis 최근값 다시 지우기 (지운 체크포인트 것)</button>}
    </div>}
  </>;

  return <div className="stats-manage">
    <div ref={resultRef} className="stats-result-anchor">{resultBox}</div>
    {pending.length > 0 && <details className="stats-recovery stats-pending" open><summary>마무리 안 된 작업 {pending.length}개 — DB에는 저장됐지만 수집 서버에 아직 알리지 못했거나, 지운 장비의 Redis 최근값이 남은 이름 변경·삭제</summary>
      <p className="stats-muted">언제 쓰나: 적용·삭제 결과에 「알리지 못했습니다」·「다 지우지 못했습니다」가 나왔을 때, 또는 그 도중에 앱이 꺼졌을 때. 그 작업을 한 때와 같은 MariaDB·Kafka로 연결 확인한 상태에서 누르세요. 「저장 여부 모름」은 누르면 DB부터 확인합니다. 앱을 다시 켜도 이 목록은 남습니다.</p>
      <ul>{pending.map(p => <li key={p.id}>{p.at.slice(0, 16).replace("T", " ")} · {p.kind === "delete" ? "삭제" : "이름·주기 변경"} · 그룹 {p.groupName} · {p.committed ? `수집 서버에 알림 ${p.sent}/${p.total}건 보냄` : "저장 여부 모름"}{p.keysLeft ? ` · Redis 에 남은 최근값 ${p.keysLeft}개` : ""}{p.message && <div className="stats-muted"><StatsMessage text={p.message} /></div>}
        {(!p.committed || p.sent < p.total) && <button className="linkbtn" disabled={!!busy} onClick={() => resend(p.id)}>수집 서버에 다시 알리기</button>}{p.kind === "delete" && p.committed && p.sent >= p.total && <> <button className="linkbtn" disabled={!!busy} onClick={() => redisRetry(p.id)}>Redis 최근값 다시 지우기</button></>}</li>)}</ul>
    </details>}
    {orphans && orphans.length > 0 && <details className="stats-recovery stats-orphans" open><summary>그룹에 없는 장비 {orphans.length}대 — admin 에서 그룹을 지우면 장비는 그룹 밖에 남아 계속 수집됩니다</summary>
      <p className="stats-muted">이 앱이 등록한 장비(체크포인트가 모두 리소스 수집 키)만 여기서 지울 수 있습니다. 나머지는 🔒 이유와 admin 에서 찾는 곳을 적어 두었습니다.</p>
      <ul>{orphans.map(o => <li key={o.deviceId}>{o.name} (장비 ID {o.deviceId}) · 체크포인트 {o.checkpoints}개 {gone.includes(o.deviceId) ? <small className="stats-badge">삭제됨, 반영 대기</small> : o.locked ? <small className="stats-warning">🔒 {o.locked}</small> : <button className="linkbtn" disabled={!!busy} onClick={() => work("지울 내용 확인 중…", async () => { setResult(null); setDelResult(null); setNote(""); setConfirm(""); setDelPreview(await StatsManageOrphanDeletePreview(plan.id, o.deviceId)); })}>지울 내용 미리보기</button>}</li>)}</ul>
    </details>}
    {!(plan.existing || []).length && <p className="stats-muted">관리할 그룹이 없습니다{orphans?.length ? " — 위의 「그룹에 없는 장비」만 정리할 수 있습니다" : ""}.</p>}
    {(plan.existing || []).length > 0 && <div className="stats-toolbar">
      <label className="stats-inline">관리할 그룹<select value={groupId} disabled={!!busy} onChange={e => { const id = Number(e.target.value); setGroupId(id); setResult(null); setDelResult(null); work("그룹 불러오는 중…", () => load(id)); }}>{(plan.existing || []).map(g => <option key={g.groupId} value={g.groupId}>{g.name} (ID {g.groupId}) — 장비 {g.devices.length}대 · 체크포인트 {g.points}개</option>)}</select></label>
      <button className="toolbtn" disabled={!!busy || !groupId} onClick={() => { setResult(null); setDelResult(null); work("그룹 불러오는 중…", () => load(groupId)); }}>{group ? "다시 불러오기" : "불러오기"}</button>
      {group && <span>장비 {devices.length}대 · 키 묶음 {group.rows.length}줄 · 체크포인트 {group.checkpoints}개{group.rawNames ? ` · 이름이 키 그대로 ${group.rawNames}개` : ""}</span>}
    </div>}
    {!group && (plan.existing || []).length > 0 && <p className="stats-muted">그룹을 고르고 「불러오기」를 누르면 그 그룹의 장비와 체크포인트를 서버만 다른 키끼리 묶어 한 줄로 보여 줍니다. 이름·주기 수정, 장비 이름 수정, 장비·그룹 삭제를 할 수 있습니다.</p>}
    {group && <>
      {group.warnings.map((w, i) => <p className="stats-warning" key={i}>{w}</p>)}
      <fieldset className="stats-settings" disabled={!!busy}><legend>장비 이름 (살아 있는 다른 장비와 같은 이름은 막습니다)</legend>
        {devices.map(d => <label key={d.deviceId}>{d.host === "common" ? "공통 항목" : d.host ? `서버 ${d.host}` : "서버 모름"} · 체크포인트 {d.checkpoints}개<input aria-label={`장비 ${d.name} 새 이름`} value={deviceNames[d.deviceId] ?? d.name} disabled={!!d.locked} title={d.locked || undefined} onChange={e => setDeviceNames(v => ({ ...v, [d.deviceId]: e.target.value }))} />{d.locked && <small className="stats-warning">🔒 {d.locked}</small>}</label>)}
      </fieldset>
      <div className="stats-toolbar">
        <input aria-label="키 또는 이름 검색" placeholder="키 또는 지금 이름 검색" value={search} onChange={e => setSearch(e.target.value)} />
        <label className="stats-check"><input type="checkbox" checked={rawOnly} onChange={e => setRawOnly(e.target.checked)} />이름이 키 그대로인 것만 ({group.rows.filter(r => r.rawName).length}줄)</label>
        <span>보이는 줄 {listed.length}/{group.rows.length} · 바꿀 줄 {editedRows}개{editedDevices ? ` · 바꿀 장비 이름 ${editedDevices}개` : ""}</span>
        <button className="toolbtn" disabled={!!busy || (!editedRows && !editedDevices)} onClick={clearInputs}>입력 지우기</button>
      </div>
      <details className="stats-help"><summary>새 이름 칸 쓰는 법</summary>
        <p>한 줄은 <b>서버만 다르고 나머지가 같은 키</b>를 묶은 것입니다(예: <code>liz.stats.server.{"{서버별}"}.free.memory</code> — {"{서버별}"} 자리에 서버마다 다른 값이 들어 있음). 줄의 「새 이름」은 그 줄에 있는 <b>모든 서버</b>의 체크포인트에 들어갑니다. 서버마다 다르게 하려면 ▸ 를 눌러 펼친 뒤 서버별 칸에 쓰세요(서버별 칸이 우선).</p>
        <p>이름에 넣으면 서버마다 알아서 바뀌는 자리: <code>{"{서버}"}</code> 서버 이름 그대로(server_1) · <code>{"{서버대문자}"}</code> 첫 글자만 대문자(Server_1) · <code>{"{서버번호}"}</code> 서버 이름 끝 번호(server_3 → 3) · <code>{"{키1}"}</code> <code>{"{키2}"}</code>… 키의 첫 번째·두 번째 {"{ }"} 안 값(예: 모듈 번호, 네트워크 카드 이름). 「지금 이름에서 시작」은 서버마다 다른 부분을 이 자리로 바꿔 넣습니다. 빈 칸은 바꾸지 않습니다. 주기는 ms(1000 = 1초)이고, admin 처럼 수집 주기·보관 주기를 같이 바꿉니다.</p>
        <p>바꾼 포인트 이름·주기는 이 PC가 기억해 두었다가, 다음에 새로 등록하거나 새 키를 추가할 때 같은 종류의 키에 미리 채웁니다. 장비 이름은 기억하지 않습니다.</p>
      </details>
      <div className="stats-table-wrap"><table className="stats-table stats-manage-table">
        <thead><tr><th></th><th title="서버만 다르고 나머지가 같은 키를 한 줄로 묶었습니다. {서버별} 자리에 서버마다 다른 값이 들어 있습니다">키 묶음 / 지금 이름</th>{devices.map(d => <th key={d.deviceId} title={`장비 ID ${d.deviceId}`}>{d.name}{gone.includes(d.deviceId) && <small className="stats-badge">삭제됨, 반영 대기</small>}</th>)}<th className="stats-interval-col">주기 ms</th><th>새 이름</th></tr></thead>
        <tbody>{listed.map(r => {
          const cur = rowCurrentName(r);
          const ivs = rowIntervals(r);
          const v = rows[r.shape] || { name: "", interval: "" };
          const partial = r.devices < (r.shape.includes("{서버별}") ? hasServers : 1);
          return [<tr key={r.shape} className={edited(r) ? "stats-edited-row" : undefined}>
            <td><button className="linkbtn" aria-label={`${r.shape} 서버별로 펼치기`} aria-expanded={!!open[r.shape]} onClick={() => setOpen(o => ({ ...o, [r.shape]: !o[r.shape] }))}>{open[r.shape] ? "▾" : "▸"}</button></td>
            <td><code>{r.shape}</code>{cur ? <span>{cur}</span> : <span className="stats-muted">서버마다 이름이 다름 — 펼쳐서 보기</span>}{r.rawName && <small className="stats-badge">이름이 키 그대로</small>}{partial && <small className="stats-badge">{r.devices}/{r.shape.includes("{서버별}") ? hasServers : 1} 서버에만 있음</small>}</td>
            {devices.map(d => { const ps = r.points.filter(p => p.deviceId === d.deviceId); return <td key={d.deviceId} className="stats-cell">{ps.length === 0 ? <span className="stats-muted">—</span> : <>✓{ps.length > 1 ? ` ${ps.length}개` : ""}<small title={ps.map(p => `${p.key} → ${p.name}`).join("\n")}>{ps.map(p => overrides[p.checkpointId] || p.name).join(", ")}</small></>}</td>; })}
            <td className="stats-interval-col"><input aria-label={`${r.shape} 새 주기 ms`} type="number" min={1000} step={1000} placeholder={ivs.length === 1 ? String(ivs[0]) : "서로 다름"} value={v.interval} disabled={!!busy} onChange={e => setRow(r.shape, { interval: e.target.value })} /></td>
            <td><input aria-label={`${r.shape} 새 이름`} placeholder={cur || "새 이름"} value={v.name} disabled={!!busy} onChange={e => setRow(r.shape, { name: e.target.value })} /><small className="stats-muted">{r.devices > 1 ? `→ 이 줄의 서버 ${r.devices}대 모두` : "→ 이 서버 1대에만 있음"}</small>{!v.name && r.template && <button className="linkbtn" disabled={!!busy} title="지금 이름을 틀로 바꿔 넣습니다. 서버마다 다른 부분은 {서버}·{서버번호}·{키1} 자리가 되어 서버별로 채워집니다" onClick={() => setRow(r.shape, { name: r.template })}>지금 이름에서 시작 ({r.template})</button>}{r.templateWarning && <small className="stats-warning">⚠ {r.templateWarning}</small>}</td>
          </tr>,
          open[r.shape] && <tr key={r.shape + " open"} className="stats-open-row"><td></td><td colSpan={devices.length + 3}>{r.points.map(p => <label key={p.checkpointId} className="stats-override"><span>{devices.find(d => d.deviceId === p.deviceId)?.name} · <code>{p.key}</code> · 체크포인트 {p.checkpointId} · 지금 이름 「{p.name}」 · {p.interval} ms</span><input aria-label={`체크포인트 ${p.checkpointId} 새 이름`} placeholder="이 서버만 다른 이름 (비우면 줄의 새 이름)" value={overrides[p.checkpointId] || ""} disabled={!!busy} onChange={e => setOverrides(o => ({ ...o, [p.checkpointId]: e.target.value }))} /></label>)}</td></tr>];
        })}</tbody>
      </table></div>
      <div className="stats-toolbar"><button className="toolbtn primary" disabled={!!busy || (!editedRows && !editedDevices)} onClick={() => work("바뀔 내용 확인 중…", async () => { setResult(null); setDelResult(null); setPreview(await StatsManagePreview(edit())); })}>바뀔 내용 미리보기 (줄 {editedRows}개{editedDevices ? ` · 장비 ${editedDevices}개` : ""})</button><span className="stats-muted">미리보기에서 「적용」을 눌러야 DB에 씁니다.</span></div>

      <details className="stats-danger"><summary>장비 또는 그룹 삭제</summary>
        <div className="stats-muted">장비를 지우면 그 장비의 체크포인트도 모두 지워집니다(admin 의 장비 삭제와 같음). 체크포인트 하나씩 지우기는 없습니다 — 지금 수집 서버는 하나만 지운 체크포인트를 재시작 전까지 초당 10번씩 경고로 남기기 때문입니다. 그룹 삭제는 그 안의 장비를 먼저 모두 지운 뒤 그룹을 지웁니다(admin 은 장비를 남깁니다). 지운 체크포인트가 Redis 에 남긴 최근값도 이 앱이 지웁니다. <details className="stats-more"><summary>자세히</summary><code>Redis 키 checkpoint:&lt;체크포인트 번호&gt; — 이 빌드(trunk #987)의 Collector 는 장비를 지워도 이 키를 남김(TTL 없음)</code></details></div>
        <div className="stats-toolbar"><label className="stats-inline">지울 대상<select value={target} disabled={!!busy} onChange={e => setTarget(e.target.value)}><option value="">고르세요</option>{devices.map(d => <option key={d.deviceId} value={String(d.deviceId)} disabled={!!d.locked || gone.includes(d.deviceId)}>장비 {d.name} (체크포인트 {d.checkpoints}개){d.locked ? " — 🔒 다른 그룹에도 있거나 바꿀 수 없는 장비" : ""}</option>)}<option value="group">그룹 「{group.name}」 전체 (장비 {devices.length}대)</option></select></label>
          <button className="toolbtn" disabled={!!busy || !target} onClick={() => work("지울 내용 확인 중…", async () => { setResult(null); setDelResult(null); setNote(""); setConfirm(""); setDelPreview(await StatsManageDeletePreview(new statsreg.DeleteRequest({ manageId: group.id, deviceId: target === "group" ? 0 : Number(target), wholeGroup: target === "group" }))); })}>지울 내용 미리보기</button></div>
      </details>
    </>}

    {preview && <div className="scrim stats-inner-scrim"><div className="stats-inner-dialog" role="dialog" aria-modal="true" aria-labelledby="stats-preview-title">
      <h3 id="stats-preview-title">바뀔 내용</h3>
      <p>체크포인트 {preview.points.length}개(이름 {preview.renamed}개 · 주기 {preview.intervals}개) · 장비 이름 {preview.devices.length}개 · 영향 받는 장비 {preview.touched}대. 적용하면 한 번에 저장하고, 수집 서버에 변경 알림 {(preview.points.length ? 1 : 0) + 1 + (preview.devices.length ? 1 : 0)}건을 보낸 뒤 값이 계속 들어오는지 확인합니다.</p>
      <p className="stats-muted">admin 화면에서 이 장비·포인트의 편집 창을 열어 둔 사람이 있으면 먼저 닫게 해 주세요. 열린 창에서 저장하면 여기서 바꾼 이름이 예전 이름으로 돌아갑니다.</p>
      {preview.errors?.map((e, i) => <p className="stats-error" key={"e" + i}>⛔ {e}</p>)}
      {preview.warnings?.map((w, i) => <p className="stats-warning" key={"w" + i}>⚠ {w}</p>)}
      <div className="stats-table-wrap"><table className="stats-table"><thead><tr><th>장비</th><th>키</th><th>지금 → 바뀐 뒤</th></tr></thead><tbody>
        {preview.devices.map(d => <tr key={"d" + d.deviceId}><td>장비 이름</td><td>ID {d.deviceId}</td><td>{d.oldName} → <b>{d.newName}</b></td></tr>)}
        {preview.points.map(c => <tr key={c.checkpointId} className={c.suspicious ? "stats-suspicious-row" : undefined}><td>{c.deviceName}</td><td><code>{c.key}</code></td><td>{c.oldName !== c.newName ? <>{c.oldName} → <b>{c.newName}</b></> : c.oldName}{c.suspicious && <small className="stats-warning">⚠ 다른 서버의 값이 들어 있습니다 — 이 서버의 이름이 맞는지 확인하세요</small>}{c.oldInterval !== c.newInterval && <small>주기 {c.oldInterval} → <b>{c.newInterval}</b> ms</small>}</td></tr>)}
      </tbody></table></div>
      <div className="stats-update-buttons"><button className="toolbtn primary" disabled={!!busy || !preview.id} onClick={() => work("적용 중…", async () => { const id = preview.id; setPreview(null); const r = await StatsManageApply(id); setResult(r); if (r.committed) { clearInputs(); try { setGroup(await StatsManageLoad(plan.id, groupId)); } catch (e) { setGroup(null); setResult({ ...r, message: r.message + ` (목록을 다시 불러오지 못했습니다: ${e} — 「연결 설정」에서 다시 연결 확인하세요)` } as statsreg.ManageResult); } } })}>적용</button><button className="toolbtn" disabled={!!busy} onClick={() => setPreview(null)}>취소</button>{!preview.id && <span className="stats-muted">위 ⛔ 항목을 고쳐야 적용할 수 있습니다.</span>}</div>
    </div></div>}

    {delPreview && <div className="scrim stats-inner-scrim"><div className="stats-inner-dialog stats-delete-dialog" role="alertdialog" aria-modal="true" aria-labelledby="stats-delete-title">
      <h3 id="stats-delete-title">{delPreview.wholeGroup ? `그룹 「${delPreview.groupName}」 삭제` : `장비 「${delPreview.confirm}」 삭제`}</h3>
      <p>지워지는 것: <b>장비 {delPreview.devices.length}대 · 체크포인트 {delPreview.checkpoints}개</b>{delPreview.wholeGroup ? " · 그룹 1개" : ""} · Redis 에 남은 최근값 최대 {delPreview.checkpoints}개</p>
      <ul>{delPreview.devices.map(d => { const ps = (delPreview.points || []).filter(p => p.deviceId === d.deviceId); return <li key={d.deviceId}>{d.name} (ID {d.deviceId}) — 체크포인트 {d.checkpoints}개{ps.length > 0 && <details className="stats-delete-names"><summary>지워질 체크포인트 이름 보기</summary><ol>{ps.map(p => <li key={p.checkpointId}>{p.name} <small className="stats-muted">{p.key}</small></li>)}</ol></details>}</li>; })}</ul>
      <p className="stats-error"><b>되돌릴 수 없습니다.</b> admin 에도 지운 장비를 되살리는 화면이 없습니다.</p>
      {delPreview.blockers?.map((b, i) => <p className="stats-error" key={"b" + i}>⛔ {b}</p>)}
      {delPreview.warnings?.map((w, i) => <p className="stats-warning" key={"w" + i}>⚠ {w}</p>)}
      {delPreview.id && <label>확인을 위해 <b>{delPreview.confirm}</b> 을(를) 그대로 입력하세요<input aria-label="삭제 확인 이름" value={confirm} autoComplete="off" onChange={e => setConfirm(e.target.value)} /></label>}
      <div className="stats-update-buttons"><button className="toolbtn danger" disabled={!!busy || !delPreview.id || confirm !== delPreview.confirm} onClick={() => work("삭제 중…", async () => { const p = delPreview; setDelPreview(null); setNote(""); const r = await StatsManageDelete(p.id, confirm); setDelResult(r); setConfirm(""); if (r.committed) setGone(r.deviceIds || []); try { const next = await reinspect(); loadOrphans(next.id); const still = next.existing?.find(g => g.groupId === groupId); if (still) setGroup(await StatsManageLoad(next.id, groupId)); else { setGroup(null); setGroupId(next.existing?.[0]?.groupId || 0); } } catch (e) { setNote(`삭제는 끝났지만 목록 갱신 실패 — 아래 목록은 지우기 전 상태일 수 있습니다. 「다시 불러오기」를 누르세요. (${e})`); } })}>{delPreview.wholeGroup ? "그룹 삭제" : "장비 삭제"}</button><button className="toolbtn" disabled={!!busy} onClick={cancelDelete}>취소</button></div>
    </div></div>}
  </div>;
}
