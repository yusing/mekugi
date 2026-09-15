// Evaluate this test driver in a real Code Mode cell. Set nativeFixture to the
// metadata emitted by TestHpatchNativeFixture. Each invocation uses actual host
// tools; options only inject lifecycle pauses or a host application failure.
// Run each case in a fresh fixture. For hard-cancel cases, terminate the outer
// cell at native_test_paused/native_test_wait_started/application_returned,
// inspect files and sessions, then call nativeMixedRun("resume HANDLE ...").
// For wait, create native-release only after confirming the yielded session is
// still alive; native-wait-count must contain one x after plain resume.
// Partial application and afterApplication must reject plain resume; accept is
// valid after confirming their intended file exists and native work has ended.
// BeforeApplication must leave its file absent; inspected retry creates it once.
// CompletedNoop must append only one x after plain resume.
const nativeMixedCases = {
  failedShell: {
    source: 'new native-kept.txt\ntype "kept\\n"\nshell printf x >> native-attempts; exit 7\nnew native-suffix.txt\ntype "done\\n"',
    options: {}
  },
  freshTargets: {
    source: 'new native-target.txt\ntype "old\\n"\nshell false\nin native-target.txt\ntype "old" "new"',
    options: {}
  },
  wait: {
    source: 'shell <<SHELL\n#!params={"yield_time_ms":1000,"max_output_tokens":30000}\npython3 -c \'print("v"*40000)\'\nwhile [ ! -f native-release ]; do sleep 0.1; done\nprintf x >> native-wait-count\nSHELL\nnew native-after-wait.txt\ntype "done\\n"',
    options: {observeWait: true}
  },
  partialApplication: {
    source: 'new native-partial.txt\ntype "first file\\n"\nshell printf x >> native-partial-suffix',
    options: {failApplication: true}
  },
  beforeApplication: {
    source: 'new native-before-apply.txt\ntype "once\\n"\nshell true',
    options: {pausePhase: 'applying', pauseSegment: 1}
  },
  afterApplication: {
    source: 'shell true\nnew native-cancelled-apply.txt\ntype "applied once\\n"\nshell printf x >> native-cancelled-suffix',
    options: {pauseAfterApplication: true}
  },
  completedNoop: {
    source: 'new native-noop.txt\ntype "same\\n"\nshell true\nin native-noop.txt\ntype "same" "same"\nshell printf x >> native-noop-count',
    options: {pausePhase: 'segment_completed', pauseSegment: 3}
  }
};

async function nativeMixedRun(source, options = {}) {
  const fixture = nativeFixture;
  const quote = value => "'" + value.replaceAll("'", "'\\''") + "'";
  const response = await tools.exec_command({
    cmd: "curl -fsS --max-time 5 --data-binary " + quote(source) + " " + quote(fixture.url + "/translate"),
    max_output_tokens: 30000
  });
  const generated = JSON.parse(response.output);
  if (generated.error) return {rejected: generated.error};
  const host = tools;
  let summary;
  const checkpoints = [];
  let notificationCount = 0;
  const phase = options.pausePhase;
  const segment = options.pauseSegment;
  let paused = false;
  async function execute(tools, notify, text) {
    await eval("(async () => {\n" + generated.carrier + "\n})()");
  }
  let error;
  try {
    await execute({
      exec_command: async args => {
        const result = await host.exec_command({
          ...args, workdir: args.workdir || fixture.root,
          cmd: (args.cmd === "shell" || args.cmd.startsWith("shell "))
            ? "env MEKUGI_HPATCH_WORKER_TEST=1 " + quote(fixture.wrapper) + " " + args.cmd.slice(6)
            : args.cmd
        });
        return result;
      },
      write_stdin: async args => {
        const pending = host.write_stdin(args);
        const checkpoint = checkpoints.at(-1);
        if (options.observeWait && args.chars === '' &&
            checkpoint?.phase === 'awaiting_session' && checkpoint?.session_id === args.session_id) {
          notify({native_test_wait_started: checkpoint});
        }
        const result = await pending;
        if (args.chars?.startsWith('{"operation":"checkpoint"')) {
          const observed = await host.exec_command({
            cmd: "curl -fsS --max-time 5 " + quote(fixture.url + "/progress?handle=" + generated.handle),
            max_output_tokens: 2000
          });
          const saved = JSON.parse(observed.output);
          checkpoints.push({...saved.current, completed_segments: saved.completed_segments,
            resume: "resume " + saved.resume_handle, control_session_id: saved.control_session_id});
        }
        const persisted = checkpoints.at(-1);
        // Pause after the checkpoint RPC has returned and its saved state was
        // observed, so hard termination exercises a persisted boundary.
        if (!paused && phase && args.chars?.startsWith('{"operation":"checkpoint"') &&
            persisted?.phase === phase && persisted?.segment === segment) {
          paused = true;
          notify({native_test_paused: persisted});
          await new Promise(resolve => setTimeout(resolve, 120000));
        }
        return result;
      },
      apply_patch: async patch => {
        // The fixture has its own request cwd; the testing host's apply_patch
        // uses the live agent cwd, so preserve the fixture's relative identities.
        patch = patch.replace(/^(\*\*\* (?:Add File|Update File|Delete File|Move to): )([^\n]+)/gm,
          (_, prefix, path) => prefix + (path.startsWith('/') ? path : fixture.root + '/' + path));
        if (options.failApplication) {
          // An Add File whose parent is a regular fixture file fails in the
          // native host's write phase, after earlier paths may have been written.
          patch = patch.replace("*** End Patch",
            "*** Add File: " + fixture.root + "/native-blocker/child\n+blocked\n*** End Patch");
        }
        const result = await host.apply_patch(patch);
        if (options.pauseAfterApplication) {
          notify({native_test_application_returned: true, resume: 'resume ' + generated.handle});
          await new Promise(resolve => setTimeout(resolve, 120000));
        }
        return result;
      }
    }, value => {
      notificationCount++;
      throw new Error('Production carrier emitted an unexpected notification: ' + JSON.stringify(value));
    }, value => { summary = JSON.parse(value); });
  } catch (caught) {
    error = String(caught);
  }
  return {summary, checkpoints, notificationCount, error};
}
