// Messages from the resource-registration backend may carry, after the screen
// text, the input they concern (" ⟦kafka.1.port⟧") and the raw error
// ("\n자세히: …"); see internal/statsreg/connerr.go. The screen shows the
// sentence, marks the input and folds the raw part into 「자세히」.

export interface StatsMessageParts { summary: string; field: string; detail: string }

export function parseStatsMessage(text: string): StatsMessageParts {
  const t = (text || "").replace(/^Error:\s*/, "");
  const field = t.match(/⟦([^⟧]*)⟧/)?.[1] || "";
  const i = t.indexOf("\n자세히: ");
  const head = i >= 0 ? t.slice(0, i) : t;
  const detail = i >= 0 ? t.slice(i + "\n자세히: ".length) : "";
  return { summary: head.replace(/\s*⟦[^⟧]*⟧/g, "").trim(), field, detail };
}

// badInput: is the input with this code (db.host, kafka.1.port …) the one the error names?
export const badInput = (bad: string, code: string) => !!bad && (bad === code || code.startsWith(bad + "."));

export default function StatsMessage({ text }: { text: string }) {
  const m = parseStatsMessage(text);
  return <>{m.summary}{m.detail && <details className="stats-more"><summary>자세히</summary><code>{m.detail}</code></details>}</>;
}
