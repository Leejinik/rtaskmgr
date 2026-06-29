import { useState } from "react";
import { host } from "../../wailsjs/go/models";
import { SaveHost } from "../../wailsjs/go/main/App";

interface Props {
  initial?: host.Host;
  onSaved: (h: host.Host, connect: boolean) => void;
  onClose: () => void;
}

// ConnectDialog adds or edits a host. "저장 후 연결" saves and immediately
// connects; "저장"만 누르면 목록에만 추가된다. sudo는 입력한 비밀번호로
// 자동 시도하므로 별도 입력란이 없다.
export default function ConnectDialog({ initial, onSaved, onClose }: Props) {
  const editing = !!initial;
  const [name, setName] = useState(initial?.name ?? "");
  const [addr, setAddr] = useState(initial?.addr ?? "");
  const [port, setPort] = useState(initial?.port ?? 22);
  const [user, setUser] = useState(initial?.user ?? "");
  const [password, setPassword] = useState(initial?.password ?? "");
  const [keyPath, setKeyPath] = useState(initial?.keyPath ?? "");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

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
