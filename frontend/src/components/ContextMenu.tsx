import { useEffect } from "react";

export interface MenuItem {
  label: string;
  danger?: boolean;
  onClick: () => void;
}

interface Props {
  x: number;
  y: number;
  items: MenuItem[];
  onClose: () => void;
}

export default function ContextMenu({ x, y, items, onClose }: Props) {
  useEffect(() => {
    const close = () => onClose();
    window.addEventListener("click", close);
    window.addEventListener("contextmenu", close);
    return () => {
      window.removeEventListener("click", close);
      window.removeEventListener("contextmenu", close);
    };
  }, [onClose]);

  return (
    <div className="ctxmenu" style={{ left: x, top: y }} onMouseDown={(e) => e.stopPropagation()}>
      {items.map((it, i) => (
        <div
          key={i}
          className={"ctxitem" + (it.danger ? " danger" : "")}
          onClick={() => { it.onClick(); onClose(); }}
        >
          {it.label}
        </div>
      ))}
    </div>
  );
}
