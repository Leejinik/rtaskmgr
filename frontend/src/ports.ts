// Client-side mirror of the backend's port parser, for the live BPF preview and
// the inline error in the capture modal.
//
// This is a PREVIEW ONLY. The backend re-parses the same text and is the sole
// authority on what tcpdump receives — the rules are duplicated here so the
// operator sees what their input becomes before pressing start, not so the
// frontend can decide anything.
//
// The separators are "," and whitespace and nothing else: because ";", "|", "$"
// and "`" are not separators, an injection attempt stays inside one token and is
// rejected by the token pattern instead of quietly splitting into valid ports.
// A reversed range is refused rather than swapped — silently "fixing" 1237-1234
// would hide a typo in a filter the operator is about to trust.

export const MAX_PORT_RANGES = 64;

export interface PortSpec {
  all: boolean;
  ranges: [number, number][];
}

export interface PortParse {
  spec?: PortSpec;
  err?: string;
}

const TOKEN = /^(\d{1,5})(?:-(\d{1,5}))?$/;

export function parsePorts(input: string): PortParse {
  if (input.length > 1024) return { err: "포트 입력이 너무 깁니다 (최대 1024자)" };
  if (input.trim() === "") return { spec: { all: true, ranges: [] } };

  const toks = input.split(/[,\s]+/u).filter((t) => t !== "");
  if (toks.length === 0) return { spec: { all: true, ranges: [] } };
  if (toks.length > 256) return { err: "포트 항목이 너무 많습니다 (최대 256개)" };

  const ranges: [number, number][] = [];
  for (const t of toks) {
    const m = TOKEN.exec(t);
    if (!m) return { err: `포트 형식이 아닙니다: "${t}" (예: 3306 또는 8080-8090)` };
    const lo = Number(m[1]);
    const hi = m[2] !== undefined ? Number(m[2]) : lo;
    if (lo < 1 || lo > 65535 || hi < 1 || hi > 65535) {
      return { err: `포트는 1~65535 범위여야 합니다: "${t}"` };
    }
    if (hi < lo) return { err: `포트 범위의 시작이 끝보다 큽니다: "${t}" (자동으로 바꾸지 않습니다)` };
    ranges.push([lo, hi]);
  }
  ranges.sort((a, b) => (a[0] !== b[0] ? a[0] - b[0] : a[1] - b[1]));

  const merged: [number, number][] = [];
  for (const r of ranges) {
    const last = merged[merged.length - 1];
    if (last && r[0] <= last[1] + 1) {
      if (r[1] > last[1]) last[1] = r[1];
      continue;
    }
    merged.push([r[0], r[1]]);
  }
  if (merged.length > MAX_PORT_RANGES) {
    return { err: `포트 구간이 너무 많습니다 (병합 후 ${merged.length}개, 최대 ${MAX_PORT_RANGES}개)` };
  }
  return { spec: { all: false, ranges: merged } };
}

// bpfOf rebuilds the filter from integers and fixed keywords only — the same way
// the backend does, so the preview cannot show something different from what runs.
export function bpfOf(spec: PortSpec): string {
  if (spec.all || spec.ranges.length === 0) return "";
  return spec.ranges
    .map(([lo, hi]) => (lo === hi ? `port ${lo}` : `portrange ${lo}-${hi}`))
    .join(" or ");
}

export function canonicalOf(spec: PortSpec): string {
  if (spec.all || spec.ranges.length === 0) return "";
  return spec.ranges.map(([lo, hi]) => (lo === hi ? `${lo}` : `${lo}-${hi}`)).join(", ");
}

// ---- host / network half of the filter -----------------------------------

export const MAX_HOST_TERMS = 32;

export interface HostSpec {
  all: boolean;
  terms: { net: boolean; value: string }[];
}

export interface HostParse {
  spec?: HostSpec;
  err?: string;
}

// The pcap-filter keywords an operator is most likely to paste in out of habit
// ("host 10.0.0.5"). They are legal short hostnames, so without this they would be
// accepted here and only fail later as an unresolvable name.
const BPF_KEYWORDS = new Set([
  "host", "net", "port", "portrange", "src", "dst", "and", "or", "not", "ip",
  "ip6", "tcp", "udp", "icmp", "ether", "gateway", "less", "greater", "vlan",
  "mask", "proto",
]);

const IPV4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;
const LABEL = /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;
const DIGITS_DOTS = /^[0-9.]+$/;

function isIPv4(s: string): boolean {
  const m = IPV4.exec(s);
  if (!m) return false;
  return m.slice(1).every((o) => o.length <= 3 && Number(o) <= 255);
}

// A deliberately loose IPv6 check: the Go side is authoritative (net.ParseIP), and
// this only decides what the preview says.
function isIPv6(s: string): boolean {
  return s.includes(":") && /^[0-9A-Fa-f:.]+$/.test(s) && !s.includes(":::");
}

// parseHosts mirrors the Go parseHostSpec for the live preview only.
export function parseHosts(input: string): HostParse {
  if (input.length > 1024) return { err: "호스트 입력이 너무 깁니다 (최대 1024자)" };
  if (input.trim() === "") return { spec: { all: true, terms: [] } };

  const toks = input.split(/[,\s]+/u).filter((t) => t !== "");
  if (toks.length === 0) return { spec: { all: true, terms: [] } };
  if (toks.length > MAX_HOST_TERMS) {
    return { err: `호스트 항목이 너무 많습니다 (최대 ${MAX_HOST_TERMS}개)` };
  }

  const terms: { net: boolean; value: string }[] = [];
  const seen = new Set<string>();
  for (const t of toks) {
    if (BPF_KEYWORDS.has(t.toLowerCase())) {
      return { err: `"${t}" 는 tcpdump 필터 키워드입니다 — 여기에는 주소만 적으세요 (예: "host 10.0.0.5" 대신 "10.0.0.5")` };
    }
    let term: { net: boolean; value: string };
    if (t.includes("/")) {
      const [addr, len] = t.split("/");
      const bits = Number(len);
      const v4 = isIPv4(addr);
      const v6 = isIPv6(addr);
      if (
        t.split("/").length !== 2 || len === "" || !Number.isInteger(bits) || bits < 0 ||
        (!v4 && !v6) || (v4 && bits > 32) || (v6 && bits > 128)
      ) {
        return { err: `네트워크 표기가 아닙니다: "${t}" (예: 10.0.0.0/24)` };
      }
      // The canonical network address is computed on the Go side; show what was
      // typed and let the server's canonical form come back with the capture.
      term = { net: true, value: t };
    } else if (isIPv4(t) || isIPv6(t)) {
      term = { net: false, value: t };
    } else if (DIGITS_DOTS.test(t)) {
      return { err: `IP 주소 형식이 아닙니다: "${t}"` };
    } else {
      if (t.length > 253) return { err: `호스트 이름이 너무 깁니다: "${t}"` };
      const labels = t.replace(/\.$/, "").split(".");
      if (!labels.every((l) => LABEL.test(l))) {
        return { err: `호스트 형식이 아닙니다: "${t}" (예: 10.0.0.5, 10.0.0.0/24, db1.example.com)` };
      }
      term = { net: false, value: t.toLowerCase() };
    }
    const key = `${term.net}|${term.value}`;
    if (seen.has(key)) continue;
    seen.add(key);
    terms.push(term);
  }
  return { spec: { all: false, terms } };
}

export function bpfOfHosts(spec: HostSpec): string {
  if (spec.all || spec.terms.length === 0) return "";
  return spec.terms.map((t) => `${t.net ? "net" : "host"} ${t.value}`).join(" or ");
}

// combinedBpf joins the two halves the way an operator writes them by hand.
// The parentheses are load-bearing: `and` binds tighter than `or` in pcap syntax,
// so "host A or host B and port 3306" would mean "host A or (host B and port 3306)".
export function combinedBpf(hosts: string, ports: string): string {
  const group = (s: string) => (s.includes(" or ") ? `( ${s} )` : s);
  if (!hosts && !ports) return "";
  if (!ports) return hosts;
  if (!hosts) return ports;
  return `${group(hosts)} and ${group(ports)}`;
}
