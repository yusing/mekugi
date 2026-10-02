type JournalState = "pending" | "working" | "done" | "blocked" | "dropped";
type JournalPlanTask = string | {
  p?: string; title: string; body?: string; state?: JournalState;
  reason?: string; tasks?: JournalPlanTask[];
};
type JournalPlan = { op: "plan"; under?: string; tasks: JournalPlanTask[]; reset?: "slice" };
type JournalAdd = { op: "add"; under?: string; title: string; body?: string; before?: string } & (
  | { kind: "task"; state?: JournalState; reason?: string; agent?: string }
  | { kind?: "note" | "context"; state?: never; reason?: never; agent?: never }
);
type JournalSet = {
  op: "set"; p: string; title?: string; body?: string;
  state?: JournalState; reason?: string; agent?: string;
};
type JournalMutation = JournalPlan | JournalAdd | JournalSet
  | { op: "log"; p?: string; text: string }
  | { op: "remove"; p: string }
  | { op: "finish" };
type JournalRead = { op: "read"; p?: string; agent?: string; depth?: number; view?: "combined" | "own" | "tasks" };
type JournalStamp = { seq: number; at?: string };
type JournalNode = {
  path: string; kind: "task" | "note" | "context" | "answer";
  title: string; body?: string; state?: JournalState; reason?: string;
  question?: string; agent?: string; author: string;
  created: JournalStamp; updated: JournalStamp;
  started?: JournalStamp; finished?: JournalStamp; children: JournalNode[];
};
declare function journal(input: JournalRead): Promise<JournalNode[]>;
declare function journal(input: JournalPlan | JournalMutation[]): Promise<string[]>;
declare function journal(input: Exclude<JournalMutation, JournalPlan>): Promise<string | null>;
declare function journal(input: JournalMutation | JournalMutation[] | JournalRead): Promise<string | null | string[] | JournalNode[]>;
