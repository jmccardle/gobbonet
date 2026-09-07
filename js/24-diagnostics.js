/* @gobbonet-split js/24-diagnostics.js
   Connection diagnostics modal, opened from the header status label.
   Load order is a contract -- see REFACTOR-PLAN.md before reordering.
   Must load before 25-boot.js, which wires the page lifecycle.
   @end-split-header */
/* ================================================================
   CONNECTION DIAGNOSTICS

   The status label used to be the end of the line: it reduced every
   possible failure to one word, threw the detail away, and left the
   user with "Error: 502" -- which says a proxy got no answer, not
   which of the three links between them and the model is broken.

   The server has known the answer the whole time. The supervisor
   captures llama-server's stderr, the job manager records why each
   generation ended, the config knows whether this is a supervised
   install or a proxy to somebody else's llama.cpp. None of it was
   ever shown. /diagnostics.json now assembles all of it, and this
   module renders it behind a click on the label.

   The design goal is one paste. A user who cannot read a terminal
   should be able to open this, press COPY REPORT, and put everything
   a maintainer needs into an issue -- without being walked through
   four rounds of "what does X say".
================================================================ */

// The last report fetched, kept so COPY REPORT does not re-probe (which would
// take another five timeouts on a broken install, and could plausibly report
// something different from what the user is looking at).
let _diagReport = null;
let _diagLoading = false;

/* Local-time formatter for the epoch seconds the job records carry. */
function _diagTime(epochSeconds) {
  if (!epochSeconds) return '';
  try {
    return new Date(epochSeconds * 1000).toLocaleTimeString();
  } catch (e) {
    return String(epochSeconds);
  }
}

function _diagBytes(n) {
  if (!n || n <= 0) return '—';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0, v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return (i === 0 ? v : v.toFixed(1)) + ' ' + units[i];
}

/* ================================================================
   OPEN / CLOSE
================================================================ */
function openDiagnostics() {
  const modal = document.getElementById('diag-modal');
  if (!modal) return;
  modal.classList.add('open');
  refreshDiagnostics();
}

function closeDiagnostics() {
  const modal = document.getElementById('diag-modal');
  if (modal) modal.classList.remove('open');
}

/* ================================================================
   FETCH

   Opened as a file:// page there is no server to ask, and the fetch
   would throw a bare TypeError. That case is named explicitly rather
   than caught and shrugged at: "you opened chat.html directly" is a
   real and common mistake with a real fix, and it is not the same
   problem as a server that is down.
================================================================ */
async function refreshDiagnostics(withTest) {
  const body = document.getElementById('diag-body');
  if (!body || _diagLoading) return;

  if (!IS_SERVED) {
    body.innerHTML = _diagNotice(
      'This page was opened directly from disk',
      'There is no GobboNet server to ask, so no report can be built. Open the ' +
      'chat at the address GobboNet prints when it starts (http://127.0.0.1:9066 ' +
      'by default) rather than opening chat.html itself.');
    return;
  }

  _diagLoading = true;
  body.innerHTML = '<div class="diag-loading">Collecting…' +
    (withTest ? ' sending a test message to the model — this can take a moment.' : '') +
    '</div>';

  try {
    const resp = await fetch('/diagnostics.json', {
      // POST is what asks for the live test message. It is a real outbound
      // request to the model, so it is never what a plain open does.
      method: withTest ? 'POST' : 'GET',
      cache: 'no-store',
    });

    if (!resp.ok) {
      body.innerHTML = _diagNotice(
        `The server answered ${resp.status}`,
        resp.status === 404
          ? 'This build of the GobboNet server predates the diagnostics endpoint. ' +
            'Update the server, or run "gobbonet debug-report" from a terminal instead.'
          : 'The diagnostics endpoint is reachable but refused the request.');
      return;
    }

    _diagReport = await resp.json();
    renderDiagnostics(_diagReport);
  } catch (e) {
    // Deliberately not silent. Everywhere else a failed diagnostic fetch is
    // decoration on top of a status we already have; here it IS the content,
    // and an empty panel would read as "nothing wrong".
    body.innerHTML = _diagNotice('Could not reach the GobboNet server',
      escapeHtml(e.message || String(e)) +
      '. The page is loaded but the server behind it is not answering — it may ' +
      'have stopped since this tab was opened.');
  } finally {
    _diagLoading = false;
  }
}

function _diagNotice(title, detail) {
  return '<div class="diag-notice"><b>' + escapeHtml(title) + '</b>' +
         '<div>' + detail + '</div></div>';
}

/* ================================================================
   RENDER
================================================================ */
function renderDiagnostics(r) {
  const body = document.getElementById('diag-body');
  if (!body) return;

  // The pre-login shape. Reachable here only if the session expired between
  // the page loading and the modal opening, which is worth saying plainly
  // rather than rendering as a report with most of it missing.
  if (r.tier === 'public') {
    body.innerHTML = _diagNotice('Your session has ended',
      'The server answered, but no longer recognises this browser as signed in' +
      (r.auth && r.auth.session === 'stale'
        ? ' — the session expired, or the server restarted with a different password.'
        : '.') +
      ' Reload the page and sign in to see the full report.');
    return;
  }

  let h = '';
  h += _diagSummary(r);
  h += _diagConnection(r);
  h += _diagRuntime(r);
  h += _diagConfig(r);
  h += _diagSystem(r);
  h += _diagData(r);
  h += _diagNotes(r);
  body.innerHTML = h;
}

/* --- summary ---------------------------------------------------- */
function _diagSummary(r) {
  const b = r.build || {};
  const rows = [];

  const verdict = r.network ? r.network.verdict : 'not tested';
  const bad = !r.network || !/reachable/.test(verdict);
  rows.push(['Connection', verdict, bad ? 'bad' : 'good']);

  // "Which kind of install is this" is the question behind half of all
  // model-switching confusion, so it is stated in words rather than as a mode
  // string nobody outside the codebase knows how to read.
  const mode = r.config ? r.config.mode : '';
  rows.push(['Setup', mode === 'local'
    ? 'GobboNet manages llama.cpp for you — model switching works from the header'
    : 'Connecting to an LLM server you run yourself — GobboNet only proxies to it',
    'neutral']);

  rows.push(['Version', (b.version || '—') + ' · ' + (b.origin_detail || ''), 'neutral']);
  if (b.stamp_mismatch) rows.push(['Build warning', b.stamp_mismatch, 'bad']);

  const sup = r.runtime && r.runtime.supervisor;
  if (sup && sup.last_error) rows.push(['llama.cpp said', sup.last_error, 'bad']);

  let h = '<div class="diag-section"><div class="diag-section-label">Summary</div><table class="diag-kv">';
  for (const [k, v, tone] of rows) {
    h += '<tr><th>' + escapeHtml(k) + '</th><td class="diag-' + tone + '">' +
         escapeHtml(String(v)) + '</td></tr>';
  }
  h += '</table></div>';
  return h;
}

/* --- connection ------------------------------------------------- */
function _diagConnection(r) {
  if (!r.network) return '';
  let h = '<div class="diag-section"><div class="diag-section-label">Connection</div>';

  h += '<table class="diag-table"><thead><tr><th>hop</th><th>status</th><th>ms</th>' +
       '<th>detail</th></tr></thead><tbody>';
  for (const hop of (r.network.hops || [])) {
    const detail = hop.error || hop.note || hop.body || '';
    h += '<tr class="' + (hop.error ? 'diag-row-bad' : '') + '">' +
         '<td><b>' + escapeHtml(hop.name) + '</b><br><span class="diag-url">' +
         escapeHtml(hop.url) + '</span></td>' +
         '<td>' + (hop.status ? hop.status : '—') + '</td>' +
         '<td>' + (hop.latency_ms || 0) + '</td>' +
         '<td class="diag-detail">' + escapeHtml(detail) + '</td></tr>';
  }
  h += '</tbody></table>';

  const tc = r.network.test_completion;
  if (tc) {
    h += '<div class="diag-test ' + (tc.ok ? 'diag-good' : 'diag-bad') + '">' +
         (tc.ok
           ? 'Test message succeeded in ' + tc.latency_ms + ' ms — ' +
             escapeHtml(tc.model || 'the model') + ' replied ' +
             '<code>' + escapeHtml(tc.reply || '') + '</code>'
           : 'Test message FAILED after ' + tc.latency_ms + ' ms' +
             (tc.status ? ' (HTTP ' + tc.status + ')' : '') +
             (tc.error ? '<pre>' + escapeHtml(tc.error) + '</pre>' : '')) +
         '</div>';
  } else {
    h += '<div class="diag-hint">No live test has been run. ' +
         '<button class="btn btn-small" onclick="refreshDiagnostics(true)">SEND TEST MESSAGE</button> ' +
         'sends one short message to the model — nothing from your conversations.</div>';
  }

  h += '</div>';
  return h;
}

/* --- runtime ---------------------------------------------------- */
function _diagRuntime(r) {
  const rt = r.runtime;
  if (!rt) return '';
  let h = '<div class="diag-section"><div class="diag-section-label">Server</div>';

  h += '<table class="diag-kv">';
  h += '<tr><th>process</th><td>pid ' + rt.pid +
       (rt.uptime ? ' · up ' + escapeHtml(rt.uptime) : '') + '</td></tr>';
  if (rt.listener) {
    const l = rt.listener;
    h += '<tr><th>listening</th><td>' + escapeHtml(l.host) + ':' + l.port +
         (l.lan_reachable ? '' : ' <span class="diag-bad">(loopback only — not reachable from other devices)</span>') +
         '</td></tr>';
    if (l.fell_back) {
      h += '<tr><th class="diag-bad">bind</th><td class="diag-bad">' +
           'asked for a network-wide bind and was refused: ' +
           escapeHtml(l.bind_error || '') + '</td></tr>';
    }
    if (l.addresses && l.addresses.length) {
      h += '<tr><th>reach it at</th><td>' +
           l.addresses.map(a => '<code>' + escapeHtml(a) + '</code>').join('<br>') +
           '</td></tr>';
    }
  }
  h += '</table>';

  const sup = rt.supervisor;
  if (sup) {
    h += '<div class="diag-sub">llama.cpp — phase <b>' + escapeHtml(sup.phase || '?') + '</b>' +
         (sup.model ? ' · ' + escapeHtml(sup.model) : '') + '</div>';
    if (sup.last_error) h += '<pre class="diag-error">' + escapeHtml(sup.last_error) + '</pre>';
    if (sup.stderr) {
      h += '<details class="diag-details"><summary>llama.cpp output (last lines)</summary>' +
           '<pre>' + escapeHtml(sup.stderr) + '</pre></details>';
    }
  }

  if (rt.jobs && rt.jobs.length) {
    h += '<div class="diag-sub">Recent replies</div>';
    h += '<table class="diag-table"><thead><tr><th>when</th><th>status</th>' +
         '<th>size</th><th>error</th></tr></thead><tbody>';
    for (const j of rt.jobs) {
      h += '<tr class="' + (j.error ? 'diag-row-bad' : '') + '">' +
           '<td>' + escapeHtml(_diagTime(j.started_at)) + '</td>' +
           '<td>' + escapeHtml(j.status || '') + '</td>' +
           '<td>' + _diagBytes(j.bytes) + '</td>' +
           '<td class="diag-detail">' + escapeHtml(j.error || '—') + '</td></tr>';
    }
    h += '</tbody></table>';
  }

  h += '</div>';
  return h;
}

/* --- configuration ---------------------------------------------- */
function _diagConfig(r) {
  const c = r.config;
  if (!c) return '';

  const changed = (c.entries || []).filter(e => e.changed);
  let h = '<div class="diag-section"><div class="diag-section-label">Configuration</div>';
  h += '<table class="diag-kv"><tr><th>file</th><td><code>' + escapeHtml(c.path) + '</code></td></tr>';
  h += '<tr><th>type</th><td>' + (c.mode === 'local'
        ? 'GobboNet-managed llama.cpp'
        : 'upstream / external API') + '</td></tr></table>';

  for (const d of (c.deferred || [])) {
    h += '<div class="diag-error-line">' + escapeHtml(d) + '</div>';
  }

  h += '<div class="diag-sub">Changed from defaults</div>';
  if (!changed.length) {
    h += '<div class="diag-hint">Nothing — this is a stock configuration.</div>';
  } else {
    h += '<table class="diag-table"><thead><tr><th>setting</th><th>value</th>' +
         '<th>default</th></tr></thead><tbody>';
    for (const e of changed) {
      h += '<tr><td><code>' + escapeHtml(e.key) + '</code></td>' +
           '<td class="diag-detail">' + escapeHtml(e.value) + '</td>' +
           '<td class="diag-detail">' + escapeHtml(e.default || '—') + '</td></tr>';
    }
    h += '</tbody></table>';
  }

  const rest = (c.entries || []).filter(e => !e.changed);
  if (rest.length) {
    h += '<details class="diag-details"><summary>' + rest.length +
         ' settings at their defaults</summary><table class="diag-table"><tbody>';
    for (const e of rest) {
      h += '<tr><td><code>' + escapeHtml(e.key) + '</code></td>' +
           '<td class="diag-detail">' + escapeHtml(e.value) + '</td></tr>';
    }
    h += '</tbody></table></details>';
  }

  h += '</div>';
  return h;
}

/* --- system ----------------------------------------------------- */
function _diagSystem(r) {
  const s = r.system;
  if (!s) return '';
  let h = '<div class="diag-section"><div class="diag-section-label">System</div>';

  h += '<table class="diag-kv">';
  h += '<tr><th>machine</th><td>' + escapeHtml(s.os_version || s.os) + ' · ' +
       s.num_cpu + ' CPU · ' + _diagBytes(s.total_ram_bytes) + ' RAM</td></tr>';
  h += '<tr><th>running as</th><td>' + escapeHtml(s.user || '—') +
       (s.elevated ? ' <b>(elevated)</b>' : '') + '</td></tr>';
  h += '</table>';

  // Locale notes are warnings with an explanation attached — the CP932
  // backslash-renders-as-yen case among them — so they are shown, not folded.
  for (const n of ((s.locale && s.locale.notes) || [])) {
    h += '<div class="diag-warn-line">' + escapeHtml(n) + '</div>';
  }

  h += '<details class="diag-details"><summary>Paths</summary>' +
       '<table class="diag-table"><tbody>';
  for (const p of (s.paths || [])) {
    const flags = [];
    if (!p.exists) flags.push(p.error || 'missing');
    else if (!p.writable) flags.push('NOT writable');
    if (p.non_ascii) flags.push('non-ASCII');
    if (!p.valid_utf8) flags.push('invalid UTF-8');
    if (p.volume_type) flags.push(p.volume + ' is ' + p.volume_type);
    h += '<tr class="' + (!p.exists || !p.valid_utf8 ? 'diag-row-bad' : '') + '">' +
         '<td>' + escapeHtml(p.label) + '</td>' +
         '<td class="diag-detail"><code>' + escapeHtml(p.raw) + '</code>' +
         (p.bytes ? '<br><span class="diag-url">bytes: ' + escapeHtml(p.bytes) + '</span>' : '') +
         '</td><td class="diag-detail">' + escapeHtml(flags.join(', ')) + '</td></tr>';
  }
  h += '</tbody></table></details></div>';
  return h;
}

/* --- saved data ------------------------------------------------- */
function _diagData(r) {
  const d = r.data;
  if (!d) return '';
  let h = '<div class="diag-section"><div class="diag-section-label">Saved data</div>';

  h += '<table class="diag-table"><thead><tr><th>file</th><th>threads</th>' +
       '<th>messages</th><th>size</th></tr></thead><tbody>';
  for (const f of (d.files || [])) {
    h += '<tr><td><code>' + escapeHtml(f.name) + '</code> <span class="diag-url">' +
         escapeHtml(f.role) + '</span></td>' +
         (f.exists
           ? '<td>' + f.threads + '</td><td>' + f.messages + '</td><td>' + _diagBytes(f.size) + '</td>'
           : '<td colspan="3" class="diag-detail">absent</td>') +
         '</tr>';
  }
  h += '</tbody></table>';

  for (const f of (d.files || [])) {
    if (f.error) h += '<div class="diag-error-line"><code>' + escapeHtml(f.name) +
                      '</code>: ' + escapeHtml(f.error) + '</div>';
  }
  for (const finding of (d.findings || [])) {
    h += '<div class="diag-warn-line">' + escapeHtml(finding) + '</div>';
  }

  h += '</div>';
  return h;
}

function _diagNotes(r) {
  if (!r.notes || !r.notes.length) return '';
  let h = '<div class="diag-section"><div class="diag-section-label">Not collected</div>';
  for (const n of r.notes) h += '<div class="diag-hint">' + escapeHtml(n) + '</div>';
  return h + '</div>';
}

/* ================================================================
   COPY

   The report is fetched as JSON and rendered above; this rebuilds it
   as markdown for pasting. The server already knows how to do that
   (Report.Markdown in Go) but sending both shapes would double the
   payload for a button most people never press, so the markdown is
   assembled here from the same object the panel is showing -- which
   also guarantees the paste matches what the user is looking at.
================================================================ */
function diagnosticsMarkdown(r) {
  if (!r) return '';
  const L = [];
  const b = r.build || {};

  L.push('## GobboNet debug report', '');
  L.push('| | |', '|---|---|');
  L.push('| version | `' + (b.version || '—') + '` |');
  L.push('| build | ' + (b.origin || '') + ' — ' + (b.origin_detail || '') + ' |');
  if (b.stamp_mismatch) L.push('| ⚠ stamp | ' + b.stamp_mismatch + ' |');
  L.push('| platform | ' + (b.os || '') + '/' + (b.arch || '') + ' |');
  if (r.system && r.system.os_version) L.push('| os | ' + r.system.os_version + ' |');
  if (r.config) L.push('| mode | ' + r.config.mode + ' |');
  if (r.network) L.push('| upstream | ' + _mdCell(r.network.verdict) + ' |');
  L.push('| auth | required=' + r.auth.required + ' · secret=' + r.auth.secret_format +
         ' · session=' + r.auth.session + ' |');
  L.push('| generated | ' + r.generated + ' |');
  L.push('');

  if (r.system && r.system.locale && r.system.locale.notes) {
    for (const n of r.system.locale.notes) L.push('> ⚠ ' + n);
    if (r.system.locale.notes.length) L.push('');
  }

  if (r.network) {
    L.push('### Connectivity', '');
    L.push('| hop | status | ms | detail |', '|---|---|---|---|');
    for (const h of (r.network.hops || [])) {
      L.push('| ' + h.name + ' | ' + (h.status || '—') + ' | ' + (h.latency_ms || 0) +
             ' | ' + _mdCell(h.error || h.note || h.body || '—') + ' |');
    }
    L.push('');
    const tc = r.network.test_completion;
    if (tc) {
      L.push('**Live test message:** ' + (tc.ok
        ? 'succeeded in ' + tc.latency_ms + ' ms (model `' + (tc.model || '?') + '`)'
        : 'FAILED after ' + tc.latency_ms + ' ms, status ' + (tc.status || '—')));
      if (tc.error) L.push('', '```', tc.error, '```');
      L.push('');
    }
  }

  const rt = r.runtime;
  if (rt) {
    L.push('### Server', '');
    L.push('- pid ' + rt.pid + ' · mode ' + rt.mode + ' · hot-swap ' + rt.hotswap +
           ' · upstream_ok ' + rt.upstream_ok + (rt.uptime ? ' · up ' + rt.uptime : ''));
    if (rt.listener) {
      L.push('- listening on ' + rt.listener.host + ':' + rt.listener.port +
             ' · LAN reachable ' + rt.listener.lan_reachable);
      if (rt.listener.fell_back) L.push('- ⚠ wide bind refused: ' + rt.listener.bind_error);
    }
    if (rt.supervisor) {
      L.push('- llama.cpp phase `' + rt.supervisor.phase + '`' +
             (rt.supervisor.model ? ' · model `' + rt.supervisor.model + '`' : ''));
      if (rt.supervisor.last_error) L.push('', '```', rt.supervisor.last_error, '```');
      if (rt.supervisor.stderr) {
        L.push('', '<details><summary>llama.cpp output</summary>', '',
               '```', rt.supervisor.stderr, '```', '', '</details>');
      }
    }
    if (rt.jobs && rt.jobs.length) {
      L.push('', '| when | status | bytes | error |', '|---|---|---|---|');
      for (const j of rt.jobs) {
        L.push('| ' + _diagTime(j.started_at) + ' | ' + j.status + ' | ' + j.bytes +
               ' | ' + _mdCell(j.error || '—') + ' |');
      }
    }
    L.push('');
  }

  if (r.config) {
    L.push('### Configuration', '');
    L.push('- `' + r.config.path + '` · mode **' + r.config.mode + '**');
    for (const d of (r.config.deferred || [])) L.push('- ⚠ ' + d);
    L.push('', '**Changed from defaults**', '');
    const changed = (r.config.entries || []).filter(e => e.changed);
    if (!changed.length) {
      L.push('_none — stock configuration._');
    } else {
      L.push('| key | value | default |', '|---|---|---|');
      for (const e of changed) {
        L.push('| `' + e.key + '` | ' + _mdCell(e.value) + ' | ' + _mdCell(e.default || '—') + ' |');
      }
    }
    L.push('');
  }

  const s = r.system;
  if (s) {
    L.push('### System', '');
    L.push('- ' + (s.os_version || s.os) + ' · ' + s.num_cpu + ' CPU · ' +
           _diagBytes(s.total_ram_bytes) + ' RAM · user `' + (s.user || '?') + '`' +
           (s.elevated ? ' (elevated)' : ''));
    if (s.locale) {
      L.push('- locale: ' + (s.locale.effective ||
             ('ANSI CP ' + s.locale.ansi_code_page + ', console CP ' +
              s.locale.console_output_code_page + ', ' + (s.locale.user_locale || ''))));
    }
    L.push('', '| what | path | state |', '|---|---|---|');
    for (const p of (s.paths || [])) {
      const flags = [];
      if (!p.exists) flags.push(p.error || 'missing');
      else if (!p.writable) flags.push('NOT writable');
      if (p.non_ascii) flags.push('non-ASCII bytes: ' + p.bytes);
      if (!p.valid_utf8) flags.push('INVALID UTF-8');
      if (p.volume_type) flags.push(p.volume + ' is ' + p.volume_type);
      L.push('| ' + p.label + ' | `' + p.raw + '` | ' + _mdCell(flags.join(', ') || 'ok') + ' |');
    }
    L.push('');
  }

  if (r.data) {
    L.push('### Saved data', '');
    L.push('| file | role | threads | messages | size |', '|---|---|---|---|---|');
    for (const f of (r.data.files || [])) {
      L.push('| `' + f.name + '` | ' + f.role + ' | ' + (f.exists ? f.threads : '—') +
             ' | ' + (f.exists ? f.messages : '—') + ' | ' +
             (f.exists ? _diagBytes(f.size) : 'absent') + ' |');
    }
    for (const f of (r.data.files || [])) {
      if (f.error) L.push('- ⚠ `' + f.name + '`: ' + f.error);
    }
    for (const finding of (r.data.findings || [])) L.push('- ⚠ ' + finding);
    L.push('');
  }

  if (r.notes && r.notes.length) {
    L.push('### Not collected', '');
    for (const n of r.notes) L.push('- ' + n);
    L.push('');
  }

  L.push('<sub>GobboNet diagnostics · schema ' + r.schema + '</sub>');
  return L.join('\n');
}

/* Neutralise the two characters that would break a markdown table. */
function _mdCell(s) {
  return String(s == null ? '' : s)
    .replace(/\|/g, '\\|')
    .replace(/\r?\n/g, '<br>');
}

async function copyDiagnostics(btn) {
  if (!_diagReport) return;
  const text = diagnosticsMarkdown(_diagReport);
  const original = btn.textContent;

  try {
    await navigator.clipboard.writeText(text);
    btn.textContent = 'COPIED';
  } catch (e) {
    // Clipboard access is blocked on a plain-HTTP origin in some browsers,
    // which is exactly how this app is served. Falling back to a selectable
    // textarea keeps the button honest rather than silently doing nothing.
    const box = document.getElementById('diag-copy-fallback');
    if (box) {
      box.value = text;
      box.style.display = 'block';
      box.select();
      btn.textContent = 'SELECT & COPY ↓';
    } else {
      btn.textContent = 'COPY FAILED';
    }
  }
  setTimeout(() => { btn.textContent = original; }, 2500);
}
