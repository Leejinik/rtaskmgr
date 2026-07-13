import { useMemo } from "react";
import { Frame, Proc, SortKey, SortSpec } from "../types";
import { pct, mib, bytesRate, netRate, diskBps, heat } from "../format";

interface Props {
  frame: Frame;
  search: string;
  hideKthreads: boolean;
  topLevelOnly: boolean;
  sort: SortSpec[];
  selectedPid: number | null;
  onSort: (k: SortKey) => void;
  onSelect: (pid: number) => void;
  onOpen: (pid: number) => void;
}

function sortValue(p: Proc, k: SortKey): number | string {
  switch (k) {
    case "name": return p.name.toLowerCase();
    case "pid": return p.pid;
    case "user": return p.user.toLowerCase();
    case "service": return p.service.toLowerCase();
    // Quantize numeric metrics to their on-screen resolution so rows that look
    // identical tie — letting the next sort column decide. Otherwise sub-display
    // precision (e.g. CPU 0.02% all shown as "0.0%") drives an invisible order
    // and the secondary/tertiary sort never visibly engages.
    case "cpu": return Math.round(p.cpu * 10) / 10; // 0.1 %
    case "memPct": return Math.round((p.rssKiB / 1024) * 10) / 10; // 0.1 MB (MB is shown)
    case "disk": {
      const d = diskBps(p.diskR, p.diskW);
      return d < 0 ? -1 : Math.round(d / 1024); // KB step
    }
    case "net": return p.net < 0 ? -1 : Math.round(p.net / 1024); // KB step
  }
}

export default function ProcTable({
  frame, search, hideKthreads, topLevelOnly, sort, selectedPid, onSort, onSelect, onOpen,
}: Props) {
  const rows = useMemo(() => {
    const q = search.trim().toLowerCase();
    let r = frame.procs;
    if (hideKthreads) {
      // Kernel threads have an empty cmdline, so the sampler renders them as
      // "[comm]" (e.g. [kworker/0:1], [kthreadd]). Drop those.
      r = r.filter((p) => !p.name.startsWith("["));
    }
    if (topLevelOnly) {
      // Only processes directly under pid 0/1 (init or the kernel root) — the
      // engineer's "root-child" filter that hides everyone else's children.
      r = r.filter((p) => p.ppid <= 1);
    }
    if (q) {
      r = r.filter(
        (p) =>
          p.name.toLowerCase().includes(q) ||
          p.service.toLowerCase().includes(q) ||
          p.user.toLowerCase().includes(q) ||
          String(p.pid).includes(q)
      );
    }
    const specs = sort.length ? sort : [{ key: "cpu" as SortKey, dir: -1 as const }];
    const cpuExplicit = specs.some((s) => s.key === "cpu");
    const sorted = [...r].sort((a, b) => {
      for (const s of specs) {
        const av = sortValue(a, s.key);
        const bv = sortValue(b, s.key);
        if (av < bv) return -1 * s.dir;
        if (av > bv) return 1 * s.dir;
      }
      // CPU descending is the implicit final tiebreaker (the always-on default),
      // then PID for stability.
      if (!cpuExplicit && a.cpu !== b.cpu) return b.cpu - a.cpu;
      return a.pid - b.pid;
    });
    return sorted;
  }, [frame, search, hideKthreads, topLevelOnly, sort]);

  const maxCpu = Math.max(1, ...rows.map((p) => p.cpu));
  const maxMem = Math.max(1, ...rows.map((p) => p.memPct));
  const maxDisk = Math.max(1, ...rows.map((p) => Math.max(0, diskBps(p.diskR, p.diskW))));
  const totalDisk = rows.reduce((s, p) => s + Math.max(0, diskBps(p.diskR, p.diskW)), 0);
  const netRows = rows.filter((p) => p.net >= 0);
  const maxNet = Math.max(1, ...netRows.map((p) => p.net));
  const totalNet = netRows.reduce((s, p) => s + p.net, 0);

  const cpuInSort = sort.some((s) => s.key === "cpu");
  const arrow = (k: SortKey) => {
    const i = sort.findIndex((s) => s.key === k);
    if (i < 0) {
      // CPU descending is the always-on base sort; show a faint marker for it.
      if (k === "cpu" && !cpuInSort) {
        return <span className="arrow base" title="기본 정렬 (CPU 내림차순)">▼</span>;
      }
      return null;
    }
    return (
      <span className="arrow">
        {sort[i].dir === 1 ? "▲" : "▼"}
        <sub className="ord">{i + 1}</sub>
      </span>
    );
  };

  return (
    <div className="tablewrap">
      <table className="proc">
        <colgroup>
          <col style={{ width: "27%" }} />
          <col style={{ width: "8%" }} />
          <col style={{ width: "10%" }} />
          <col style={{ width: "18%" }} />
          <col style={{ width: "9.25%" }} />
          <col style={{ width: "9.25%" }} />
          <col style={{ width: "9.25%" }} />
          <col style={{ width: "9.25%" }} />
        </colgroup>
        <thead>
          <tr title="클릭마다 오름차순 → 내림차순 → 정렬 해제. 클릭한 순서대로 우선순위(①②③), 최대 3개. 모두 해제하면 CPU 내림차순.">
            <th className="left" onClick={() => onSort("name")}>
              <span className="lbl">이름</span>{arrow("name")}
            </th>
            <th onClick={() => onSort("pid")}>
              <span className="lbl">PID</span>{arrow("pid")}
            </th>
            <th onClick={() => onSort("user")}>
              <span className="lbl">사용자</span>{arrow("user")}
            </th>
            <th className="left" onClick={() => onSort("service")}>
              <span className="lbl">서비스</span>{arrow("service")}
            </th>
            <th onClick={() => onSort("cpu")}>
              <span className="agg">{frame.cpu.toFixed(0)}%</span>
              <span className="lbl">CPU{arrow("cpu")}</span>
            </th>
            <th onClick={() => onSort("memPct")}>
              <span className="agg">{frame.mem.toFixed(0)}%</span>
              <span className="lbl">메모리{arrow("memPct")}</span>
            </th>
            <th onClick={() => onSort("disk")}>
              <span className="agg">{bytesRate(totalDisk)}</span>
              <span className="lbl">디스크{arrow("disk")}</span>
            </th>
            <th onClick={() => onSort("net")}>
              <span className="agg">{netRows.length ? netRate(totalNet) : "—"}</span>
              <span className="lbl">네트워크{arrow("net")}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((p) => {
            const disk = diskBps(p.diskR, p.diskW);
            return (
              <tr
                key={p.pid}
                // data-pid/data-name let App's document-level (capture-phase)
                // contextmenu handler resolve the row reliably — Wails production
                // builds can swallow React's synthetic onContextMenu on rows.
                data-pid={p.pid}
                data-name={p.name}
                data-service={p.service}
                className={selectedPid === p.pid ? "sel" : ""}
                onClick={() => onSelect(p.pid)}
                onDoubleClick={() => onOpen(p.pid)}
                title={`${p.name} (PID ${p.pid})`}
              >
                <td className="left">
                  <div className="name-cell">
                    <span className="ico" />
                    <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>
                      {p.name}
                    </span>
                  </div>
                </td>
                <td>{p.pid}</td>
                <td>{p.user}</td>
                <td className="left svc">{p.service === "-" ? "" : p.service}</td>
                <td className="heatcell" style={{ ["--a" as any]: heat(p.cpu, maxCpu) }}>
                  {pct(p.cpu)}
                </td>
                <td className="heatcell" style={{ ["--a" as any]: heat(p.memPct, maxMem) }}>
                  {mib(p.rssKiB)}
                </td>
                <td className="heatcell" style={{ ["--a" as any]: heat(Math.max(0, disk), maxDisk) }}>
                  {bytesRate(disk)}
                </td>
                <td
                  className={p.net >= 0 ? "heatcell" : ""}
                  style={p.net >= 0 ? ({ ["--a" as any]: heat(p.net, maxNet) }) : undefined}
                >
                  {netRate(p.net)}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
