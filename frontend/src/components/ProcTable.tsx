import { useMemo } from "react";
import { Frame, Proc, SortKey } from "../types";
import { pct, mib, bytesRate, netRate, diskBps, heat } from "../format";

interface Props {
  frame: Frame;
  search: string;
  sortKey: SortKey;
  sortDir: 1 | -1;
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
    case "cpu": return p.cpu;
    case "memPct": return p.memPct;
    case "disk": return diskBps(p.diskR, p.diskW);
    case "net": return p.net;
  }
}

export default function ProcTable({
  frame, search, sortKey, sortDir, selectedPid, onSort, onSelect, onOpen,
}: Props) {
  const rows = useMemo(() => {
    const q = search.trim().toLowerCase();
    let r = frame.procs;
    if (q) {
      r = r.filter(
        (p) =>
          p.name.toLowerCase().includes(q) ||
          p.service.toLowerCase().includes(q) ||
          p.user.toLowerCase().includes(q) ||
          String(p.pid).includes(q)
      );
    }
    const sorted = [...r].sort((a, b) => {
      const av = sortValue(a, sortKey);
      const bv = sortValue(b, sortKey);
      if (av < bv) return -1 * sortDir;
      if (av > bv) return 1 * sortDir;
      return a.pid - b.pid;
    });
    return sorted;
  }, [frame, search, sortKey, sortDir]);

  const maxCpu = Math.max(1, ...rows.map((p) => p.cpu));
  const maxMem = Math.max(1, ...rows.map((p) => p.memPct));
  const maxDisk = Math.max(1, ...rows.map((p) => Math.max(0, diskBps(p.diskR, p.diskW))));
  const totalDisk = rows.reduce((s, p) => s + Math.max(0, diskBps(p.diskR, p.diskW)), 0);

  const arrow = (k: SortKey) =>
    sortKey === k ? <span className="arrow">{sortDir === 1 ? "▲" : "▼"}</span> : null;

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
          <tr>
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
              <span className="agg">—</span>
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
                <td>{netRate(p.net)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
