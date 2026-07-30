import { useEffect } from "react";

// useEsc closes a modal on Escape.
//
// The `enabled` flag matters: ConfirmDialog binds Escape on `window` too, so a
// modal that keeps its own handler active while a confirmation sits on top of it
// would see one Escape dismiss both. Pass `false` whenever a child dialog owns
// the keyboard.
export function useEsc(onEsc: () => void, enabled = true) {
  useEffect(() => {
    if (!enabled) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onEsc();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onEsc, enabled]);
}
