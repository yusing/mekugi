import type {
  Start as APIStart,
  ReplayStart,
  PageCursor,
  ContentReference,
  PageResult as APIPageResult,
  DeferredSections,
} from "../../../../sessionapi/protocol.ts";
export type {
  PageCursor,
  ContentReference,
  ContentResult,
  DeferredSections,
} from "../../../../sessionapi/protocol.ts";
export type PageResult = APIPageResult & { acknowledge?: () => void };
export type {
  Session,
  Input,
  Answer,
  Playback,
  StartOptions,
  Model as StartModel,
} from "../../../../sessionapi/protocol.ts";
import type {
  Session,
  Input,
  Playback,
  Chunk,
  Counts,
  Origin,
  Presentation_Counts,
  Preview,
} from "../../../../sessionapi/protocol.ts";
export type {
  Chunk,
  Counts,
  Origin,
  Preview,
  ReviewFile,
} from "../../../../sessionapi/protocol.ts";

// Wire shapes served by apps/mekugi/internal/sessionapi/projection.go. Times are Unix
// milliseconds; durations and playback offsets are milliseconds.

// A live session's input. reason says why Main cannot take input now;
// waiting counts the questions and approvals Main waits on.
export interface Prompts {
  questions: QuestionCall[];
  approvals: Approval[];
}

export interface QuestionCall {
  id: string;
  agent?: string;
  questions: Question[];
}

export interface Question {
  id: string;
  header?: string;
  text?: string;
  options?: { label: string; description?: string }[];
  secret?: boolean;
}

export interface Approval {
  id: string;
  agent?: string;
  title: string;
  subject?: string;
  details: string[];
  choices: { id: number; label: string; deny?: boolean }[];
}

export interface Decide {
  approval: string;
  choice: number;
  reason?: string;
}

// One entry of the session list. waiting counts the questions and approvals
// Main waits on. Identities belong to the selected execution machine.
export interface Listed {
  id: string;
  starting?: boolean;
  replay?: boolean;
  thread?: string;
  title?: string;
  workspace?: string;
  started?: number;
  status: Status;
  waiting?: number;
}

export interface Status {
  text?: string;
  failure?: string;
  alert?: boolean;
  running?: boolean;
  completed?: number;
}

export interface Read {
  path: string;
  ranges?: string[];
}

export interface Change {
  verb?: string;
  code?: string;
  label?: string;
  added?: number;
  removed?: number;
  note?: string;
  footer?: boolean;
}

export interface Value {
  detailsAvailable?: boolean;
  detailCode?: string;
  tailClipped?: boolean;
  sourceOmitted?: boolean;
  kind?: string;
  verb?: string;
  // A compound command's combined result; without code, its steps are listed before it.
  batchExit?: boolean;
  label?: string;
  // Programs that made a file edit, outside its label.
  sources?: string[];
  code?: string;
  lang?: string;
  fenced?: boolean;
  body?: string;
  // A child reply's own summary of body, when the feature is on.
  summary?: string;
  details?: string;
  reads?: Read[];
  path?: string;
  autoReload?: boolean;
  approval?: string;
  subsumed?: boolean;
  effect?: boolean;
  pending?: boolean;
  skipped?: boolean;
  // The retained run starting at this value folds behind one disclosure.
  hiddenEdits?: number;
  codeLines?: number;
  tailLines?: number;
  foldSource?: boolean;
  duration?: number;
  thought?: number;
  workdir?: string;
  from?: string;
  to?: string;
  questions?: { text?: string; answer?: string }[];
  waits?: { name: string; status?: string }[];
  waitTimeout?: string;
  waitReason?: string;
  exitCode?: number;
  editOutcome?: string;
  running?: boolean;
  live?: boolean;
  tail?: string[];
  tailOmitted?: number;
  changes?: Change[];
  hook?: { status?: string; hasError?: boolean };
}

export interface Entry {
  outcome?: string;
  position?: number;
  turn?: number;
  revision?: number;
  content?: ContentReference | null;
  autoContent?: boolean;
  spans?: { Start: number; End: number; Kind: number }[];
  images?: string[];
  seq: number;
  agent?: string;
  kind?: string;
  text?: string;
  observed?: number;
  running?: boolean;
  assignment?: boolean;
  assignmentTo?: string;
  assignmentText?: string;
  values?: Value[];
}

export type AgentState = "working" | "waiting" | "error" | "done" | "idle";

export interface Agent {
  content?: ContentReference | null;
  name: string;
  thread?: string;
  nonAttachable?: boolean;
  label: string;
  role?: string;
  depth?: number;
  state: AgentState;
  wait?: Value;
  activity?: Entry;
  turns?: number;
  roundtrips?: number;
  work?: number;
  workSince?: number;
  lastResponse?: number;
  cost?: number;
  costPartial?: boolean;
  inputTokens?: number;
  outputTokens?: number;
  usagePartial?: boolean;
  context?: number;
  window?: number;
  added?: number;
  removed?: number;
  skills?: string[];
  items?: string[];
}

export interface Roster {
  next?: PageCursor | null;
  total?: number;
  agents: Agent[];
  added?: number;
  removed?: number;
}

// The saved review: each file's net composition of its captured changes.
// Negative counts are unavailable.
export interface SavedReview {
  page?: PageCursor | null;
  fileCount: number;
  revision: number;
  workspace: string;
  coverage: string;
  failure?: string;
  // Each caller's own files, Main first; "?" is an unknown caller.
  callers: { caller: string; count: number }[];
  counts: Presentation_Counts;
}

export interface ReviewedFile {
  content?: ContentReference | null;
  key: string;
  path: string;
  chunks: Chunk[];
  origins: Origin[];
  counts: Counts;
}

export interface FeedChange {
  attachment?: string;
  entries?: Entry[];
  order?: number[];
  revision?: number;
  earlier?: PageCursor | null;
}

export interface Message {
  deferred?: DeferredSections | null;
  session?: Session;
  playback?: Playback;
  input?: Input;
  prompts?: Prompts;
  status?: Status;
  main?: FeedChange;
  agents?: FeedChange;
  roster?: Roster;
  // A patch sends null when the review becomes unavailable, and [] to clear live.
  review?: SavedReview | null;
  live?: Preview[] | null;
}

export type Action = "toggle-pause" | "faster" | "slower" | "back" | "forward" | "restart" | "seek";

export type Attachment =
  | { source: "path"; path: string }
  | { source: "upload"; name: string; data: string }
  | { source: "selection"; name: string; data: string; path?: string; inline?: boolean };

export type Start =
  | (Omit<APIStart, "text" | "attachments" | "replay"> & {
      text?: string;
      attachments?: Attachment[];
      replay?: never;
    })
  | { replay: ReplayStart };
