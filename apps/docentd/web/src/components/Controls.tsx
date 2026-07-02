import { useRef, useState } from "react";

// The ↻ button shared by every page. Briefly toggles a `spin` class (matching
// the old dashboard) and invokes onClick.
export function RefreshButton({ onClick }: { onClick: () => void }) {
  const [spin, setSpin] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  return (
    <button
      id="refresh"
      title="Refresh now"
      className={spin ? "spin" : ""}
      onClick={() => {
        setSpin(true);
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(() => setSpin(false), 400);
        onClick();
      }}
    >
      ↻
    </button>
  );
}

// The dashboard's auto-refresh toggle.
export function AutoToggle({ checked, onChange }: { checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="toggle">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} /> auto
    </label>
  );
}
