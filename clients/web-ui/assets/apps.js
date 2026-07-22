// App blocks client runtime (APP-1). The node is the only data path: meta
// (instance + pinned definition), per-input reads with server-side scoping,
// and the action endpoint. The client folds the reducer vocabulary
// deterministically and renders with its own trusted per-template renderers —
// a definition can never ship code, only declare shape.

const APP_REDUCERS = {
  latest: (evs) => evs.length ? evs[evs.length - 1] : null,
  ring: (evs, r) => evs.slice(-(r.size || 20)),
  count: (evs) => evs.length,
  list: (evs) => evs,
  sum: (evs, r) => evs.reduce((a, e) => a + (Number(e.data?.[r.field]) || 0), 0),
};

async function loadAppState(instanceID) {
  const meta = await api(`/api/spaces/${current}/apps/${instanceID}`);
  const inputs = {};
  for (const name of Object.keys(meta.definition.inputs || {})) {
    try {
      const r = await api(`/api/spaces/${current}/apps/${instanceID}/inputs/${name}`);
      inputs[name] = r.events;
    } catch (e) { inputs[name] = null; } // unavailable → fallback path
  }
  const state = {};
  for (const [name, red] of Object.entries(meta.definition.state || {})) {
    const evs = inputs[red.input];
    state[name] = evs == null ? null : APP_REDUCERS[red.reducer]?.(evs, red);
  }
  return { meta, inputs, state };
}

// renderAppInstance renders an app block into a fresh node. Unavailable data
// falls back to the definition's fallback text — the post never breaks.
async function renderAppInstance(instanceID) {
  const box = document.createElement('div');
  box.className = 'app-block';
  try {
    const { meta, state } = await loadAppState(instanceID);
    const inst = meta.instance, def = meta.definition;
    const broken = Object.values(state).some(v => v === null);
    if (broken) {
      box.appendChild(appFallback(def));
      return box;
    }
    switch (inst.app_id) {
      case 'poll': renderPoll(box, inst, state); break;
      case 'form': renderForm(box, inst, state); break;
      default: renderGenericApp(box, def, state);
    }
  } catch (e) {
    const f = document.createElement('div');
    f.className = 'unknownblk';
    f.textContent = 'Interactive block unavailable.';
    box.appendChild(f);
  }
  return box;
}

function appFallback(def) {
  const f = document.createElement('div');
  f.className = 'app-fallback';
  f.textContent = def.fallback || 'Live data is currently unavailable.';
  return f;
}

// ---- poll: LWW per author (a revote replaces), counts by option ----

function foldPollVotes(events) {
  const perAuthor = new Map(); // author → option (events arrive in order)
  for (const e of events || []) {
    if (e.data?.option != null) perAuthor.set(e.author, String(e.data.option));
  }
  const counts = {};
  for (const opt of perAuthor.values()) counts[opt] = (counts[opt] || 0) + 1;
  return { counts, total: perAuthor.size, perAuthor };
}

function renderPoll(box, inst, state) {
  const q = document.createElement('div');
  q.className = 'app-title';
  q.textContent = '📊 ' + (inst.props?.question || 'Poll');
  box.appendChild(q);
  const options = (inst.props?.options || 'yes,no').split(',').map(s => s.trim()).filter(Boolean);
  const { counts, total } = foldPollVotes(state.votes);
  for (const opt of options) {
    const n = counts[opt] || 0;
    const row = document.createElement('button');
    row.className = 'app-poll-option';
    const pct = total ? Math.round(n * 100 / total) : 0;
    row.textContent = `${opt} — ${n} (${pct}%)`;
    row.onclick = async () => {
      await api(`/api/spaces/${current}/apps/${inst.instance_id}/actions/vote`,
        { method: 'POST', body: JSON.stringify({ option: opt }) });
      const fresh = await renderAppInstance(inst.instance_id);
      box.replaceWith(fresh);
    };
    box.appendChild(row);
  }
  const m = document.createElement('div');
  m.className = 'pub-meta';
  m.textContent = `${total} ${total === 1 ? 'vote' : 'votes'} · tap to vote or revote`;
  box.appendChild(m);
}

// ---- form: bounded named fields; responses visible to members (honesty) ----

function renderForm(box, inst, state) {
  const q = document.createElement('div');
  q.className = 'app-title';
  q.textContent = '📝 ' + (inst.props?.title || 'Form');
  box.appendChild(q);
  const fields = (inst.props?.fields || 'answer').split(',').map(s => s.trim()).filter(Boolean);
  const inputs = {};
  for (const f of fields) {
    const inp = document.createElement('input');
    inp.placeholder = f;
    inputs[f] = inp;
    box.appendChild(inp);
  }
  const send = document.createElement('button');
  send.className = 'btn-tinted';
  send.textContent = 'Submit';
  send.onclick = async () => {
    const vals = {};
    for (const f of fields) vals[f] = inputs[f].value;
    await api(`/api/spaces/${current}/apps/${inst.instance_id}/actions/submit`,
      { method: 'POST', body: JSON.stringify({ fields: vals }) });
    const fresh = await renderAppInstance(inst.instance_id);
    box.replaceWith(fresh);
  };
  box.appendChild(send);
  const m = document.createElement('div');
  m.className = 'pub-meta';
  const n = (state.responses || []).length;
  m.textContent = `${n} ${n === 1 ? 'response' : 'responses'} · responses are visible to members of this space`;
  box.appendChild(m);
}

// ---- generic: metric rows + event list from the declared view ----

function renderGenericApp(box, def, state) {
  const t = document.createElement('div');
  t.className = 'app-title';
  t.textContent = def.name;
  box.appendChild(t);
  for (const [name, v] of Object.entries(state)) {
    const row = document.createElement('div');
    row.className = 'pub-meta';
    if (Array.isArray(v)) {
      row.textContent = `${name}: ${v.length} events`;
    } else if (v && typeof v === 'object') {
      row.textContent = `${name}: ${JSON.stringify(v.data ?? v).slice(0, 120)}`;
    } else {
      row.textContent = `${name}: ${v}`;
    }
    box.appendChild(row);
  }
}

// ---- composer integration: create + embed a poll/form ----

async function insertAppBlock(kind) {
  const props = {};
  if (kind === 'poll') {
    const q = prompt('Poll question:');
    if (!q) return;
    const opts = prompt('Options (comma separated):', 'yes, no');
    if (!opts) return;
    props.question = q;
    props.options = opts;
  } else {
    const title = prompt('Form title:');
    if (!title) return;
    const fields = prompt('Fields (comma separated):', 'answer');
    if (!fields) return;
    props.title = title;
    props.fields = fields;
  }
  try {
    const r = await api(`/api/spaces/${current}/apps`, { method: 'POST',
      body: JSON.stringify({ app_id: kind, document_id: composerDoc.document_id, props }) });
    composerDoc.blocks.push({ id: 'b' + randHex16().slice(0, 8), type: 'app',
      props: { asset: r.instance_id } });
    renderComposerBlocks();
  } catch (err) { document.getElementById('compMsg').textContent = err.message; }
}
