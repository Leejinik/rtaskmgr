// Mirrors the Go monitor.Frame / monitor.Proc payloads streamed over the
// "frame" Wails event.

export interface Proc {
  pid: number;
  ppid: number;
  name: string;
  user: string;
  service: string;
  state: string;
  cpu: number; // % of whole machine (100 = all cores)
  memPct: number; // RSS / MemTotal %
  rssKiB: number;
  diskR: number; // bytes/s, -1 = permission denied
  diskW: number;
  net: number; // bytes/s, -1 = N/A (no nethogs)
  threads: number;
  start: number; // process start, unix seconds (0 if unknown)
}

export interface NetStat {
  name: string;
  rxBps: number;
  txBps: number;
  speed: number; // Mbit/s, 0 = unknown
}

export interface DiskStat {
  mount: string;
  dev: string;
  fsType: string;
  total: number; // bytes
  free: number;
  used: number;
  rBps: number; // bytes/s
  wBps: number;
  busy: number; // % of interval doing I/O
  kind: string; // "SSD" / "HDD" / ""
}

export interface Frame {
  hostId: string;
  t: number; // unix millis
  ncpu: number;
  memTotal: number; // KiB
  memUsed: number; // KiB
  cpu: number; // overall busy %
  mem: number; // overall mem %
  swapTotal: number; // KiB
  swapUsed: number; // KiB
  netRx: number; // bytes/s
  netTx: number; // bytes/s
  netSpeed: number; // Mbit/s, 0 = unknown
  nets: NetStat[];
  disks: DiskStat[];
  procs: Proc[];
}

// SysSample is one timestamped system-level snapshot (no per-process rows) kept
// in a rolling client-side history for the performance charts.
export interface SysSample {
  t: number;
  cpu: number;
  mem: number;
  memTotal: number;
  memUsed: number;
  swapTotal: number;
  swapUsed: number;
  netRx: number;
  netTx: number;
  netSpeed: number;
  nets: NetStat[];
  disks: DiskStat[];
}

export interface Capabilities {
  uid: number;
  os: string;
  rhel: string;
  nethogs: boolean;
  pidstat: boolean;
  sudo: boolean;
}

export type ConnState =
  | "connecting"
  | "probing"
  | "uploading"
  | "streaming"
  | "stopped"
  | "error";

export interface HostStatus {
  state: ConnState;
  detail: string;
}

export type SortKey =
  | "name"
  | "pid"
  | "user"
  | "service"
  | "cpu"
  | "memPct"
  | "disk"
  | "net";

// One level of a multi-column sort. dir: 1 = ascending, -1 = descending.
export interface SortSpec {
  key: SortKey;
  dir: 1 | -1;
}

// Up to this many explicit sort levels (CPU-desc is always the implicit final
// tiebreaker on top of these). Sorting ~300 rows is negligible regardless; the
// cap is for human readability, not performance.
export const MAX_SORT = 3;
