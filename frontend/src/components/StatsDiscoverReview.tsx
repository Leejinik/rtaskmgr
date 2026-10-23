import { useState } from "react";
import { host, statsreg } from "../../wailsjs/go/models";
import { KafkaRow } from "./StatsConnectionFields";
import { StatsDiscoverFromHosts, StatsDiscoverFromCluster } from "../../wailsjs/go/main/App";
import "./StatsDiscover.css";

// 「서버에서 불러오기」 result (Go: statsreg.DiscoverResult).
export interface DiscoverField { key: string; label: string; value: string; secret: boolean; source: string }
export interface DiscoverKafkaRow { hostname: string; ip: string; port: number; source: string; reachable: boolean }
export interface DiscoverKafkaOption { listener: string; security: string; interBroker: boolean; rows: DiscoverKafkaRow[]; reachable: number; brokers: number; note: string }
export interface DiscoverServer { name: string; addr: string; hostname: string; via: string; elevated: boolean; error: string }
export interface DiscoverResult { fields: DiscoverField[]; kafka: DiscoverKafkaOption[]; kafkaChoice: number; kafkaReason: string; warnings: string[]; servers: DiscoverServer[] }

// Empty Go slices arrive as null.
function normalize(r: statsreg.DiscoverResult): DiscoverResult {
  const d = r as unknown as DiscoverResult;
  return {
    ...d,
    fields: d.fields ?? [],
    kafka: (d.kafka ?? []).map((o) => ({ ...o, rows: o.rows ?? [] })),
    warnings: d.warnings ?? [],
    servers: d.servers ?? [],
  };
}

export function discoverFromHosts(hosts: host.Host[]): Promise<DiscoverResult> {
  return StatsDiscoverFromHosts(hosts).then(normalize);
}

export function discoverFromCluster(clusterId: string): Promise<DiscoverResult> {
  return StatsDiscoverFromCluster(clusterId).then(normalize);
}

const numeric = new Set(["dbPort", "redisPort", "redisDB"]);
const securities = new Set(["PLAINTEXT", "SSL", "SASL_PLAINTEXT", "SASL_SSL"]);

// A field still holding what the dialog put there by itself (empty, a default,
// or the first server's address) is filled without asking; anything else was
// typed by the user and is only replaced when ticked.
function untouched(cur: string, baseline: string) {
  return cur.trim() === "" || cur === baseline;
}

export default function StatsDiscoverReview({ result, config, baseline, kafkaRows, onApply, onClose }: {
  result: DiscoverResult;
  config: statsreg.Config;
  baseline: statsreg.Config;
  kafkaRows: KafkaRow[];
  onApply: (config: statsreg.Config, kafkaRows: KafkaRow[] | null) => void;
  onClose: () => void;
}) {
  const cur = (k: string) => String((config as any)[k] ?? "");
  const [checked, setChecked] = useState<Record<string, boolean>>(() => {
    const out: Record<string, boolean> = {};
    for (const f of result.fields) out[f.key] = cur(f.key) !== f.value && untouched(cur(f.key), String((baseline as any)[f.key] ?? ""));
    return out;
  });
  const [choice, setChoice] = useState(result.kafkaChoice);
  const kafkaUntouched = kafkaRows.every((r) => r.hostname.trim() === "");
  const [useKafka, setUseKafka] = useState(result.kafkaChoice >= 0 && kafkaUntouched);
  const option = choice >= 0 ? result.kafka[choice] : undefined;

  function apply() {
    const next: any = { ...config };
    for (const f of result.fields) {
      if (checked[f.key] && cur(f.key) !== f.value) next[f.key] = numeric.has(f.key) ? Number(f.value) : f.value;
    }
    let rows: KafkaRow[] | null = null;
    if (useKafka && option) {
      rows = option.rows.map((r) => ({ hostname: r.hostname, ip: r.ip, port: r.port }));
      if (securities.has(option.security)) next.kafkaSecurity = option.security;
    }
    onApply(next as statsreg.Config, rows);
  }

  const shown = (f: DiscoverField, v: string) => (f.secret ? (v ? `●●●● (${v.length}자)` : "") : v);
  const changes = result.fields.filter((f) => checked[f.key] && cur(f.key) !== f.value).length + (useKafka && option ? 1 : 0);
  const ok = result.servers.filter((s) => !s.error);

  return <div className="sd-panel">
    <div className="sd-head">
      <b>서버에서 불러온 값</b>
      <span className="sd-muted">
        {ok.length}/{result.servers.length}대에서 읽음 —{" "}
        {result.servers.map((s, i) => <span key={i} className={s.error ? "sd-bad" : ""}>
          {i > 0 && " · "}{s.hostname || s.name}{s.via ? ` (경유 ${s.via})` : ""}{s.error ? " 실패" : s.elevated ? "" : " (sudo 없이)"}
        </span>)}
      </span>
    </div>

    {result.warnings.length > 0 && <ul className="sd-warn">{result.warnings.map((w, i) => <li key={i}>{w}</li>)}</ul>}

    {result.fields.length > 0 && <table className="sd-table">
      <thead><tr><th></th><th>항목</th><th>지금</th><th>불러온 값</th><th>어디서</th></tr></thead>
      <tbody>{result.fields.map((f) => {
        const same = cur(f.key) === f.value;
        return <tr key={f.key} className={same ? "sd-same" : ""}>
          <td><input type="checkbox" disabled={same} checked={!same && !!checked[f.key]}
            onChange={(e) => setChecked({ ...checked, [f.key]: e.target.checked })} /></td>
          <td>{f.label}</td>
          <td>{same ? "같음" : cur(f.key) ? <span className={checked[f.key] ? "sd-old" : ""}>{f.secret ? "입력됨(다른 값)" : cur(f.key)}</span> : <span className="sd-muted">비어 있음</span>}</td>
          <td><code>{shown(f, f.value)}</code></td>
          <td className="sd-src">{f.source}</td>
        </tr>;
      })}</tbody>
    </table>}

    {result.kafka.length > 0 && <div className="sd-kafka">
      <label className="sd-check">
        <input type="checkbox" checked={useKafka} onChange={(e) => setUseKafka(e.target.checked)} />
        Kafka 서버 목록을 아래 값으로 바꾸기{!kafkaUntouched && <span className="sd-muted"> (지금 입력된 {kafkaRows.length}행: {kafkaRows.map((r) => `${r.hostname || "?"}=${r.ip}:${r.port}`).join(", ")})</span>}
      </label>
      {result.kafka.length > 1 && <div className="sd-options">
        {result.kafka.map((o, i) => <label key={o.listener} className="sd-check">
          <input type="radio" name="sd-listener" checked={choice === i} onChange={() => setChoice(i)} />
          <b>{o.listener}</b> <span className="sd-muted">{o.security || "?"} · {o.note}</span>
          {i === result.kafkaChoice && <span className="sd-badge">추천</span>}
        </label>)}
      </div>}
      {result.kafkaChoice >= 0 && <p className="sd-muted">{result.kafkaReason}</p>}
      {option && <table className="sd-table">
        <thead><tr><th>호스트 이름 (브로커가 알려 주는 이름)</th><th>IP</th><th>포트</th><th>이 PC에서</th><th>어디서</th></tr></thead>
        <tbody>{option.rows.map((r, i) => <tr key={i}>
          <td><code>{r.hostname}</code></td><td><code>{r.ip}</code></td><td>{r.port}</td>
          <td className={r.reachable ? "sd-good" : "sd-bad"}>{r.reachable ? "접속됨" : "접속 안 됨"}</td>
          <td className="sd-src">{r.source}</td>
        </tr>)}</tbody>
      </table>}
      {option && securities.has(option.security) && <p className="sd-muted">Kafka 보안은 {option.security}로 맞춥니다.</p>}
    </div>}

    <div className="sd-actions">
      <span className="sd-muted">서버 설정은 읽기만 했습니다. 체크한 항목만 위 입력란에 채웁니다 — 직접 입력해 둔 값은 체크해야 바뀝니다.</span>
      <button className="toolbtn" onClick={onClose}>닫기</button>
      <button className="toolbtn primary" disabled={changes === 0} onClick={apply}>선택한 {changes}개 채우기</button>
    </div>
  </div>;
}
