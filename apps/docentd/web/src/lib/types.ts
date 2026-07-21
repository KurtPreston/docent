// TypeScript mirrors of the docentd JSON payloads. Field names/shape follow the
// Go structs in apps/docentd/internal/engine/engine.go (json tags).

export type DashboardSession = {
  kind: string;
  name: string;
  ide?: string;
  host?: string;
  targetHost?: string;
  path?: string;
  ticket?: string;
  color?: string;
  fg?: string;
  live: boolean;
  status: string;
  needsFollowup: boolean;
  lastActivity?: string;
};

export type DashboardPR = {
  prNumber: number;
  title: string;
  url?: string;
  repo?: string;
  state?: string;
  draft: boolean;
  ticket?: string;
};

export type DashboardTicket = {
  key: string;
  title?: string;
  url?: string;
  status?: string;
};

export type DashboardGroup = {
  key: string;
  ticket?: string;
  summary?: string;
  repo?: string;
  branch?: string;
  openPath?: string;
  deepLink?: string;
  lastActivity?: string;
  jiraStatus?: string;
  jiraUrl?: string;
  color?: string;
  fg?: string;
  needsFollowup: boolean;
  status?: string;
  statusRank: number;
  actionRequired: boolean;
  sessions: DashboardSession[];
  prs: DashboardPR[];
  tickets?: DashboardTicket[];
};

export type Dashboard = {
  generatedAt: string;
  backend: string;
  sessionCount: number;
  groupCount: number;
  provider?: string;
  sshHost?: string;
  groups: DashboardGroup[];
};

export type SignalView = {
  kind: string;
  title: string;
  summary?: string;
  url?: string;
  observedAt?: string;
  entityId?: string;
  workItemKey?: string;
  fields?: Record<string, string>;
};

export type SignalUnit = {
  directiveId: string;
  collector: string;
  mode: string;
  lastRun?: string;
  lastErr?: string;
  count: number;
  signals: SignalView[];
};

export type SignalsView = {
  generatedAt: string;
  units: SignalUnit[];
};

export type UnitView = {
  directiveId: string;
  collector: string;
  mode: string;
  interval?: string;
  onRequest: boolean;
  onLoad: boolean;
  lastRun?: string;
  nextDue?: string;
  itemCount: number;
  lastErr?: string;
};

export type CollectorsView = {
  generatedAt: string;
  units: UnitView[];
};

export type EntityView = {
  id: string;
  kind: string;
  title: string;
  url?: string;
};

// Report page: mirrors the docentd /api/report* payloads.

export type ReportMode = {
  id: string;
  name: string;
  promptRequired: boolean;
  lookbackKind: "days" | "previous-weekday" | string;
  lookbackDays?: number;
  scope: string;
  prompt?: string;
  collect: string;
};

export type ReportMeta = {
  modes: ReportMode[];
  scopes: string[];
  collects: string[];
  provider: {
    label: string;
    provider: string;
    model: string;
  };
};

export type ReportRunMeta = {
  mode: string;
  modeName: string;
  scope: string;
  lookbackDays: number;
  statuses: number;
};

export type ReportStatus = "pending" | "running" | "done" | "error";

export type ReportJob = {
  id: string;
  status: ReportStatus;
  markdown?: string;
  meta?: ReportRunMeta;
  error?: string;
  phase?: string;
  partial?: string;
};

export type ReportCollectorProgress = {
  id: string;
  description?: string;
  status: string;
  detail?: string;
  completed?: number;
  total?: number;
};

export type ReportEvent = {
  type: "phase" | "collector" | "token" | "thinking" | "done" | "error";
  phase?: string;
  collector?: ReportCollectorProgress;
  text?: string;
  markdown?: string;
  meta?: ReportRunMeta;
  error?: string;
};

export type ReportRequest = {
  mode: string;
  days?: number;
  scope?: string;
  prompt?: string;
  collect?: string;
};

// Settings page: mirrors the docentd /api/config payloads.

export type ConfigFileID = "config" | "docentd" | "goals";

export type ConfigFileView = {
  id: ConfigFileID;
  label: string;
  path: string;
  content: string;
  exists: boolean;
};

export type ConfigSaveResult = {
  ok: boolean;
  problems?: string[];
  error?: string;
};

export type AutomationTrigger = {
  type?: string;
  source?: string;
  kind?: string | string[];
  at?: string;
  cron?: string;
  weekday?: string;
};

export type AutomationAction = {
  type: string;
  [key: string]: unknown;
};

export type AutomationRule = {
  id: string;
  name?: string;
  enabled: boolean;
  trigger: AutomationTrigger;
  actions: AutomationAction[];
};

export type AutomationJob = {
  id: string;
  ruleId: string;
  dedupeKey?: string;
  status: string;
  message?: string;
  error?: string;
  createdAt: string;
  updatedAt: string;
};

export type AutomationsView = {
  ok: boolean;
  rules: AutomationRule[];
  jobs: AutomationJob[];
};

export type WorkItemDetail = {
  key: string;
  title?: string;
  ticket?: string;
  summary?: string;
  repo?: string;
  branch?: string;
  openPath?: string;
  deepLink?: string;
  provider?: string;
  lastActivity?: string;
  jiraUrl?: string;
  jiraStatus?: string;
  color?: string;
  fg?: string;
  sessions: DashboardSession[];
  prs: DashboardPR[];
  tickets?: DashboardTicket[];
  entities: EntityView[];
  signals: SignalView[];
};
