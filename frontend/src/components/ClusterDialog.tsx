import { useState } from "react";
import { host } from "../../wailsjs/go/models";
import { SaveHosts } from "../../wailsjs/go/main/App";
import { candidatesOf } from "./ConnectDialog";

interface Props {
  // When set, the dialog edits an existing cluster (pre-filled) and upserts its
  // hosts by id; onSaved's caller deletes any members removed during the edit.
  editing?: { id: string; name: string; hosts: host.Host[] };
  // Every registered host, for picking an external jump server.
  hosts?: host.Host[];
  onSaved: (saved: host.Host[], connect: boolean, clusterId?: string) => void;
  onClose: () => void;
}

// How this cluster is reached. "member" is the common case in a locked-down data
// centre: one server is exposed and the rest are only reachable through it.
type JumpMode = "none" | "member" | "external";

interface Row {
  id?: string; // set for existing hosts being edited (upsert), empty for new rows
  addr: string;
  port: number;
  name: string;
  user: string;
  password: string;
  keyPath: string;
}

const emptyRow = (): Row => ({ addr: "", port: 22, name: "", user: "", password: "", keyPath: "" });

// deriveInit turns an editing cluster's hosts into the dialog's initial state.
// Common-credential mode is used when every member shares user/password/keyPath.
function deriveInit(hosts: host.Host[]) {
  const rows: Row[] = hosts.map((h) => ({
    id: h.id, addr: h.addr, port: h.port || 22, name: h.name,
    user: h.user, password: h.password, keyPath: h.keyPath,
  }));
  const same = (f: (h: host.Host) => string) => hosts.every((h) => f(h) === f(hosts[0]));
  const common = hosts.length > 0 && same((h) => h.user) && same((h) => h.password) && same((h) => h.keyPath);

  // Recover the jump configuration. Members that have candidates should all list
  // the same ones; if those targets are members of this cluster, this is the
  // "entry server" shape.
  const lists = hosts.map(candidatesOf).filter((l) => l.length > 0);
  let jumpMode: JumpMode = "none";
  let jumpRows: number[] = [];
  let jumpExternals: string[] = [];
  if (lists.length > 0) {
    const first = lists[0];
    const asRows = first.map((id) => hosts.findIndex((h) => h.id === id));
    if (asRows.every((i) => i >= 0)) {
      jumpMode = "member";
      jumpRows = asRows;
    } else {
      jumpMode = "external";
      jumpExternals = first.filter((id) => !hosts.some((h) => h.id === id));
    }
  }
  return {
    rows,
    perServer: !common,
    commonUser: common ? hosts[0].user : "",
    commonPassword: common ? hosts[0].password : "",
    commonKeyPath: common ? hosts[0].keyPath : "",
    jumpMode,
    jumpRows,
    jumpExternals,
  };
}

// ClusterDialog registers (or edits) several hosts at once as one named cluster.
// By default every server shares one SSH account/password; ticking "서버별로 다름"
// reveals per-row credential fields. IP/port rows can be added/removed dynamically.
export default function ClusterDialog({ editing, hosts = [], onSaved, onClose }: Props) {
  const init = editing ? deriveInit(editing.hosts) : null;
  const [clusterName, setClusterName] = useState(editing?.name ?? "");
  const [jumpMode, setJumpMode] = useState<JumpMode>(init?.jumpMode ?? "none");
  const [jumpRows, setJumpRows] = useState<number[]>(init?.jumpRows ?? []);
  const [jumpExternals, setJumpExternals] = useState<string[]>(init?.jumpExternals ?? []);
  const memberIds = new Set((editing?.hosts ?? []).map((h) => h.id));
  const externalChoices = hosts.filter((h) => !memberIds.has(h.id));
  // Priority is the order shown, so keep the selections sorted the same way.
  const toggleRow = (i: number) =>
    setJumpRows((p) => (p.includes(i) ? p.filter((x) => x !== i) : [...p, i].sort((a, b) => a - b)));
  const toggleExternal = (id: string) =>
    setJumpExternals((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));
  const [perServer, setPerServer] = useState(init?.perServer ?? false);
  const [commonUser, setCommonUser] = useState(init?.commonUser ?? "");
  const [commonPassword, setCommonPassword] = useState(init?.commonPassword ?? "");
  const [commonKeyPath, setCommonKeyPath] = useState(init?.commonKeyPath ?? "");
  const [rows, setRows] = useState<Row[]>(init?.rows ?? [emptyRow(), emptyRow(), emptyRow()]);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  function setRow(i: number, patch: Partial<Row>) {
    setRows((prev) => prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  }
  function addRow() {
    setRows((prev) => [...prev, emptyRow()]);
  }
  function removeRow(i: number) {
    setRows((prev) => (prev.length <= 1 ? prev : prev.filter((_, idx) => idx !== i)));
  }

  async function save(connect: boolean) {
    const name = clusterName.trim();
    if (!name) { setErr("클러스터 이름을 입력하세요."); return; }

    const filled = rows.filter((r) => r.addr.trim() !== "");
    if (filled.length === 0) { setErr("서버 IP를 최소 1개 입력하세요."); return; }

    const addrs = filled.map((r) => r.addr.trim());
    if (new Set(addrs).size !== addrs.length) { setErr("중복된 IP가 있습니다."); return; }

    if (!perServer) {
      if (!commonUser.trim()) { setErr("공통 SSH 계정을 입력하세요."); return; }
      if (!commonPassword && !commonKeyPath) {
        setErr("공통 비밀번호 또는 개인키 경로 중 하나는 필요합니다.");
        return;
      }
    } else {
      for (let i = 0; i < filled.length; i++) {
        const r = filled[i];
        if (!r.user.trim()) { setErr(`${i + 1}번째 서버의 SSH 계정을 입력하세요.`); return; }
        if (!r.password && !r.keyPath) {
          setErr(`${i + 1}번째 서버의 비밀번호 또는 키 경로가 필요합니다.`);
          return;
        }
      }
    }

    if (jumpMode === "member") {
      const valid = jumpRows.filter((i) => i >= 0 && i < filled.length);
      if (valid.length === 0) { setErr("진입 서버를 1개 이상 선택하세요."); return; }
      if (valid.length >= filled.length) {
        setErr("모든 서버를 진입 서버로 지정할 수는 없습니다 (경유할 대상이 없습니다).");
        return;
      }
    }
    if (jumpMode === "external" && jumpExternals.length === 0) {
      setErr("외부 경유 서버를 1개 이상 선택하세요.");
      return;
    }

    setBusy(true);
    setErr("");
    try {
      const clusterId = editing?.id ?? crypto.randomUUID();
      const list = filled.map((r, i) =>
        host.Host.createFrom({
          id: r.id ?? "", // preserve id → upsert existing member; empty → new host
          name: r.name.trim() || `${name}-${i + 1}`,
          addr: r.addr.trim(),
          port: Number(r.port) || 22,
          user: perServer ? r.user.trim() : commonUser.trim(),
          password: perServer ? r.password : commonPassword,
          keyPath: perServer ? r.keyPath : commonKeyPath,
          clusterId,
          clusterName: name,
          // "member" mode needs ids that do not exist yet for new rows, so it is
          // applied in a second pass below.
          jumpHostIds: jumpMode === "external" ? jumpExternals : [],
          jumpHostId: "",
        })
      );
      let saved = (await SaveHosts(list)) ?? [];

      if (jumpMode === "member" && saved.length === filled.length) {
        // Now that every row has an id, point the others at the entry servers, in
        // the displayed order. An entry server keeps an empty list — pointing it at
        // itself (or at another entry) would be a cycle.
        const entryIdx = jumpRows.filter((i) => i >= 0 && i < saved.length);
        const entryIds = entryIdx.map((i) => saved[i].id);
        const patched = saved.map((h, i) =>
          host.Host.createFrom({ ...h, jumpHostIds: entryIdx.includes(i) ? [] : entryIds, jumpHostId: "" })
        );
        saved = (await SaveHosts(patched)) ?? saved;
      }
      onSaved(saved, connect, clusterId);
    } catch (e: any) {
      setErr(String(e));
      setBusy(false);
    }
  }

  return (
    <div className="scrim" onMouseDown={onClose}>
      <div className="modal" onMouseDown={(e) => e.stopPropagation()} style={{ minWidth: 560 }}>
        <h2>{editing ? "클러스터 편집" : "클러스터 추가"}</h2>

        <label>클러스터 이름</label>
        <input type="text" value={clusterName} placeholder="예: prod-collector"
          onChange={(e) => setClusterName(e.target.value)} autoFocus />

        {!perServer && (
          <div className="row2">
            <div>
              <label>공통 SSH 계정</label>
              <input type="text" value={commonUser} placeholder="liz / root"
                onChange={(e) => setCommonUser(e.target.value)} />
            </div>
            <div>
              <label>공통 비밀번호</label>
              <input type="password" value={commonPassword}
                onChange={(e) => setCommonPassword(e.target.value)} />
            </div>
          </div>
        )}
        {!perServer && (
          <>
            <label>또는 공통 개인키 경로</label>
            <input type="text" value={commonKeyPath} placeholder="C:\keys\id_rsa"
              onChange={(e) => setCommonKeyPath(e.target.value)} />
          </>
        )}

        <label className="check" style={{ marginTop: 12 }}>
          <input type="checkbox" checked={perServer}
            onChange={(e) => setPerServer(e.target.checked)} />
          서버별로 계정/비밀번호가 다름
        </label>

        <label style={{ marginTop: 12 }}>서버 목록 (IP / 포트)</label>
        <div className="cluster-rows">
          {rows.map((r, i) => (
            <div key={i} className="cluster-row">
              <input type="text" className="cr-addr" value={r.addr} placeholder={`10.0.0.${i + 1}`}
                onChange={(e) => setRow(i, { addr: e.target.value })} />
              <input type="number" className="cr-port" value={r.port}
                onChange={(e) => setRow(i, { port: Number(e.target.value) })} />
              {perServer && (
                <>
                  <input type="text" className="cr-user" value={r.user} placeholder="계정"
                    onChange={(e) => setRow(i, { user: e.target.value })} />
                  <input type="password" className="cr-pass" value={r.password} placeholder="비밀번호"
                    onChange={(e) => setRow(i, { password: e.target.value })} />
                </>
              )}
              <button className="cr-del" title="이 서버 제거" onClick={() => removeRow(i)}
                disabled={rows.length <= 1}>×</button>
            </div>
          ))}
        </div>
        <button className="toolbtn" style={{ marginTop: 8 }} onClick={addRow}>+ 서버 추가</button>

        {/* Reachability. In a locked-down data centre only one server is exposed and
            the rest are reached through it — setting that here applies to every
            member at once instead of editing nine hosts one by one. */}
        <label style={{ marginTop: 14 }}>접속 방식</label>
        <div style={{ display: "flex", flexDirection: "column", gap: 6, fontSize: 12 }}>
          <label style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer" }}>
            <input type="radio" name="cjump" checked={jumpMode === "none"}
              onChange={() => setJumpMode("none")} />
            모든 서버에 직접 접속
          </label>
          <label style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer" }}>
            <input type="radio" name="cjump" checked={jumpMode === "member"}
              onChange={() => setJumpMode("member")} />
            이 클러스터의 서버를 경유 (진입 서버 · 여러 개 선택 가능)
          </label>
          {jumpMode === "member" && (
            <div style={{ marginLeft: 24, display: "flex", flexDirection: "column", gap: 3 }}>
              {rows.filter((r) => r.addr.trim() !== "").map((r, i) => (
                <label key={i} style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer" }}>
                  <input type="checkbox" checked={jumpRows.includes(i)} onChange={() => toggleRow(i)} />
                  {i + 1}번째 — {r.addr.trim()}{r.name.trim() ? ` (${r.name.trim()})` : ""}
                </label>
              ))}
            </div>
          )}
          <label style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer" }}>
            <input type="radio" name="cjump" checked={jumpMode === "external"}
              onChange={() => setJumpMode("external")} />
            다른(외부) 서버를 경유 (여러 개 선택 가능)
          </label>
          {jumpMode === "external" && (
            <div style={{ marginLeft: 24, display: "flex", flexDirection: "column", gap: 3, maxHeight: 140, overflowY: "auto" }}>
              {externalChoices.length === 0 ? (
                <span style={{ color: "var(--text-mute)" }}>등록된 다른 호스트가 없습니다.</span>
              ) : externalChoices.map((h) => (
                <label key={h.id} style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer" }}>
                  <input type="checkbox" checked={jumpExternals.includes(h.id)}
                    onChange={() => toggleExternal(h.id)} />
                  {h.name} ({h.user}@{h.addr})
                </label>
              ))}
            </div>
          )}
        </div>
        {jumpMode === "member" && jumpRows.length > 0 && (
          <div style={{ color: "var(--text-mute)", fontSize: 11, marginTop: 6 }}>
            진입 서버는 직접 접속하고, 나머지 서버는 <b>
              {jumpRows.map((i) => rows[i]?.addr.trim() || `${i + 1}번째`).join(" → ")}
            </b> 순서로 경유를 시도합니다. 앞의 경로가 막혀 있으면 다음 후보로 자동
            재시도하고, 모두 실패하면 직접 접속으로 넘어가지 않고 실패로 보고합니다.
          </div>
        )}

        <div style={{ color: "var(--text-mute)", fontSize: 11, marginTop: 10 }}>
          표시 이름을 비우면 <b>{clusterName || "클러스터"}-1, -2 …</b> 로 자동 지정됩니다.
          sudo는 각 서버의 비밀번호로 자동 시도합니다.
        </div>

        <div className="err">{err}</div>
        <div className="actions">
          <button className="toolbtn" onClick={onClose}>취소</button>
          <button className="toolbtn" onClick={() => save(false)} disabled={busy}>저장</button>
          <button className="toolbtn primary" onClick={() => save(true)} disabled={busy}>
            {busy ? "처리 중…" : "저장 후 연결"}
          </button>
        </div>
      </div>
    </div>
  );
}
