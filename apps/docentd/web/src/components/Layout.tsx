import type { ReactNode } from "react";
import { NavLink } from "react-router-dom";
import { ToastHost } from "./ToastHost";

type Props = {
  /** className for the <main> element (e.g. "board", "wrap", "detail-grid"). */
  mainClass?: string;
  /** Right-of-nav stat chips (page-specific). */
  stats?: ReactNode;
  /** Far-right controls (auto toggle, refresh button, …). */
  controls?: ReactNode;
  children: ReactNode;
};

const navClass = ({ isActive }: { isActive: boolean }) => (isActive ? "active" : "");

export function Layout({ mainClass, stats, controls, children }: Props) {
  return (
    <>
      <header className="topbar">
        <div className="brand">
          <span className="dot" />
          <h1>docent</h1>
        </div>
        <nav className="nav">
          <NavLink to="/" end className={navClass}>
            Dashboard
          </NavLink>
          <NavLink to="/signals" className={navClass}>
            Signals
          </NavLink>
          <NavLink to="/collectors" className={navClass}>
            Collectors
          </NavLink>
        </nav>
        <div className="stats">{stats}</div>
        <div className="controls">{controls}</div>
      </header>
      <main className={mainClass} aria-live="polite">
        {children}
      </main>
      <ToastHost />
    </>
  );
}
