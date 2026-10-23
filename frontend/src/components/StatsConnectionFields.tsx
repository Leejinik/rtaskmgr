import { statsreg } from "../../wailsjs/go/models";
import "./StatsRegistration.css";
import "./StatsDiscover.css";
import { badInput } from "./statsMessage";

export interface KafkaRow { hostname: string; ip: string; port: number }

export const defaultStatsConfig = (host: string): statsreg.Config => ({ dbHost: host, dbPort: 3306, dbUser: "root", dbPassword: "", redisHost: host, redisPort: 5000, redisUser: "", redisPassword: "", redisDB: 0, brokers: "", kafkaSecurity: "PLAINTEXT", kafkaUser: "", kafkaPassword: "", kafkaHosts: "" });

// statsConnection checks the Kafka rows and derives brokers/kafkaHosts from them.
export function statsConnection(config: statsreg.Config, kafkaRows: KafkaRow[]): statsreg.Config {
  const rows = kafkaRows.map(r => ({ hostname: r.hostname.trim(), ip: r.ip.trim(), port: r.port }));
  if (rows.some(r => !r.hostname || !r.ip || !Number.isInteger(r.port) || r.port < 1 || r.port > 65535)) throw new Error("Kafka의 호스트 이름, IP, 포트(1~65535)를 모두 입력하세요.");
  const names = rows.map(r => r.hostname.toLowerCase().replace(/\.$/, ""));
  if (new Set(names).size !== names.length) throw new Error("Kafka 호스트 이름이 중복됩니다.");
  const address = (r: KafkaRow) => `${r.ip.includes(":") && !r.ip.startsWith("[") ? `[${r.ip}]` : r.ip}:${r.port}`;
  return { ...config, brokers: rows.map(r => `${r.hostname}:${r.port}`).join(","), kafkaHosts: rows.map(r => `${r.hostname} = ${address(r)}`).join("\n") };
}

// The MariaDB / Redis / Kafka inputs, shared by the registration dialog and the
// cluster dialog (where a cluster keeps its own copy). onDiscover adds the
// 「서버에서 불러오기」 button (the cluster dialog passes it; the values are
// reviewed there before anything is filled).
// bad: the input a failed connection check named (db.host, kafka.1.port …), marked red.
export default function StatsConnectionFields({ config, setConfig, kafkaRows, setKafkaRows, disabled = false, onDiscover, discovering = false, bad = "" }: {
  config: statsreg.Config;
  setConfig: (c: statsreg.Config) => void;
  kafkaRows: KafkaRow[];
  setKafkaRows: (f: (rows: KafkaRow[]) => KafkaRow[]) => void;
  disabled?: boolean;
  onDiscover?: () => void;
  discovering?: boolean;
  bad?: string;
}) {
  const codes: Partial<Record<keyof statsreg.Config, string>> = { dbHost: "db.host", dbPort: "db.port", dbUser: "db.user", dbPassword: "db.password", redisHost: "redis.host", redisPort: "redis.port", redisUser: "redis.password", redisPassword: "redis.password" };
  const mark = (code: string) => badInput(bad, code) ? "stats-bad" : undefined;
  const input = (label: string, key: keyof statsreg.Config, type = "text") => <label>{label}<input type={type} className={mark(codes[key] || "")} aria-invalid={!!mark(codes[key] || "")} value={String(config[key])} autoComplete="off" onChange={e => setConfig({ ...config, [key]: type === "number" ? Number(e.target.value) : e.target.value })} /></label>;
  return <div className="stats-fields">
    {onDiscover && <div className="stats-discover-bar"><button className="toolbtn" disabled={disabled || discovering} onClick={onDiscover}>{discovering ? "불러오는 중…" : "서버에서 불러오기"}</button><span className="stats-muted">클러스터 서버에 SSH로 접속해 redis.conf · Kafka server.properties · collector.properties · admin application.yml · /etc/hosts 를 읽고, 채울 값과 출처를 보여 줍니다. 서버는 읽기만 합니다.</span></div>}
    <div className="stats-connections">
      <fieldset disabled={disabled}><legend>MariaDB · liz</legend>{input("서버 주소", "dbHost")}{input("포트", "dbPort", "number")}{input("계정", "dbUser")}{input("비밀번호", "dbPassword", "password")}</fieldset>
      <fieldset disabled={disabled}><legend>Redis</legend>{input("서버 주소 (Cluster면 전부, 쉼표로)", "redisHost")}{input("포트", "redisPort", "number")}{input("ACL 계정 (선택)", "redisUser")}{input("비밀번호", "redisPassword", "password")}{input("DB 번호", "redisDB", "number")}</fieldset>
      <fieldset disabled={disabled}><legend>Kafka 보안</legend><label>보안<select value={config.kafkaSecurity} onChange={e => setConfig({ ...config, kafkaSecurity: e.target.value })}><option>PLAINTEXT</option><option>SSL</option><option>SASL_PLAINTEXT</option><option>SASL_SSL</option></select></label>{config.kafkaSecurity.startsWith("SASL") && <>{input("SASL/PLAIN 계정", "kafkaUser")}{input("비밀번호", "kafkaPassword", "password")}</>}<p className="stats-muted">아래에 Kafka 서버를 추가하세요. 입력한 호스트 이름을 IP와 포트에 바인딩합니다.</p></fieldset>
    </div>
    <fieldset disabled={disabled} className="stats-host-bindings"><legend>Kafka 서버</legend>{kafkaRows.map((row, index) => <div className="stats-kafka-row" key={index}><label>호스트 이름<input aria-label={`Kafka ${index + 1} 호스트 이름`} className={mark(`kafka.${index}.host`)} placeholder="server_1 또는 server01" value={row.hostname} onChange={e => setKafkaRows(rows => rows.map((r, i) => i === index ? { ...r, hostname: e.target.value } : r))} /></label><label>IP<input aria-label={`Kafka ${index + 1} IP`} className={mark(`kafka.${index}.ip`)} placeholder="192.0.2.11" value={row.ip} onChange={e => setKafkaRows(rows => rows.map((r, i) => i === index ? { ...r, ip: e.target.value } : r))} /></label><label>포트<input aria-label={`Kafka ${index + 1} 포트`} className={mark(`kafka.${index}.port`)} type="number" min={1} max={65535} value={row.port} onChange={e => setKafkaRows(rows => rows.map((r, i) => i === index ? { ...r, port: Number(e.target.value) } : r))} /></label><button className="toolbtn" aria-label={`Kafka ${index + 1} 삭제`} disabled={kafkaRows.length === 1} onClick={() => setKafkaRows(rows => rows.filter((_, i) => i !== index))}>−</button></div>)}<button className="toolbtn" onClick={() => setKafkaRows(rows => [...rows, { hostname: "", ip: "", port: 9092 }])}>+ 서버 추가</button><p className="stats-muted">호스트 이름은 Kafka가 알려주는 이름 그대로 입력하세요. 연결 확인·알림 전송·재시도에 동일하게 적용합니다.</p></fieldset>
  </div>;
}
