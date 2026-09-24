import type {ChildProcess} from "node:child_process";

export class ResolverTimeout extends Error {
  constructor() {
    super("resolver exceeded the 30 s limit; retry with a narrower --workspace ROOT");
  }
}

export async function withResolverDeadline<T>(
  query: (deadline: Promise<never>) => Promise<T>,
): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new ResolverTimeout()), 30_000);
  });
  // Observe expiry even if startup fails before the query begins racing it.
  void deadline.catch(() => {});
  try {
    return await query(deadline);
  } finally {
    clearTimeout(timer);
  }
}

type ResolverExit = {exitCode: number | null; error?: Error};

export function resolverProcess(child: ChildProcess): {
  exited: Promise<ResolverExit>;
  finish(shutdown?: () => Promise<void>): Promise<ResolverExit>;
} {
  let exit: ResolverExit | undefined;
  const exited = new Promise<ResolverExit>((resolve) => {
    child.once("error", (error) => {
      exit = {exitCode: null, error};
      resolve(exit);
    });
    child.once("exit", (exitCode) => {
      exit = {exitCode};
      resolve(exit);
    });
  });
  const closed = new Promise<ResolverExit>((resolve) => {
    child.once("close", (exitCode) => resolve(exit ?? {exitCode}));
  });
  let finishing: Promise<ResolverExit> | undefined;
  const lifecycle = {
    exited,
    finish(shutdown?: () => Promise<void>): Promise<ResolverExit> {
      finishing ??= (async () => {
        let timer: ReturnType<typeof setTimeout> | undefined;
        const forced = new Promise<ResolverExit>((resolve) => {
          timer = setTimeout(() => {
            child.kill("SIGKILL");
            child.stdin?.destroy();
            child.stdout?.destroy();
            child.stderr?.destroy();
            // The invocation owner retires the remaining group after the host
            // returns. An inherited pipe must not keep either host wait alive.
            child.unref();
            resolve(exit ?? {exitCode: null});
          }, 1_000);
        });
        const graceful = (async () => {
          try {
            await shutdown?.();
          } catch {
            child.kill("SIGKILL");
          }
          return closed;
        })();
        try {
          return await Promise.race([graceful, forced]);
        } finally {
          clearTimeout(timer);
        }
      })();
      return finishing;
    },
  };
  // Exit is not EOF: a descendant may still hold either resolver output pipe.
  // Start the same bounded drain even while a protocol request awaits its reply.
  void exited.then(() => lifecycle.finish());
  return lifecycle;
}
