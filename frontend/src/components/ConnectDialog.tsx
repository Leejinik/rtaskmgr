import { useState } from "react";
import { host } from "../../wailsjs/go/models";
import { SaveHost } from "../../wailsjs/go/main/App";

interface Props {
  initial?: host.Host;
  // Every registered host, so this one can be pointed through another as a jump.
  hosts?: host.Host[];
  onSaved: (h: host.Host, connect: boolean) => void;
  onClose: () => void;
}

// candidatesOf reads a host's jump candidates, tolerating the earlier
// single-value form.
export function candidatesOf(h: host.Host): string[] {
  const list = (h as any).jumpHostIds as string[] | undefined;
  if (list && list.length > 0) return list.filter((x) => x);
  const one = (h as any).jumpHostId as string | undefined;
  return one ? [one] : [];
}

// jumpWouldLoop reports whether making `cand` a jump host of `selfId` creates a
// cycle — following cand's own candidates must never arrive back at selfId. The
// backend refuses a loop too, but catching it here means the operator never gets
// to save a configuration that cannot connect.
function jumpWouldLoop(hosts: host.Host[], selfId: string, cand: host.Host): boolean {
  const byId = new Map(hosts.map((h) => [h.id, h]));
  const seen = new Set<string>();
  const stack = [cand];
  while (stack.length > 0) {
    const cur = stack.pop()!;
    if (cur.id === selfId) return true;
    if (seen.has(cur.id)) continue;
    seen.add(cur.id);
    for (const id of candidatesOf(cur)) {
      const nxt = byId.get(id);
      if (nxt) stack.push(nxt);
    }
  }
  return false;
}

// ConnectDialog adds or edits a host. "저장 후 연결" saves and immediately
// connects; "저장"만 누르면 목록에만 추가된다. sudo는 입력한 비밀번호로
// 자동 시도하므로 별도 입력란이 없다.
export default function ConnectDialog({ initial, hosts = [], onSaved, onClose }: Props) {
  const editing = !!initial;
  const [name, setName] = useState(initial?.name ?? "");
  const [addr, setAddr] = useState(initial?.addr ?? "");
  const [port, setPort] = useState(initial?.port ?? 22);
  const [user, setUser] = useState(initial?.user ?? "");
  const [password, setPassword] = useState(initial?.password ?? "");
  const [keyPath, setKeyPath] = useState(initial?.keyPath ?? "");
  const [jumpIds, setJumpIds] = useState<string[]>(initial ? candidatesOf(initial) : []);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const selfId = initial?.id ?? "";
  const byId = new Map(hosts.map((h) => [h.id, h]));
  const jumpChoices = hosts.filter(
    (h) => h.id !== selfId && !jumpIds.includes(h.id) && !jumpWouldLoop(hosts, selfId, h)
  );
  const move = (i: number, d: number) =>
    setJumpIds((prev) => {
      const j = i + d;
      if (j < 0 || j >= prev.length) return prev;
      const next = [...prev];
      [next[i], next[j]] = [next[j], next[i]];
      return next;
    });

  async function save(connect: boolean) {
    if (!addr) { setErr("호스트 주소를 입력하세요."); return; }
    if (!user) { setErr("SSH 계정을 입력하세요."); return; }
    if (!password && !keyPath) {
      setErr("비밀번호 또는 키 파일 경로 중 하나는 필요합니다.");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const h = host.Host.createFrom({
        ...(initial ?? {}),
        name: name || addr,
        addr,
        port: Number(port) || 22,
        user,
        password,
        keyPath,
        jumpHostIds: jumpIds,
        jumpHostId: "", // superseded by the list
      });
      const saved = await SaveHost(h);
      onSaved(saved, connect);
    } catch (e: any) {
      setErr(String(e));
      setBusy(false);
    }
  }

  return (
    <div className="scrim" onMouseDown={onClose}>
      <div className="modal" onMouseDown={(e) => e.stopPropagation()}>
        <h2>{editing ? "호스트 편집" : "호스트 추가"}</h2>

        <label>표시 이름</label>
        <input type="text" value={name} placeholder="예: prod-collector-01"
          onChange={(e) => setName(e.target.value)} />

        <div className="row2">
          <div>
            <label>호스트 / IP</label>
            <input type="text" value={addr} placeholder="10.0.0.10"
              onChange={(e) => setAddr(e.target.value)} autoFocus />
          </div>
          <div style={{ maxWidth: 90 }}>
            <label>포트</label>
            <input type="number" value={port}
              onChange={(e) => setPort(Number(e.target.value))} />
          </div>
        </div>

        <label>SSH 계정</label>
        <input type="text" value={user} placeholder="liz / root"
          onChange={(e) => setUser(e.target.value)} />

        <div className="row2">
          <div>
            <label>비밀번호</label>
            <input type="password" value={password}
              onChange={(e) => setPassword(e.target.value)} />
          </div>
          <div>
            <label>또는 개인키 경로</label>
            <input type="text" value={keyPath} placeholder="C:\\keys\\id_rsa"
              onChange={(e) => setKeyPath(e.target.value)} />
          </div>
        </div>

        {/* Jump host. Some data centres only expose one or two servers; everything
            else has to be reached through them. Chosen from the registered hosts so
            the credentials are already there and the jump server itself stays
            monitorable. */}
        <label>경유 서버 (직접 접속이 막힌 경우 · 위에서부터 순서대로 시도)</label>
        {jumpIds.length === 0 ? (
          <div style={{ color: "var(--text-mute)", fontSize: 12, padding: "4px 0" }}>
            직접 접속
          </div>
        ) : (
          <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
            {jumpIds.map((id, i) => {
              const h = byId.get(id);
              return (
                <div key={id} style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 12 }}>
                  <span style={{ color: "var(--text-mute)", minWidth: 16 }}>{i + 1}.</span>
                  <span style={{ flex: 1 }}>
                    {h ? `${h.name} (${h.user}@${h.addr})` : `${id} — 삭제된 호스트`}
                  </span>
                  <button className="toolbtn" style={{ padding: "1px 6px" }}
                    onClick={() => move(i, -1)} disabled={i === 0} title="위로">↑</button>
                  <button className="toolbtn" style={{ padding: "1px 6px" }}
                    onClick={() => move(i, 1)} disabled={i === jumpIds.length - 1} title="아래로">↓</button>
                  <button className="toolbtn" style={{ padding: "1px 6px" }}
                    onClick={() => setJumpIds((p) => p.filter((x) => x !== id))} title="제거">×</button>
                </div>
              );
            })}
          </div>
        )}
        {jumpChoices.length > 0 && (
          <select value="" onChange={(e) => { if (e.target.value) setJumpIds((p) => [...p, e.target.value]); }}
            style={{
              width: "100%", marginTop: 6, background: "var(--bg)", color: "var(--text)",
              border: "1px solid var(--border)", borderRadius: 6, padding: "7px 10px", outline: "none",
            }}>
            <option value="">+ 경유 서버 추가…</option>
            {jumpChoices.map((h) => (
              <option key={h.id} value={h.id}>
                {h.name} ({h.user}@{h.addr})
              </option>
            ))}
          </select>
        )}
        {jumpIds.length > 0 && (
          <div style={{ color: "var(--text-mute)", fontSize: 11, marginTop: 6 }}>
            이 서버의 모든 동작(프로세스 모니터·기록·패킷 캡쳐·로그 수집)이 경유 서버를
            통해 이뤄집니다. 첫 번째 경로가 실패하면 다음 후보로 자동 재시도하고, 모두
            실패하면 <b>직접 접속으로 넘어가지 않고</b> 실패로 보고합니다.
          </div>
        )}

        <div style={{ color: "var(--text-mute)", fontSize: 11, marginTop: 10 }}>
          sudo는 입력한 비밀번호로 자동 시도합니다. 권한이 없으면 일부 정보(타 사용자
          프로세스의 디스크 I/O)만 비활성화됩니다.
        </div>

        <div className="err">{err}</div>
        <div className="actions">
          <button className="toolbtn" onClick={onClose}>취소</button>
          <button className="toolbtn" onClick={() => save(false)} disabled={busy}>
            저장
          </button>
          <button className="toolbtn primary" onClick={() => save(true)} disabled={busy}>
            {busy ? "처리 중…" : "저장 후 연결"}
          </button>
        </div>
      </div>
    </div>
  );
}
