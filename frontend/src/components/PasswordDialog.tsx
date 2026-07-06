import { useEffect, useState } from "react";
import { pwledger } from "../../wailsjs/go/models";
import { EventsOn } from "../../wailsjs/runtime";
import {
  PwConfig, SetPwConfig, PwLedger, RenewPasswords, ChangePasswords,
} from "../../wailsjs/go/main/App";
import { PwInfo, expLabel, warnLevel } from "../pw";

interface Props {
  hostId: string;
  hostName: string;
  connected: boolean;
  info: PwInfo | undefined;
  currentPassword: string; // stored login password (== liz/root current, by convention)
  onClose: () => void;
  onToast: (m: string) => void;
  onChanged?: () => void; // called after a rotation so the caller can refresh
}

type Mode = "menu" | "change";

const fmtAt = (t: string) => {
  const d = new Date(t);
  if (isNaN(d.getTime())) return t;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
};

const stepLabel = (s: string) =>
  s === "to-temp" ? "임시PW" : s === "to-current" ? "현재PW" : s === "to-new" ? "새PW" : s;

export default function PasswordDialog({ hostId, hostName, connected, info, currentPassword, onClose, onToast, onChanged }: Props) {
  const [cfg, setCfg] = useState<pwledger.Config | null>(null);
  const [tempPw, setTempPw] = useState("");
  const [entries, setEntries] = useState<pwledger.Entry[]>([]);
  const [reveal, setReveal] = useState(false);
  const [mode, setMode] = useState<Mode>("menu");
  const [newPw, setNewPw] = useState("");
  const [newPw2, setNewPw2] = useState("");
  const [busy, setBusy] = useState(false);
  const [log, setLog] = useState<string[]>([]);

  const loadLedger = () => { PwLedger(hostId).then((e) => setEntries(e ?? [])).catch(() => {}); };

  useEffect(() => {
    PwConfig().then((c) => { setCfg(c); setTempPw(c.tempPassword); }).catch(() => {});
    loadLedger();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hostId]);

  // Live progress: mirror each rotation step to the log while a run is in flight.
  useEffect(() => {
    const off = EventsOn("pwrotate", (s: any) => {
      if (s.hostId && s.hostId !== hostId) return;
      if (s.status === "pending") {
        setLog((l) => [...l, `→ ${s.account} ${stepLabel(s.step)} 적용 중…`]);
      } else if (s.status === "ok") {
        setLog((l) => [...l, `  ✓ 적용됨`]);
        loadLedger();
      } else if (s.status === "fail") {
        setLog((l) => [...l, `  ✗ 실패: ${s.err || ""}`]);
        loadLedger();
      }
    });
    return () => { off(); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hostId]);

  const warnDays = cfg?.expiryWarnDays ?? 10;

  const acctRow = (label: string, has: boolean, exp: number) => {
    if (!has) return <div className="pw-acct"><span className="pw-acct-name">{label}</span><span className="muted">계정 없음</span></div>;
    const lvl = warnLevel(exp, warnDays);
    const cls = lvl === "expired" ? "bad" : lvl === "warn" ? "warn" : lvl === "never" ? "muted" : "good";
    return (
      <div className="pw-acct">
        <span className="pw-acct-name">{label}</span>
        <span className={`pw-exp ${cls}`}>{expLabel(exp)}</span>
      </div>
    );
  };

  async function doRenew() {
    if (busy) return;
    setBusy(true);
    setLog([`[갱신] ${hostName} — 현재PW로 만료일만 갱신 (현재→임시→현재)`]);
    try {
      await RenewPasswords(hostId);
      setLog((l) => [...l, "완료: 만료일이 갱신되었습니다."]);
      onToast(`${hostName}: 패스워드 만료일 갱신 완료`);
    } catch (e: any) {
      const msg = typeof e === "string" ? e : e?.message ?? "갱신 실패";
      setLog((l) => [...l, `중단: ${msg}`, "※ 이력 탭에서 현재 설정된 패스워드를 확인하세요."]);
      onToast(`갱신 실패: ${msg}`);
    } finally {
      setBusy(false);
      loadLedger();
      onChanged?.();
    }
  }

  async function doChange() {
    if (busy) return;
    if (newPw.length < 1) { onToast("새 패스워드를 입력하세요"); return; }
    if (newPw !== newPw2) { onToast("두 패스워드가 일치하지 않습니다"); return; }
    setBusy(true);
    setLog([`[변경] ${hostName} — liz·root를 새 패스워드로 변경 (현재→새PW)`]);
    try {
      await ChangePasswords(hostId, newPw);
      setLog((l) => [...l, "완료: liz·root 패스워드가 변경되었습니다. 저장된 접속 정보도 갱신했습니다."]);
      onToast(`${hostName}: 패스워드 변경 완료`);
      setNewPw(""); setNewPw2(""); setMode("menu");
    } catch (e: any) {
      const msg = typeof e === "string" ? e : e?.message ?? "변경 실패";
      setLog((l) => [...l, `중단: ${msg}`, "※ 이력 탭에서 현재 설정된 패스워드를 확인하세요."]);
      onToast(`변경 실패: ${msg}`);
    } finally {
      setBusy(false);
      loadLedger();
      onChanged?.();
    }
  }

  async function saveTemp() {
    if (!cfg) return;
    try {
      const next = { ...cfg, tempPassword: tempPw } as pwledger.Config;
      await SetPwConfig(next);
      setCfg(next);
      onToast("임시 패스워드를 저장했습니다");
    } catch (e: any) {
      onToast("임시 패스워드 저장 실패");
    }
  }

  return (
    <div className="scrim" onMouseDown={onClose}>
      <div className="modal pw-modal" onMouseDown={(e) => e.stopPropagation()} style={{ minWidth: 520, maxWidth: 640 }}>
        <h2>🔑 패스워드 관리 — {hostName}</h2>

        <div className="pw-body">
          <div className="pw-status">
            <div className="pw-acct">
              <span className="pw-acct-name">현재PW</span>
              <span className="mono" style={{ userSelect: "text" }}>{currentPassword || "(없음)"}</span>
              <span className="muted" style={{ fontSize: 11 }}>liz·root 공통</span>
            </div>
            {acctRow("liz", !!info?.hasLiz, info?.lizExpDays ?? 0)}
            {acctRow("root", !!info?.hasRoot, info?.rootExpDays ?? 0)}
            {info?.err && <div className="bad" style={{ fontSize: 12 }}>조회 실패: {info.err}</div>}
            {!info && <div className="muted" style={{ fontSize: 12 }}>연결하면 만료일을 조회합니다.</div>}
          </div>

          {!connected && (
            <div className="pw-note bad">연결된 상태에서만 갱신/변경할 수 있습니다.</div>
          )}

          {mode === "menu" && (
            <div className="pw-actions">
              <button className="toolbtn" disabled={!connected || busy} onClick={doRenew}
                title="현재 패스워드는 그대로 두고 만료일만 갱신합니다 (현재→임시→현재)">
                📅 날짜만 갱신
              </button>
              <button className="toolbtn" disabled={!connected || busy} onClick={() => setMode("change")}
                title="liz·root를 새 패스워드로 변경합니다 (한 번만 입력)">
                🔒 다른 패스워드로 변경…
              </button>
            </div>
          )}

          {mode === "change" && (
            <div className="pw-change">
              <div className="muted" style={{ fontSize: 12, marginBottom: 6 }}>
                liz·root 공통으로 적용됩니다. 한 번만 입력하세요.
              </div>
              <input type="password" placeholder="새 패스워드" value={newPw} autoFocus
                disabled={busy} onChange={(e) => setNewPw(e.target.value)} />
              <input type="password" placeholder="새 패스워드 확인" value={newPw2}
                disabled={busy} onChange={(e) => setNewPw2(e.target.value)} />
              <div style={{ display: "flex", gap: 6, marginTop: 6 }}>
                <button className="toolbtn danger" disabled={busy} onClick={doChange}>변경 실행</button>
                <button className="toolbtn" disabled={busy} onClick={() => { setMode("menu"); setNewPw(""); setNewPw2(""); }}>취소</button>
              </div>
            </div>
          )}

          {log.length > 0 && (
            <div className="pw-log">
              {log.map((l, i) => <div key={i}>{l}</div>)}
              {busy && <div className="muted">진행 중… 완료까지 창을 닫지 마세요.</div>}
            </div>
          )}

          <div className="pw-temp">
            <label className="muted" style={{ fontSize: 12 }}>임시 패스워드 (갱신 시 경유)</label>
            <div style={{ display: "flex", gap: 6 }}>
              <input type={reveal ? "text" : "password"} value={tempPw}
                onChange={(e) => setTempPw(e.target.value)} style={{ flex: 1 }} />
              <button className="toolbtn" onClick={saveTemp} disabled={!cfg || tempPw === cfg.tempPassword}>저장</button>
            </div>
          </div>

          <div className="pw-hist">
            <div className="pw-hist-head">
              <span>변경 이력 (최신순)</span>
              <label className="muted" style={{ fontSize: 11, display: "flex", gap: 4, alignItems: "center" }}>
                <input type="checkbox" checked={reveal} onChange={(e) => setReveal(e.target.checked)} />
                패스워드 표시
              </label>
            </div>
            <div className="pw-hist-body">
              {entries.length === 0 ? (
                <div className="muted" style={{ fontSize: 12, padding: 8 }}>이력이 없습니다.</div>
              ) : (
                <table className="pw-hist-table">
                  <thead>
                    <tr><th>시각</th><th>계정</th><th>작업</th><th>단계</th><th>패스워드</th><th>상태</th></tr>
                  </thead>
                  <tbody>
                    {entries.map((e) => (
                      <tr key={e.id} className={e.status === "pending" ? "pending" : e.status === "fail" ? "fail" : ""}>
                        <td>{fmtAt(e.at as any)}</td>
                        <td>{e.account}</td>
                        <td>{e.op === "renew" ? "갱신" : "변경"}</td>
                        <td>{stepLabel(e.step)}</td>
                        <td className="mono">{reveal ? e.password : "••••••"}</td>
                        <td>{e.status === "ok" ? "✓" : e.status === "pending" ? "⏳" : "✗"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
            <div className="muted" style={{ fontSize: 11, marginTop: 4 }}>
              ⏳ 상태(pending)는 적용 중 중단된 단계일 수 있습니다. 해당 패스워드로 접속을 시도해 보세요.
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
