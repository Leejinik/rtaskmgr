// Helpers for managed-account (liz/root) password expiry.
//
// Expiry is stored/transmitted as a Unix DAY number (days since 1970-01-01),
// matching /etc/shadow's semantics. Sentinels: -1 = never expires, 0 (or any
// non-positive) = unknown / not projectable.

export interface PwInfo {
  hasLiz: boolean;
  hasRoot: boolean;
  lizExpDays: number;
  rootExpDays: number;
  todayDays?: number;
  err?: string;
}

export const NEVER = -1;

// today as a Unix day number (local clock, day granularity).
export const todayUnixDay = (): number => Math.floor(Date.now() / 86400000);

// daysLeft returns days until expiry, Infinity for "never", or null for unknown.
export function daysLeft(expDays: number): number | null {
  if (expDays === NEVER) return Infinity;
  if (!expDays || expDays <= 0) return null;
  return expDays - todayUnixDay();
}

// expDate formats the expiry day as YYYY-MM-DD, or "" if not a real date.
export function expDate(expDays: number): string {
  if (expDays === NEVER || !expDays || expDays <= 0) return "";
  const d = new Date(expDays * 86400000);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

// expLabel is a human one-liner for one account's expiry.
export function expLabel(expDays: number): string {
  if (expDays === NEVER) return "만료 없음";
  const dl = daysLeft(expDays);
  if (dl === null) return "알 수 없음";
  const ds = expDate(expDays);
  if (dl < 0) return `${ds} (만료됨)`;
  if (dl === 0) return `${ds} (오늘 만료)`;
  return `${ds} (${dl}일 남음)`;
}

// warnLevel classifies an account's urgency given the warn threshold.
// "expired" | "warn" (<= warnDays) | "ok" | "unknown" | "never".
export function warnLevel(expDays: number, warnDays: number): string {
  if (expDays === NEVER) return "never";
  const dl = daysLeft(expDays);
  if (dl === null) return "unknown";
  if (dl < 0) return "expired";
  if (dl <= warnDays) return "warn";
  return "ok";
}

// isUrgent is true when this info should raise the connect-time alert.
export function isUrgent(info: PwInfo | undefined, warnDays: number): boolean {
  if (!info) return false;
  const check = (has: boolean, exp: number) => {
    if (!has) return false;
    const lvl = warnLevel(exp, warnDays);
    return lvl === "warn" || lvl === "expired";
  };
  return check(info.hasLiz, info.lizExpDays) || check(info.hasRoot, info.rootExpDays);
}

// tooltip is the multi-line hover text for a host's two accounts.
export function pwTooltip(info: PwInfo | undefined): string {
  if (!info) return "패스워드 만료: 연결 후 확인";
  if (info.err) return `패스워드 만료: 조회 실패 (${info.err})`;
  const liz = info.hasLiz ? expLabel(info.lizExpDays) : "없음";
  const root = info.hasRoot ? expLabel(info.rootExpDays) : "없음";
  return `패스워드 만료\n  liz: ${liz}\n  root: ${root}`;
}
