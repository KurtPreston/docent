// TypeScript mirrors of the docentd JSON payloads. Field names/shape follow the
// Go structs in apps/docentd/internal/engine/engine.go (json tags).

export type DashboardSession = {
  kind: string;
  name: string;
  ide?: string;
  host?: string;
  targetHost?: string;
  path?: string;
  /** Opens/reveals this window specifically, rather than the work item's checkout. */
  deepLink?: string;
  ticket?: string;
  color?: string;
  fg?: string;
  live: boolean;
  status: string;
  needsFollowup: boolean;
  lastActivity?: string;
};

export type DashboardThread = {
  id?: string;
  author?: string;
  body?: string;
  url?: string;
  file?: string;
  line?: number;
  /** True when the last comment in the thread is the user's own. */
  mine: boolean;
  updatedAt?: string;
};

export type DashboardPR = {
  prNumber: number;
  title: string;
  url?: string;
  repo?: string;
  state?: string;
  draft: boolean;
  ticket?: string;
  mine?: boolean;
  checks?: string;
  reviewDecision?: string;
  unresolved?: number;
  threads?: DashboardThread[];
  /**
   * The six-bucket classification, absent when docent could not read the PR's
   * timeline. Values: ready_to_merge, failing_validation, awaiting_author,
   * awaiting_review, pending_validation, draft.
   */
  bucket?: string;
  /** "author" or "reviewer": whose court the ball is in. */
  lastAction?: string;
  lastActionAt?: string;
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

/** Attention buckets from GET /api/cockpit, most urgent first. */
export type Attention =
  | "agent-waiting"
  | "pr-my-turn"
  | "ready-to-merge"
  | "review-requested"
  | "agent-working"
  | "in-progress"
  | "todo";

export type CockpitLane = {
  key: string;
  title?: string;
  ticket?: string;
  repo?: string;
  branch?: string;
  openPath?: string;
  deepLink?: string;
  jiraUrl?: string;
  jiraStatus?: string;
  color?: string;
  fg?: string;
  attention: Attention;
  attentionRank: number;
  /** Every concrete thing wanting attention; never empty. */
  reasons: string[];
  lastActivity?: string;
  sessions: DashboardSession[];
  prs: DashboardPR[];
};

export type InboxKind =
  | "agent-waiting"
  | "review-comment"
  | "checks-failing"
  | "changes-requested"
  | "ready-to-merge"
  | "review-requested";

export type InboxItem = {
  kind: InboxKind;
  laneKey: string;
  title: string;
  body?: string;
  author?: string;
  url?: string;
  repo?: string;
  prNumber?: number;
  file?: string;
  line?: number;
  ticket?: string;
  branch?: string;
  openPath?: string;
  color?: string;
  at?: string;
};

export type CockpitCounts = {
  agentWaiting: number;
  myTurnPR: number;
  readyToMerge: number;
  reviewRequested: number;
  agentWorking: number;
  inProgress: number;
  todo: number;
  actionable: number;
};

export type CockpitSource = {
  id: string;
  lastRun?: string;
  items: number;
  error?: string;
  /** False until the collector's first successful report. */
  loaded: boolean;
};

export type Cockpit = {
  generatedAt: string;
  provider?: string;
  sshHost?: string;
  /** How many work items exist in total, so the UI can say what it is hiding. */
  total: number;
  counts: CockpitCounts;
  lanes: CockpitLane[];
  queue: CockpitLane[];
  inbox: InboxItem[];
  sources: CockpitSource[];
};

// Agent sessions: mirrors the /api/agents payloads (agentSessionView in
// apps/docentd/internal/server/agents.go) and the normalized event vocabulary
// from libs/agentsession.

export type AgentStatus = "running" | "idle" | "failed" | "stopped";

export type AgentTurnResult = {
  text?: string;
  isError?: boolean;
  sessionId?: string;
  durationMs?: number;
  costUsd?: number;
  inputTokens?: number;
  outputTokens?: number;
  numTurns?: number;
};

export type AgentSession = {
  id: string;
  provider: string;
  model?: string;
  title?: string;
  repo?: string;
  branch?: string;
  dir?: string;
  project?: string;
  color?: string;
  status: AgentStatus;
  error?: string;
  turns: number;
  lastResult?: AgentTurnResult;
  createdAt: string;
  updatedAt: string;
};

export type AgentEventKind =
  | "prompt"
  | "started"
  | "text"
  | "thinking"
  | "tool"
  | "tool-result"
  | "done"
  | "error"
  | "stopped"
  | "status";

export type AgentEvent = {
  kind: AgentEventKind;
  text?: string;
  tool?: string;
  sessionId?: string;
  error?: string;
  result?: AgentTurnResult;
  at: string;
};

/** A repository from GET /api/projects: somewhere an agent can be started. */
export type RepoProject = {
  repo: string;
  dir: string;
  name: string;
};

export type AgentStartRequest = {
  provider?: string;
  title?: string;
  repo?: string;
  branch?: string;
  dir?: string;
  baseRef?: string;
  openPath?: string;
  prompt: string;
  /** Proceed even though another agent appears to be working in the worktree. */
  force?: boolean;
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

// RegistrySession mirrors the sessionView struct returned by GET /api/sessions
// (see apps/docentd/internal/server/server.go sessionsList): the raw session
// registry, keyed by composite key, rather than the work-item grouped view.
export type RegistrySession = {
  key: string;
  ide?: string;
  ideHost?: string;
  targetHost?: string;
  path?: string;
  name?: string;
  live: boolean;
  status: string;
  lastActivity?: string;
  // provider is the open_trigger provider ("cursor" | "wsm" | ""); workItemKey
  // is set when the session is correlated to a work item; deepLink opens the
  // session's own workspace (cursor provider only). See server.go sessionsList.
  provider?: string;
  workItemKey?: string;
  deepLink?: string;
};

export type SessionsView = {
  sessions: RegistrySession[];
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
