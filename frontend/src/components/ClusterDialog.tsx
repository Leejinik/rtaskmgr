import { useState } from "react";
import { host } from "../../wailsjs/go/models";
import { SaveHosts } from "../../wailsjs/go/main/App";

interface Props {
  // When set, the dialog edits an existing cluster (pre-filled) and upserts its
  // hosts by id; onSaved's caller deletes any members removed during the edit.
  editing?: { id: string; name: string; hosts: host.Host[] };
  onSaved: (saved: host.Host[], connect: boolean, clusterId?: string) => void;
  onClose: () => void;
}

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
  return {
    rows,
    perServer: !common,
    commonUser: common ? hosts[0].user : "",
    commonPassword: common ? hosts[0].password : "",
    commonKeyPath: common ? hosts[0].keyPath : "",
  };
}

// ClusterDialog registers (or edits) several hosts at once as one named cluster.
// By default every server shares one SSH account/password; ticking "서버별로 다름"
// reveals per-row credential fields. IP/port rows can be added/removed dynamically.
export default function ClusterDialog({ editing, onSaved, onClose }: Props) {
  const init = editing ? deriveInit(editing.hosts) : null;
  const [clusterName, setClusterName] = useState(editing?.name ?? "");
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
        })
      );
      const saved = await SaveHosts(list);
      onSaved(saved ?? [], connect, clusterId);
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
