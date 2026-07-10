import { lazy, Suspense } from "react";
import { createBrowserRouter } from "react-router-dom";
import { Dashboard } from "./pages/Dashboard";
import { Signals } from "./pages/Signals";
import { Collectors } from "./pages/Collectors";
import { WorkItem } from "./pages/WorkItem";
import { Report } from "./pages/Report";
import { Automations } from "./pages/Automations";

// Settings pulls in Monaco (a multi-MB editor bundle), so it's the one page
// loaded lazily — every other route stays on the small, always-loaded main
// chunk instead of paying for an editor most page views never touch.
const Settings = lazy(() => import("./pages/Settings").then((m) => ({ default: m.Settings })));

// Multi-route SPA. Each route renders its own <Layout> (shared topbar + nav),
// so navigating swaps only the page body. docentd serves index.html for any
// of these client routes in production; Vite's dev server does the same.
export const router = createBrowserRouter([
  { path: "/", element: <Dashboard /> },
  { path: "/signals", element: <Signals /> },
  { path: "/collectors", element: <Collectors /> },
  { path: "/report", element: <Report /> },
  { path: "/automations", element: <Automations /> },
  {
    path: "/settings",
    element: (
      <Suspense fallback={<div className="wrap empty">Loading…</div>}>
        <Settings />
      </Suspense>
    ),
  },
  { path: "/workitem", element: <WorkItem /> },
]);
