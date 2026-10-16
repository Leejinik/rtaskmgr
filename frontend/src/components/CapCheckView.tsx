// 용량 점검 — 클러스터 뷰의 다섯 번째 모드.
//
// AWS 2M 부하 시험에서 만든 현장 스크립트 두 개(Redis 메인 스레드 상한, Kafka 브로커 스레드·파티션)를
// 연결된 모든 서버에서 동시에 돌린다. 스크립트는 현장에 주는 파일과 같은 파일이다.
// 조회만 한다. 단 하나 실행 중인 JVM 을 건드리는 jstack 은 옵션이고 기본은 끈다 — 브로커 전체가
// 잠깐 멈춘다(2026-10-15 시험: 한 번 최대 2.4ms, 종료 0회). -Xrs 로 뜬 브로커는 스크립트가 보수적으로 건너뛴다.
import { useEffect, useMemo, useRef, useState } from "react";
import { host } from "../../wailsjs/go/models";
import { CapacityRun, CapacityLast } from "../../wailsjs/go/main/App";

interface Props {
  hosts: host.Host[];
  connected: Record<string, boolean>;
}

const SEC_OPTS = [10, 20, 30, 60];

function vcls(v: string): string {
  switch (v) {
    case "여유 있음": return "gt-v-good";
    case "여유 적음": case "늘릴 준비": return "gt-v-warn";
    case "증설 검토": case "상한 도달": case "파티션 늘림": case "한계": case "복제 빠짐": return "gt-v-bad";
    default: return "gt-v-dim";
  }
}
const man = (v: number) => (Number.isFinite(v) ? `${(v / 10000).toFixed(1)}만` : "—");
const pc = (v: number) => (Number.isFinite(v) ? `${v.toFixed(1)}%` : "—");

export default function CapCheckView({ hosts, connected }: Props) {
  const [reports, setReports] = useState<any[]>([]);
  const [running, setRunning] = useState(false);
  const [elapsed, setElapsed] = useState(0);
  const [err, setErr] = useState("");
  const [sec, setSec] = useState(20);
  const [topic, setTopic] = useState("liz.checkvalue.topic");
  const [doKafka, setDoKafka] = useState(true);
  const [doRedis, setDoRedis] = useState(true);
  const [jstack, setJstack] = useState(false);
  const alive = useRef(true);

  const ids = useMemo(() => hosts.map((h) => h.id), [hosts]);
  const liveIds = useMemo(() => ids.filter((id) => connected[id]), [ids, connected]);

  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);
  useEffect(() => {
    CapacityLast(ids).then((r) => { if (alive.current && !running) setReports(r ?? []); });
  }, [ids.join(",")]);
  useEffect(() => {
    if (!running) return;
    const t0 = Date.now();
    const iv = setInterval(() => setElapsed(Math.floor((Date.now() - t0) / 1000)), 500);
    return () => clearInterval(iv);
  }, [running]);

  async function run() {
    if (liveIds.length === 0) { setErr("연결된 서버가 없습니다. 먼저 ‘모두 연결’ 하세요."); return; }
    if (!doKafka && !doRedis) { setErr("Kafka 와 Redis 중 하나는 골라야 합니다."); return; }
    setRunning(true); setElapsed(0); setErr("");
    try {
      const got = (await CapacityRun(liveIds, { seconds: sec, topic, jstack: doKafka && jstack, kafka: doKafka, redis: doRedis } as any)) ?? [];
      if (alive.current) setReports(got);
    } catch (e: any) {
      if (alive.current) setErr(String(e?.message ?? e));
    } finally {
      if (alive.current) setRunning(false);
    }
  }

  const redisRows = reports.flatMap((r) => (r.result?.redis ?? []).map((x: any) => ({ h: r, x })));
  redisRows.sort((a, b) => (a.x.role === b.x.role ? 0 : a.x.role === "master" ? -1 : 1) || a.h.hostName.localeCompare(b.h.hostName) || a.x.port - b.x.port);
  const kafkaRows = reports.filter((r) => r.result?.kafka).map((r) => ({ h: r, k: r.result.kafka }));
  const partSrc = kafkaRows.find((r) => (r.k.parts ?? []).length > 0);
  const hostErrs = reports.filter((r) => r.err);
  const measuredAt = Math.max(0, ...reports.map((r) => r.result?.nowMs || 0));
  const nothing = reports.length > 0 && redisRows.length === 0 && kafkaRows.length === 0 && hostErrs.length === 0;

  return (
    <div className="lc-wrap">
      <div className="lc-bar">
        <button className={"toolbtn primary" + (jstack && doKafka ? " danger" : "")} disabled={running} onClick={run}
          title="연결된 모든 서버에서 동시에 잽니다">
          {running ? `측정 중… ${elapsed}초` : jstack && doKafka ? "▶ 측정 (jstack 포함)" : "▶ 측정"}
        </button>
        <label className="mini-sel" title="측정 구간. 수집량이 가장 많은 시간대에 잽니다">
          구간
          <select value={sec} disabled={running} onChange={(e) => setSec(Number(e.target.value))}>
            {SEC_OPTS.map((s) => <option key={s} value={s}>{s}초</option>)}
          </select>
        </label>
        <label className="lc-check"><input type="checkbox" checked={doRedis} disabled={running} onChange={(e) => setDoRedis(e.target.checked)} />Redis</label>
        <label className="lc-check"><input type="checkbox" checked={doKafka} disabled={running} onChange={(e) => setDoKafka(e.target.checked)} />Kafka</label>
        {doKafka && (
          <label className="mini-sel" title="파티션 적재량을 볼 토픽">
            토픽
            <input className="cc-topic" value={topic} disabled={running} onChange={(e) => setTopic(e.target.value.trim())} />
          </label>
        )}
        {doKafka && (
          <label className="lc-check" title="스레드를 이름으로 나눠 봅니다. 브로커 JVM 이 잠깐 멈춥니다">
            <input type="checkbox" checked={jstack} disabled={running} onChange={(e) => setJstack(e.target.checked)} />
            스레드 이름 구분(jstack)
          </label>
        )}
        <span style={{ flex: 1 }} />
        <span className="lc-dim">
          {measuredAt > 0 ? `마지막 측정 ${new Date(measuredAt).toLocaleTimeString()}` : "측정 기록 없음"} · 연결 {liveIds.length}/{ids.length}
        </span>
      </div>

      {doKafka && jstack && (
        <div className="lc-warn">
          ⚠ jstack 은 브로커 JVM 전체를 잠깐 멈춥니다(safepoint, 한 번에 3회 · 시험에서 최대 2.4ms).
          운영 중인 현장에서는 끄고 쓰세요. <code>-Xrs</code> 로 뜬 브로커는 자동으로 건너뜁니다.
        </div>
      )}
      <div className="lc-dim gt-note">
        조회만 합니다 — /proc, Redis <code>INFO</code>, Kafka 오프셋·토픽 정보. 판정 기준값(Redis 메인 스레드 94%,
        Kafka 파티션당 300만 건/초, 50·60%)은 <b>2026-10 AWS 2M 환경 실측·판단값</b>이라 현장마다 다를 수 있습니다.
      </div>

      {err && <div className="lc-err">{err}</div>}
      {hostErrs.map((r) => <div key={r.hostId} className="lc-warn">{r.hostName}: {r.err}</div>)}
      {nothing && <div className="lc-dim gt-empty">연결된 서버에서 Redis 도 Kafka 브로커도 찾지 못했습니다.</div>}

      <div className="cc-body">
        {redisRows.length > 0 && (
          <section>
            <h4 className="cc-h">Redis — 메인 스레드 상한</h4>
            <table className="gt-table">
              <thead><tr>
                <th>서버</th><th className="num">포트</th><th>역할</th><th>버전</th><th className="num">io-threads</th>
                <th className="num">명령/초</th><th className="num">SET/초</th><th className="num">메인%</th>
                <th className="num">건당 µs</th><th className="num">추정 상한/초</th><th className="num">여유</th><th>판정</th>
              </tr></thead>
              <tbody>
                {redisRows.map(({ h, x }) => {
                  const s = (x.samples ?? [])[(x.samples ?? []).length - 1];
                  return (
                    <tr key={h.hostId + ":" + x.port} className={x.role !== "master" ? "cc-dimrow" : ""}>
                      <td>{h.hostName}</td><td className="num">{x.port}</td>
                      <td title={`리플리카 ${x.replicas}개, 복제 버퍼 ${x.backlog === "1" ? "있음" : "없음"}`}>
                        {x.role || "—"}{x.role === "master" ? ` (리플리카 ${x.replicas})` : ""}
                      </td>
                      <td>{x.version || "—"}</td><td className="num">{x.ioThreads || "—"}</td>
                      {x.err ? <td colSpan={7} className="lc-bad">{x.err}</td> : s ? (
                        <>
                          <td className="num">{man(s.cmd)}</td><td className="num">{man(s.set)}</td>
                          <td className="num">{pc(s.mainPct)}</td><td className="num">{s.us.toFixed(3)}</td>
                          <td className="num">{man(s.cap)}</td><td className="num">{s.freePct.toFixed(0)}%</td>
                          <td><span className={"gt-badge " + vcls(s.verdict)}>{s.verdict}</span>
                            {(x.warnings ?? []).map((w: string, i: number) => <span key={i} className="lc-bad" title={w}> ⚠</span>)}</td>
                        </>
                      ) : <td colSpan={7} className="lc-dim">—</td>}
                    </tr>
                  );
                })}
              </tbody>
            </table>
            <div className="lc-dim cc-foot">
              판정은 마스터 기준(리플리카 행은 참고). 메인% 90 미만 여유 / 90~94 여유 적음 / 94 이상 상한. 추정 상한 = 명령/초 ÷ 메인% × 94%.
            </div>
          </section>
        )}

        {kafkaRows.length > 0 && (
          <section>
            <h4 className="cc-h">Kafka 브로커 — 스레드 사용률</h4>
            <table className="gt-table">
              <thead><tr>
                <th>서버</th><th>구분</th><th className="num">일하는 수</th><th className="num">가장 바쁜 것</th>
                <th className="num">평균</th><th className="num">합계</th><th>판정</th><th>브로커</th>
              </tr></thead>
              <tbody>
                {kafkaRows.map(({ h, k }) => {
                  const th: any[] = k.threads ?? [];
                  const info = `${k.heap || "힙 ?"} · net ${k.netThreads || "?"} · io ${k.ioThreads || "?"} · CPU ${k.brokerCpu.toFixed(0)}%` +
                    (k.hasGc ? ` · GC 정지 ${k.gcPct.toFixed(1)}%` : "");
                  if (k.err) return <tr key={h.hostId}><td>{h.hostName}</td><td colSpan={7} className="lc-bad">{k.err}</td></tr>;
                  if (th.length === 0) return <tr key={h.hostId}><td>{h.hostName}</td><td colSpan={6} className="lc-dim">스레드를 나누지 못했습니다</td><td>{info}</td></tr>;
                  return th.map((t, i) => (
                    <tr key={h.hostId + i}>
                      {i === 0 && <td rowSpan={th.length}>{h.hostName}</td>}
                      <td>{t.label}</td>
                      <td className="num">{t.busy}/{t.total}</td><td className="num">{pc(t.max)}</td>
                      <td className="num">{pc(t.avg)}</td><td className="num">{t.sum.toFixed(0)}%</td>
                      <td><span className={"gt-badge " + vcls(t.verdict)}>{t.verdict}</span></td>
                      {i === 0 && (
                        <td rowSpan={th.length} className="cc-info">
                          {info}
                          <div className={k.mode === "full" ? "lc-bad" : "lc-dim"}>
                            {k.mode === "full" ? `jstack 사용 (${k.jstackMs}ms)` : k.why}
                          </div>
                        </td>
                      )}
                    </tr>
                  ));
                })}
              </tbody>
            </table>
            {kafkaRows.some(({ k }) => (k.warnings ?? []).length) && (
              <ul className="gt-notes">
                {kafkaRows.flatMap(({ h, k }) => (k.warnings ?? []).map((w: string, i: number) => <li key={h.hostId + i}>{h.hostName}: {w}</li>))}
              </ul>
            )}

            {partSrc && (
              <>
                <h4 className="cc-h">Kafka 파티션 — 토픽 {partSrc.k.topic} <span className="lc-dim">(조회 서버 {partSrc.h.hostName}, 토픽 전체 기준)</span></h4>
                <table className="gt-table">
                  <thead><tr>
                    <th>파티션</th><th className="num">초당 적재량</th><th className="num">한계 대비</th>
                    <th className="num">리더</th><th>복제(ISR)</th><th>판정</th>
                  </tr></thead>
                  <tbody>
                    {(partSrc.k.parts as any[]).map((p) => (
                      <tr key={p.part}>
                        <td>{p.part}</td><td className="num">{man(p.rate)} 건</td><td className="num">{p.pct.toFixed(0)}%</td>
                        <td className="num">{p.leader}</td><td>{p.isr}{p.isrOk ? "" : " (빠짐)"}</td>
                        <td><span className={"gt-badge " + vcls(p.verdict)}>{p.verdict}</span></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <div className="lc-dim cc-foot">
                  토픽 합계 초당 {man(partSrc.k.topicTotal)} 건 · 한계 기준값 파티션당 {man(partSrc.k.partLimit)} 건.
                  50% 늘릴 준비 / 60% 파티션 늘림 / 복제가 빠지면(ISR 「빠짐」) 비율과 관계없이 파티션 늘림.
                </div>
              </>
            )}
          </section>
        )}

        {reports.length === 0 && !running && (
          <div className="lc-dim gt-empty">‘▶ 측정’을 누르면 연결된 서버에서 Redis 와 Kafka 브로커를 찾아 동시에 잽니다.</div>
        )}
      </div>
    </div>
  );
}
