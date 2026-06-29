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
}

export interface Frame {
  hostId: string;
  t: number; // unix millis
  ncpu: number;
  memTotal: number; // KiB
  memUsed: number; // KiB
  cpu: number; // overall busy %
  mem: number; // overall mem %
  procs: Proc[];
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
