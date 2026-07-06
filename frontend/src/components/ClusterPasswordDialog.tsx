import { useEffect, useState } from "react";
import { host, pwledger } from "../../wailsjs/go/models";
import { EventsOn } from "../../wailsjs/runtime";
import {
  PwConfig, SetPwConfig, PwLedger, RenewPasswords, ChangePasswords,
} from "../../wailsjs/go/main/App";
import { PwInfo, expLabel, warnLevel } from "../pw";

interface Props {
  clusterName: string;
  hosts: host.Host[];
  connectedIds: Set<string>;
  pwStatus: Record<string, PwInfo>;
  onClose: () => void;
  onToast: (m: string) => void;
  onChanged?: () => void; // called after a batch rotation so the caller can refresh
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

export default function ClusterPasswordDialog({ clusterName, hosts, connectedIds, pwStatus, onClose, onToast, onChanged }: Props) {
  const [cfg, setCfg] = useState<pwledger.Config | null>(null);
  const [tempPw, setTempPw] = useState("");
  const [entries, setEntries] = useState<pwledger.Entry[]>([]);
  const [reveal, setReveal] = useState(false);
  const [mode, setMode] = useState<Mode>("menu");
  const [newPw, setNewPw] = useState("");
  const [newPw2, setNewPw2] = useState("");
  const [busy, setBusy] = useState(false);
  const [log, setLog] = useState<string[]>([]);

  const memberIds = new Set(hosts.map((h) => h.id));
  const targets = hosts.filter((h) => connectedIds.has(h.id));

  const loadLedger = () => {
    PwLedger("").then((all) => setEntries((all ?? []).filter((e) => memberIds.has(e.hostId)))).catch(() => {});
  };

  useEffect(() => {
    PwConfig().then((c) => { setCfg(c); setTempPw(c.tempPassword); }).catch(() => {});
    loadLedger();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clusterName]);

  useEffect(() => {
    const off = EventsOn("pwrotate", (s: any) => {
      if (!s.hostId || !memberIds.has(s.hostId)) return;
      if (s.status === "ok") loadLedger();
      else if (s.status === "fail") loadLedger();
    });
    return () => { off(); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clusterName]);

  const warnDays = cfg?.expiryWarnDays ?? 10;

  async function runBatch(kind: "renew" | "change") {
    if (busy) return;
    if (!targets.length) { onToast("연결된 서버가 없습니다. 먼저 연결하세요."); return; }
    if (kind === "change") {
      if (newPw.length < 1) { onToast("새 패스워드를 입력하세요"); return; }
      if (newPw !== newPw2) { onToast("두 패스워드가 일치하지 않습니다"); return; }
    }
    const skipped = hosts.length - targets.length;
    setBusy(true);
    setLog([
      kind === "renew"
        ? `[전체 갱신] ${clusterName} — 연결된 ${targets.length}대 (현재→임시→현재)`
        : `[전체 변경] ${clusterName} — 연결된 ${targets.length}대 (현재→새PW)`,
      ...(skipped > 0 ? [`※ 미연결 ${skipped}대는 건너뜁니다.`] : []),
    ]);
    let okc = 0, failc = 0;
    for (const h of targets) {
      setLog((l) => [...l, `→ [${h.name}] 진행 중…`]);
      try {
        if (kind === "renew") await RenewPasswords(h.id);
        else await ChangePasswords(h.id, newPw);
        setLog((l) => [...l, `  ✓ [${h.name}] 완료`]);
        okc++;
      } catch (e: any) {
        const msg = typeof e === "string" ? e : e?.message ?? "실패";
        setLog((l) => [...l, `  ✗ [${h.name}] 실패: ${msg}`]);
        failc++;
      }
    }
    setLog((l) => [...l, `완료: 성공 ${okc} · 실패 ${failc}${failc ? " — 이력에서 각 서버의 현재 패스워드를 확인하세요." : ""}`]);
    onToast(`${clusterName}: 성공 ${okc} / 실패 ${failc}`);
    if (kind === "change") { setNewPw(""); setNewPw2(""); setMode("menu"); }
    setBusy(false);
    loadLedger();
    onChanged?.();
  }

  async function saveTemp() {
    if (!cfg) return;
    try {
      const next = { ...cfg, tempPassword: tempPw } as pwledger.Config;
      await SetPwConfig(next);
      setCfg(next);
      onToast("임시 패스워드를 저장했습니다");
    } catch { onToast("임시 패스워드 저장 실패"); }
  }

  const expCell = (has: boolean, exp: number) => {
    if (!has) return <span className="muted">-</span>;
    const lvl = warnLevel(exp, warnDays);
    const cls = lvl === "expired" ? "bad" : lvl === "warn" ? "warn" : lvl === "never" ? "muted" : "good";
    return <span className={cls}>{expLabel(exp)}</span>;
  };

  return (
    <div className="scrim" onMouseDown={onClose}>
      <div className="modal pw-modal" onMouseDown={(e) => e.stopPropagation()} style={{ minWidth: 620, maxWidth: 760 }}>
        <h2>🔑 클러스터 패스워드 관리 — {clusterName}</h2>

        <div className="pw-body">
          <div className="pw-hist-body" style={{ maxHeight: 210 }}>
            <table className="pw-hist-table">
              <thead>
                <tr><th>서버</th><th>연결</th><th>현재PW</th><th>liz 만료</th><th>root 만료</th></tr>
              </thead>
              <tbody>
                {hosts.map((h) => {
                  const info = pwStatus[h.id];
                  return (
                    <tr key={h.id}>
                      <td>{h.name} <span className="muted">{h.user}@{h.addr}</span></td>
                      <td>{connectedIds.has(h.id) ? <span className="good">연결됨</span> : <span className="muted">미연결</span>}</td>
                      <td className="mono" style={{ userSelect: "text" }}>{h.password || "-"}</td>
                      <td>{expCell(!!info?.hasLiz, info?.lizExpDays ?? 0)}</td>
                      <td>{expCell(!!info?.hasRoot, info?.rootExpDays ?? 0)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>

          {targets.length === 0 && <div className="pw-note bad">연결된 서버가 없습니다. 먼저 연결하세요.</div>}

          {mode === "menu" && (
            <div className="pw-actions">
              <button className="toolbtn" disabled={busy || !targets.length} onClick={() => runBatch("renew")}
                title="연결된 모든 서버의 liz·root 만료일을 갱신합니다 (현재→임시→현재)">
                📅 전체 날짜 갱신 ({targets.length}대)
              </button>
              <button className="toolbtn" disabled={busy || !targets.length} onClick={() => setMode("change")}
                title="연결된 모든 서버의 liz·root를 새 패스워드로 변경합니다">
                🔒 전체 다른 패스워드로 변경…
              </button>
            </div>
          )}

          {mode === "change" && (
            <div className="pw-change">
              <div className="muted" style={{ fontSize: 12, marginBottom: 6 }}>
                연결된 {targets.length}대의 liz·root 공통으로 적용됩니다. 한 번만 입력하세요.
              </div>
              <input type="password" placeholder="새 패스워드" value={newPw} autoFocus
                disabled={busy} onChange={(e) => setNewPw(e.target.value)} />
              <input type="password" placeholder="새 패스워드 확인" value={newPw2}
                disabled={busy} onChange={(e) => setNewPw2(e.target.value)} />
              <div style={{ display: "flex", gap: 6, marginTop: 6 }}>
                <button className="toolbtn danger" disabled={busy} onClick={() => runBatch("change")}>전체 변경 실행</button>
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
              <span>변경 이력 (클러스터 전체, 최신순)</span>
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
                    <tr><th>시각</th><th>서버</th><th>계정</th><th>작업</th><th>단계</th><th>패스워드</th><th>상태</th></tr>
                  </thead>
                  <tbody>
                    {entries.map((e) => (
                      <tr key={e.id} className={e.status === "pending" ? "pending" : e.status === "fail" ? "fail" : ""}>
                        <td>{fmtAt(e.at as any)}</td>
                        <td>{e.hostName}</td>
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
