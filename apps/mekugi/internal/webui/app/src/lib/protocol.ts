import type {
  Start as APIStart,
  ReplayStart,
  PageResult as APIPageResult,
} from "../../../../sessionapi/protocol.ts";
export type {
  PageCursor,
  ContentReference,
  ContentResult,
  DeferredSections,
  AgentIdentity,
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
import type { Preview } from "../../../../sessionapi/protocol.ts";
export type {
  Chunk,
  Counts,
  Origin,
  Preview,
  ReviewFile,
} from "../../../../sessionapi/protocol.ts";

export type {
  Prompts,
  QuestionCall,
  Question,
  Approval,
  listed as Listed,
  status as Status,
  read as Read,
  change as Change,
  value as Value,
  entry as Entry,
  savedReview as SavedReview,
  reviewFile as ReviewedFile,
} from "../../../../sessionapi/protocol.ts";
import type {
  agent,
  roster,
  message,
  feedPatch,
  entry,
  Prompts,
  status,
  savedReview,
} from "../../../../sessionapi/protocol.ts";
export type AgentState = "working" | "waiting" | "error" | "done" | "idle" | "paused";
export type Agent = agent & { state: AgentState };
export type Roster = Omit<roster, "agents"> & { agents: Agent[] };
export type FeedChange = Partial<Omit<feedPatch, "entries">> & { entries?: entry[] };
export type Message = Omit<
  message,
  "prompts" | "status" | "roster" | "review" | "live" | "main" | "agents"
> & {
  main?: FeedChange;
  agents?: FeedChange;
  prompts?: Prompts;
  status?: status;
  roster?: Roster;
  review?: savedReview | null;
  live?: Preview[] | null;
};

export type Action = "toggle-pause" | "faster" | "slower" | "back" | "forward" | "restart" | "seek";

export type Attachment =
  | { source: "path"; path: string }
  | { source: "upload"; name: string; data: string }
  | { source: "selection"; name: string; data: string; path?: string; inline?: boolean };

export type Start = (Omit<APIStart, "replay"> & { replay?: never }) | { replay: ReplayStart };
