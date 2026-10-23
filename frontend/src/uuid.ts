// newUUID returns a random (v4) UUID. crypto.randomUUID exists only in a secure
// context: the Wails window is one, but the same frontend opened over plain
// http from another PC (wails dev on a test PC) is not, and cluster saving
// failed there with "crypto.randomUUID is not a function". getRandomValues
// works in both.
export function newUUID(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40; // version 4
  b[8] = (b[8] & 0x3f) | 0x80; // RFC 4122 variant
  const h = Array.from(b, x => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}
