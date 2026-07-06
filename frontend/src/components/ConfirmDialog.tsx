import { ReactNode, useEffect, useRef } from "react";

interface Props {
  title: string;
  message: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

// A small modal Yes/No confirmation for a destructive action. Keyboard: Y
// confirms, N/Esc cancels (the "Y/N" the operator expects). Enter is deliberately
// NOT bound to confirm: on mount we focus the Cancel button, so a reflexive Enter
// (the universal acknowledge key) lands on Cancel and safely aborts instead of
// firing the kill. Confirming always takes a deliberate Y or a click.
export default function ConfirmDialog({
  title,
  message,
  confirmLabel = "예",
  cancelLabel = "아니오",
  danger,
  onConfirm,
  onCancel,
}: Props) {
  const cancelRef = useRef<HTMLButtonElement | null>(null);
  useEffect(() => {
    cancelRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      const k = e.key.toLowerCase();
      if (k === "escape" || k === "n") {
        e.preventDefault();
        onCancel();
      } else if (k === "y") {
        e.preventDefault();
        onConfirm();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onConfirm, onCancel]);

  return (
    <div className="scrim" onMouseDown={onCancel}>
      <div
        className="modal confirm"
        onMouseDown={(e) => e.stopPropagation()}
        style={{ minWidth: 360, maxWidth: 460 }}
      >
        <h2>{title}</h2>
        <div style={{ margin: "12px 0 22px", lineHeight: 1.6, fontSize: 14 }}>{message}</div>
        <div className="actions">
          <button className="toolbtn" ref={cancelRef} onClick={onCancel}>
            {cancelLabel} (N)
          </button>
          <button className={"toolbtn " + (danger ? "danger" : "primary")} onClick={onConfirm}>
            {confirmLabel} (Y)
          </button>
        </div>
      </div>
    </div>
  );
}
