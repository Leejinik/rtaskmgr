import { useState } from "react";

interface Props {
  title: string;
  message?: string;
  defaultName?: string;
  confirmLabel?: string;
  showDiscard?: boolean; // exit flow: offer "don't save"
  onConfirm: (name: string) => void;
  onDiscard?: () => void;
  onCancel: () => void;
}

// NamePrompt asks for a log name. With showDiscard it doubles as the on-exit
// "save / don't save / cancel" dialog.
export default function NamePrompt({
  title, message, defaultName = "", confirmLabel = "저장",
  showDiscard, onConfirm, onDiscard, onCancel,
}: Props) {
  const [name, setName] = useState(defaultName);
  return (
    <div className="scrim" onMouseDown={onCancel}>
      <div className="modal" onMouseDown={(e) => e.stopPropagation()}>
        <h2>{title}</h2>
        {message && (
          <div style={{ color: "var(--text-dim)", fontSize: 12, marginBottom: 6 }}>{message}</div>
        )}
        <label>로그 이름</label>
        <input
          type="text"
          value={name}
          autoFocus
          placeholder="예: collector-장애-0629"
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && onConfirm(name)}
        />
        <div className="actions">
          <button className="toolbtn" onClick={onCancel}>취소</button>
          {showDiscard && (
            <button className="toolbtn" onClick={onDiscard}>저장 안 함</button>
          )}
          <button className="toolbtn primary" onClick={() => onConfirm(name)}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
