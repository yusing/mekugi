// The carrier owns host execution. The private worker only persists continuation
// state, independently of the cell's lifetime, and translates workspace edits.
const nativeTools = tools;
{
const state = mixedConfig.state;
const progress = state.progress;
let checkpointedProgress = JSON.parse(JSON.stringify(progress));
// Retain only inserted repairs, rather than copying the original plan into every
// checkpoint. Insertion indices refer to the plan after earlier insertions.
const segments = [...state.segments];
for (const insertion of progress.insertions ?? []) {
  if (insertion.following) segments[insertion.index] = insertion.following;
  segments.splice(insertion.index, 0, insertion.segment);
}
let current = progress.current ?? null;
let output = '';
let last = {output: '', exit_code: 0};
let stoppedReason = null;
let operationIndex = 0;
let storageFailed = false;
const revalidate = {};
let refreshTranslation = !!current && current.kind === 'edit' &&
  !progress.operations.some(operation => operation.method === 'apply_patch');

let controlSession = null;
const controlBudget = 100000;
async function closeControl(session) {
  let result = await nativeTools.write_stdin({session_id: session,
    chars: '{"operation":"close"}\n', yield_time_ms: 250, max_output_tokens: controlBudget});
  while (result.session_id != null) {
    result = await nativeTools.write_stdin({session_id: result.session_id,
      chars: '', yield_time_ms: 5000, max_output_tokens: controlBudget});
  }
}
async function controlFrame(request) {
  if (controlSession == null) throw new Error('HPATCH control channel is unavailable.');
  let result = await nativeTools.write_stdin({session_id: controlSession,
    chars: JSON.stringify(request) + '\n', yield_time_ms: 250, max_output_tokens: controlBudget});
  let received = result.output || '';
  while (result.session_id != null && !received.endsWith('\n')) {
    result = await nativeTools.write_stdin({session_id: controlSession,
      chars: '', yield_time_ms: 5000, max_output_tokens: controlBudget});
    received += result.output || '';
  }
  if (result.session_id == null) {
    controlSession = null;
    throw new Error('HPATCH control channel stopped; inspect the retained handle before continuing.');
  }
  // Parse the complete framed reply; truncated or extra output must never
  // acknowledge a checkpoint or authorize a patch.
  return JSON.parse(received);
}
async function controlRequest(request) {
  let frame = await controlFrame(request);
  let received = '';
  for (;;) {
    if (typeof frame.data !== 'string' || typeof frame.more !== 'boolean') {
      throw new Error('Invalid HPATCH control response frame.');
    }
    received += frame.data;
    if (received.length > 32 * 1024 * 1024) throw new Error('HPATCH control response exceeds retention limit.');
    if (!frame.more) return JSON.parse(received);
    frame = await controlFrame({operation: 'next'});
  }
}
async function startControl() {
  // The old channel is read-only apart from private checkpoints. Drain and
  // close it before translating afresh; never restart a workspace process.
  if (progress.control_session_id != null) {
    try {
      await closeControl(progress.control_session_id);
    } catch (error) {
      if (!/unknown.*(?:session|process)|(?:session|process).*(?:not found|unknown|not running)|no such.*(?:session|process)/i.test(String(error))) throw error;
    }
  }
  let result = await nativeTools.exec_command({
    cmd: 'shell', login: false, tty: true, yield_time_ms: 1000, max_output_tokens: 1000
  });
  controlSession = result.session_id ?? null;
  let received = result.output || '';
  while (result.session_id != null && 'HPATCH-READY\n'.startsWith(received) && received !== 'HPATCH-READY\n') {
    result = await nativeTools.write_stdin({session_id: controlSession,
      chars: '', yield_time_ms: 1000, max_output_tokens: 1000});
    received += result.output || '';
  }
  if (result.session_id == null || received !== 'HPATCH-READY\n') {
    controlSession = result.session_id ?? null;
    throw new Error('HPATCH control channel did not become ready.');
  }
  const response = await controlRequest({operation: 'open', handle: state.handle});
  if (response.opened !== true || response.progress === null || typeof response.progress !== 'object') {
    throw new Error('HPATCH control channel did not bind the retained handle.');
  }
  checkpointedProgress = response.progress;
}
function checkpointMutations(before, after, path = [], mutations = []) {
  if (Object.is(before, after)) return mutations;
  if (Array.isArray(before) && Array.isArray(after)) {
    if (after.length < before.length) {
      mutations.push({path, value: after});
      return mutations;
    }
    const shared = Math.min(before.length, after.length);
    for (let index = 0; index < shared; index++) {
      checkpointMutations(before[index], after[index], [...path, String(index)], mutations);
    }
    for (let index = shared; index < after.length; index++) {
      mutations.push({path: [...path, String(index)], value: after[index]});
    }
    return mutations;
  }
  const beforeObject = before !== null && typeof before === 'object' && !Array.isArray(before);
  const afterObject = after !== null && typeof after === 'object' && !Array.isArray(after);
  if (beforeObject && afterObject) {
    for (const key of Object.keys(before)) {
      if (!Object.hasOwn(after, key)) mutations.push({path: [...path, key], remove: true});
    }
    for (const key of Object.keys(after)) {
      if (!Object.hasOwn(before, key)) mutations.push({path: [...path, key], value: after[key]});
      else checkpointMutations(before[key], after[key], [...path, key], mutations);
    }
    return mutations;
  }
  mutations.push({path, value: after});
  return mutations;
}
async function checkpoint(phase, copies = []) {
  if (current) current.phase = phase;
  progress.current = current;
  progress.control_session_id = controlSession;
  const nextProgress = JSON.parse(JSON.stringify(progress));
  const mutations = checkpointMutations(checkpointedProgress, nextProgress);
  for (const copy of copies) {
    const index = mutations.findIndex(mutation =>
      mutation.path.length === copy.path.length &&
      mutation.path.every((component, pathIndex) => component === copy.path[pathIndex]));
    if (index < 0) throw new Error('Checkpoint copy does not match changed state.');
    mutations[index] = copy;
  }
  try {
    const response = await controlRequest({
      operation: 'checkpoint', revision: state.revision, mutations
    });
    if (response.revision !== state.revision + 1) {
      throw new Error('Checkpoint storage failed or this carrier is stale. Stop; inspect the retained handle before continuing.');
    }
    state.revision++;
    checkpointedProgress = nextProgress;
  } catch (error) {
    storageFailed = true;
    throw error;
  }
}

// A recorded successful operation is returned, never reexecuted. An interrupted
// empty write_stdin is a wait on the SAME session, not a new process. Every other
// unconfirmed operation requires explicit external reconciliation.
async function invoke(method, args) {
  const index = operationIndex++;
  let operation = progress.operations[index];
  if (operation) {
    if (operation.method !== method) throw new Error('Retained operation order changed; reconcile this segment.');
    if (!operation.pending) return operation.result;
    if (method !== 'translate' && (method !== 'write_stdin' || operation.sent_input)) {
      throw new Error('An operation has an unknown outcome. Resolve the previous cell, sessions, and effects, then use retry, repair, or accept.');
    }
  } else {
    operation = {method, pending: true, sent_input: method === 'write_stdin' && !!args.chars};
    progress.operations.push(operation);
  }
  if (method === 'write_stdin') current.session_id = args.session_id;
  await checkpoint(method === 'apply_patch' ? 'applying' :
    method === 'write_stdin' ? 'awaiting_session' : current.kind === 'edit' ? 'translating' : 'executing');
  const result = method === 'translate'
    ? {output: JSON.stringify(await controlRequest({operation: 'translate', source: args})), exit_code: 0}
    : await nativeTools[method](args);
  operation.result = result;
  operation.pending = false;
  if (result?.session_id != null) current.session_id = result.session_id;
  else delete current.session_id;
  await checkpoint(result?.session_id != null ? 'session_available' :
    method === 'apply_patch' ? 'application_returned' : 'native_returned',
    method === 'translate' ? [{path: ['operations', String(index), 'result'], copy: 'translation'}] : []);
  return result;
}
async function executeEdit(source) {
  await translateSource(source);
  if (last.exit_code !== 0) {
    current.status = 'failed';
    current.output = output;
    stoppedReason = 'translation_error';
    return;
  }
  let edit;
  try {
    edit = JSON.parse(output);
  } catch {
    throw new Error('HPATCH translation output is incomplete or truncated; this segment was not applied. Repair this segment and resume.');
  }
  if (typeof edit.patch !== 'string' || typeof edit.report !== 'string' || typeof edit.diagnostic !== 'string') {
    throw new Error('Invalid HPATCH translation result; this segment was not applied.');
  }
  if (edit.diagnostic) {
    current.status = 'rejected';
    current.diagnostic = edit.diagnostic;
    stoppedReason = 'edit_rejected';
    return;
  }
  output = '';
  last = {output: '', exit_code: 0};
  if (edit.patch) await tools.apply_patch(edit.patch);
  current.report = edit.report;
  if (edit.attempt_id) await controlRequest({operation: 'confirm', attempt_id: edit.attempt_id});
}
const tools = {
  exec_command: args => invoke('exec_command', args),
  write_stdin: args => invoke('write_stdin', args),
  apply_patch: async patch => {
    const result = await invoke('apply_patch', patch);
    if (result?.isError === true) {
      throw new Error('Host HPATCH application failed; effects may be partial or unknown. Inspect current files before retrying.');
    }
    return result;
  }
};
async function finish(args, forward = false) {
  let result = await tools.exec_command(args);
  if (forward) { last = result; output += result.output || ''; }
  while (result.session_id != null) {
    result = await tools.write_stdin({session_id: result.session_id, chars: '',
      yield_time_ms: 300000, max_output_tokens: args.max_output_tokens});
    if (forward) { last = result; output += result.output || ''; }
  }
  return result;
}
async function run(args) {
  await finish(args, true);
  if (refreshTranslation) throw revalidate;
}
async function translateSource(source) {
  const result = await invoke('translate', source);
  last = result;
  output = result.output;
  if (refreshTranslation) throw revalidate;
}
function completeCurrent() {
  refreshTranslation = false;
  progress.results.push(current);
  progress.index++;
  progress.operations = [];
  delete progress.replacement;
  current = null;
  progress.current = null;
}

if (mixedConfig.action || mixedConfig.replacement) {
  if (!current || current.status === 'completed') {
    throw new Error('No failed or interrupted segment to reconcile; use the plain resume handle.');
  }
  if (mixedConfig.action !== 'repair' && mixedConfig.replacement && mixedConfig.replacement.kind !== current.kind) {
    throw new Error('A replacement must keep the failed segment kind.');
  }
  // These explicit actions attest that the old cell has ended, all potentially
  // live sessions have been resolved, and uncertain effects have been inspected.
  if (mixedConfig.action === 'accept') {
    current.status = 'reconciled';
    delete current.report;
    delete current.session_id;
    completeCurrent();
  } else {
    if (mixedConfig.action === 'repair') {
      const insertion = {
        index: progress.index,
        segment: {...mixedConfig.replacement, repair: true},
        following: progress.replacement
          ? {...segments[progress.index], ...progress.replacement, line: segments[progress.index].line}
          : null
      };
      (progress.insertions ??= []).push(insertion);
      if (insertion.following) segments[insertion.index] = insertion.following;
      segments.splice(insertion.index, 0, insertion.segment);
      delete progress.replacement;
    } else if (mixedConfig.replacement) {
      progress.replacement = mixedConfig.replacement;
    }
    progress.operations = [];
    current = null;
    progress.current = null;
  }
  refreshTranslation = false;
} else if (current?.status === 'failed' && current.kind === 'shell') {
  throw new Error(`The failed shell may have changed state. Inspect it, then use resume ${state.handle} retry, repair, or accept.`);
} else if (progress.operations.some(operation =>
  operation.method === 'apply_patch' && (operation.pending || operation.result?.isError === true))) {
  throw new Error(`Host application is unresolved. Inspect effects and resolve live work before resume ${state.handle} retry, repair, or accept.`);
}

try {
  await startControl();
  // Revision comparison prevents an older carrier from starting another
  // operation after another continuation has claimed this retained state.
  await checkpoint('continuation_claimed');
  if (current?.status === 'completed') {
    completeCurrent();
    await checkpoint('between_segments');
  }
  while (progress.index < segments.length) {
    const segment = progress.replacement ?? segments[progress.index];
    current = {segment: progress.index + 1, line: segments[progress.index].line,
      kind: segment.kind, status: 'started', session_id: current?.session_id,
      ...(segments[progress.index].repair ? {repair: true} : {})};
    await checkpoint('segment_start');
    for (;;) {
      operationIndex = 0;
      output = '';
      last = {output: '', exit_code: 0};
      try {
        if (segment.kind === 'edit') await executeEdit(segment.source);
        else await eval('(async () => {\n' + segment.program + '\n})()');
        break;
      } catch (error) {
        if (error !== revalidate) throw error;
        // Finish any old translation session first, then resolve every target
        // again against current files instead of applying its cached patch.
        refreshTranslation = false;
        progress.operations = [];
        await checkpoint('revalidating');
      }
    }
    if (stoppedReason) {
      await checkpoint('segment_stopped');
      break;
    }
    current.status = 'completed';
    await checkpoint('segment_completed');
    completeCurrent();
    await checkpoint('between_segments');
  }
} catch (error) {
  stoppedReason = 'host_error';
  if (current) {
    current.status = 'interrupted';
    current.output = current.kind === 'shell' ? output : '';
    current.diagnostic = String(error);
  }
  if (!storageFailed) await checkpoint('unresolved');
  throw error;
} finally {
  let cleanupDiagnostic;
  if (controlSession != null) {
    try {
      await closeControl(controlSession);
      controlSession = null;
    } catch (error) {
      cleanupDiagnostic = String(error);
    }
  }
  const results = current ? [...progress.results, current] : progress.results;
  text(JSON.stringify({change_id: state.change_id, results, resume_handle: state.handle, expires_at: state.expires_at,
    ...(cleanupDiagnostic ? {control_session_id: controlSession, cleanup_diagnostic: cleanupDiagnostic} : {}), sequence: {
    segment_count: segments.length, started_segments: results.length,
    not_started_segments: segments.length - results.length, stopped_reason: stoppedReason
  }}));
}
}
