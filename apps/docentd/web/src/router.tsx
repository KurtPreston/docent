import { createBrowserRouter } from "react-router-dom";
import { Dashboard } from "./pages/Dashboard";
import { Signals } from "./pages/Signals";
import { Collectors } from "./pages/Collectors";
import { WorkItem } from "./pages/WorkItem";
import { Report } from "./pages/Report";
import { Settings } from "./pages/Settings";

// Multi-route SPA. Each route renders its own <Layout> (shared topbar + nav),
// so navigating swaps only the page body. docentd serves index.html for any
// of these client routes in production; Vite's dev server does the same.
export const router = createBrowserRouter([
  { path: "/", element: <Dashboard /> },
  { path: "/signals", element: <Signals /> },
  { path: "/collectors", element: <Collectors /> },
  { path: "/report", element: <Report /> },
  { path: "/settings", element: <Settings /> },
  { path: "/workitem", element: <WorkItem /> },
]);
