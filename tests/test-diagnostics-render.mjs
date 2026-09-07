/* The connection diagnostics modal — js/24-diagnostics.js.
 *
 * The panel exists to be pasted into a bug report, so two properties matter
 * more than anything it looks like:
 *
 *   1. Nothing from the report can become markup. Every string in it comes off
 *      the wire — llama.cpp error bodies, filenames, config values — and the
 *      renderer builds HTML by concatenation. One unescaped field and a model
 *      filename becomes script.
 *
 *   2. A missing section is stated, never blank. The whole failure mode this
 *      feature replaces is a UI that reduced everything to one word and left
 *      the user to guess; a panel that renders empty on error would be the
 *      same bug wearing a bigger box.
 *
 * Runs against the real file, with the same globals chat.html gives it.
 *
 *   node tests/test-diagnostics-render.mjs
 */
import fs from 'fs';
import vm from 'vm';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url)).replace(/\/$/, '');
const SRC = fs.readFileSync(ROOT + '/js/24-diagnostics.js', 'utf8');
const CHAT_HTML = fs.readFileSync(ROOT + '/chat.html', 'utf8');
const CSS = fs.readFileSync(ROOT + '/css/18-diagnostics.css', 'utf8');

let pass = 0, fail = 0;
const ok = (cond, label) => {
  if (cond) { pass++; console.log('  \u2713 ' + label); }
  else { fail++; console.log('  \u2717 ' + label); }
};

/* ----------------------------------------------------------------
   Harness: the globals 24-diagnostics.js expects from its siblings.
---------------------------------------------------------------- */
function makeSandbox(opts = {}) {
  const els = {};
  const el = id => (els[id] = els[id] || {
    id, innerHTML: '', value: '', textContent: '',
    style: {}, classList: { add() {}, remove() {} }, select() {},
  });

  const sandbox = {
    // js/01-config.js
    IS_SERVED: opts.served !== false,
    // js/18-utils.js — the real one, matching its behaviour.
    escapeHtml: s => String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;'),
    document: { getElementById: el },
    console: { log() {}, error() {} },
    setTimeout: () => {},
    navigator: { clipboard: { writeText: async () => {} } },
    fetch: opts.fetch || (async () => { throw new TypeError('Failed to fetch'); }),
  };
  vm.createContext(sandbox);
  vm.runInContext(SRC, sandbox);
  return { sandbox, els, body: () => el('diag-body').innerHTML };
}

/* A full-tier report with every section populated. */
const REPORT = {
  schema: 1, tier: 'full', generated: '2026-01-01T00:00:00Z',
  build: {
    version: '1.7.3-go-abc1234', origin: 'release',
    origin_detail: 'stamped by the release script', os: 'windows', arch: 'amd64',
  },
  auth: { required: true, secret_format: 'argon2id', session: 'valid', login: '/login' },
  network: {
    verdict: 'upstream unreachable: connection refused',
    hops: [
      { name: 'llm /health', url: 'http://127.0.0.1:8080/health', latency_ms: 3,
        error: 'dial tcp: connection refused' },
      { name: 'llm /v1/models', url: 'http://127.0.0.1:8080/v1/models', status: 200,
        latency_ms: 5, note: '1 model(s) offered: gemma' },
    ],
    test_completion: { requested: true, ok: false, status: 500, latency_ms: 12,
      error: 'upstream exploded' },
  },
  runtime: {
    pid: 1234, uptime: '3m2s', mode: 'local', hotswap: true, upstream_ok: false,
    listener: { host: '0.0.0.0', port: 9066, lan_reachable: true, fell_back: false,
      addresses: ['http://192.168.0.5:9066'] },
    supervisor: { phase: 'error', model: 'gemma.gguf',
      last_error: 'failed to allocate buffer', stderr: 'line1\nline2' },
    jobs: [{ id: 'abcdef0123', status: 'error', error: 'upstream stopped', bytes: 0,
      started_at: 1767225600 }],
  },
  config: {
    path: 'C:\\GobboNet\\config.toml', exists: true, mode: 'local', deferred: [],
    entries: [
      { key: 'llm_url', value: 'http://127.0.0.1:8080', default: 'http://127.0.0.1:11437', changed: true },
      { key: 'ctx_size', value: '16384', default: '16384', changed: false },
    ],
  },
  system: {
    os: 'windows', os_version: 'Windows 10.0 build 19045', num_cpu: 8,
    total_ram_bytes: 17179869184, user: 'yuki', elevated: false,
    locale: { ansi_code_page: 932, console_output_code_page: 932, user_locale: 'ja-JP',
      notes: ['code page 932 (Shift-JIS): byte 0x5C is the ASCII backslash but renders as ¥.'] },
    paths: [
      { label: 'model_dir', raw: 'F:\\GobboNet\\models', exists: false,
        error: 'does not exist', valid_utf8: true, non_ascii: false,
        volume: 'F:', volume_type: 'network' },
    ],
    home_redacted: false,
  },
  data: {
    data_dir: 'C:\\GobboNet', schema_version_supported: false,
    files: [
      { name: 'state.json', role: 'live', exists: true, threads: 0, messages: 0, size: 2200 },
      { name: '.gobbonet-state.json', role: 'legacy', exists: true, threads: 1, messages: 47, size: 221700 },
    ],
    findings: ['a legacy state file holds more history than the live one (47 messages vs 0).'],
  },
  notes: ['network: not collected — probes were disabled.'],
};

console.log('\nRENDER');
{
  const { sandbox, body } = makeSandbox();
  sandbox.renderDiagnostics(REPORT);
  const html = body();

  const labels = [...html.matchAll(/diag-section-label">([^<]+)/g)].map(m => m[1]);
  // "Not collected" is last because REPORT carries a note; that section is the
  // explicit-absence rule doing its job, not an extra.
  ok(labels.join(',') === 'Summary,Connection,Server,Configuration,System,Saved data,Not collected',
    'renders every section in order (got: ' + labels.join(', ') + ')');

  ok(html.includes('failed to allocate buffer'),
    "surfaces llama.cpp's own error — the field nothing used to show");
  ok(html.includes('47'), 'shows the stranded legacy message count');
  ok(html.includes('code page 932'), 'shows the locale note that explains ¥ path separators');
  ok(html.includes('F: is network'),
    'names a mapped network drive, which explains "cannot find the drive specified"');
  ok(html.includes('GobboNet manages llama.cpp'),
    'says which kind of install this is in words, not a mode string');
  // Changed settings lead; the rest are folded behind a <details> rather than
  // dropped, because "what is everything set to" is still a question a
  // maintainer asks — just not the first one.
  const foldStart = html.indexOf('settings at their defaults');
  ok(html.indexOf('llm_url') < foldStart,
    'a changed setting is shown up front');
  ok(html.indexOf('ctx_size') > foldStart,
    'a setting at its default is folded away, not omitted');
}

console.log('\nESCAPING');
{
  // Every string below arrives from the network. If any reaches innerHTML raw,
  // a model filename or an upstream error body is an injection vector.
  const XSS = '<img src=x onerror=alert(1)>';
  const hostile = JSON.parse(JSON.stringify(REPORT));
  hostile.network.verdict = XSS;
  hostile.network.hops[0].error = XSS;
  hostile.network.hops[0].url = XSS;
  hostile.runtime.supervisor.last_error = XSS;
  hostile.runtime.supervisor.stderr = XSS;
  hostile.runtime.jobs[0].error = XSS;
  hostile.config.path = XSS;
  hostile.config.entries[0].value = XSS;
  hostile.system.paths[0].raw = XSS;
  hostile.system.locale.notes[0] = XSS;
  hostile.data.files[0].name = XSS;
  hostile.data.findings[0] = XSS;
  hostile.notes[0] = XSS;
  hostile.build.version = XSS;

  const { sandbox, body } = makeSandbox();
  sandbox.renderDiagnostics(hostile);
  const html = body();

  ok(!html.includes('<img'), 'no report field can introduce a tag');
  ok(!html.includes(XSS), 'the hostile string never appears unescaped');
  ok(html.includes('&lt;img'), 'hostile content is still shown, escaped');

  // "onerror=" as a substring is not a finding — it appears inside the escaped
  // text above, which is exactly what should happen. The finding would be an
  // on* ATTRIBUTE on a tag, so scan tag openings rather than the whole string.
  // The renderer emits one handler of its own, and only that one is allowed.
  const OWN_HANDLERS = /^onclick="refreshDiagnostics\(true\)"$/;
  const smuggled = [...html.matchAll(/<[a-zA-Z][^>]*>/g)]
    .flatMap(m => [...m[0].matchAll(/\son[a-zA-Z]+="[^"]*"/g)].map(a => a[0].trim()))
    .filter(attr => !OWN_HANDLERS.test(attr));
  ok(smuggled.length === 0,
    'no event-handler attribute reached the DOM (' +
    (smuggled.length ? 'found: ' + smuggled.join(' ') : 'none') + ')');

  const tags = [...new Set([...html.matchAll(/<\/?([a-zA-Z][a-zA-Z0-9]*)/g)].map(m => m[1].toLowerCase()))];
  const allowed = new Set(['div', 'table', 'thead', 'tbody', 'tr', 'th', 'td',
    'b', 'span', 'code', 'pre', 'details', 'summary', 'button', 'br']);
  const unexpected = tags.filter(t => !allowed.has(t));
  ok(unexpected.length === 0, 'only the renderer\'s own tags appear (' +
    (unexpected.length ? 'found: ' + unexpected.join(' ') : 'none') + ')');
}

console.log('\nDEGRADED STATES');
{
  // file:// — there is no server to ask, and that is a different problem from
  // a server that is down. It has its own fix, so it gets its own message.
  const { sandbox, body } = makeSandbox({ served: false });
  await sandbox.refreshDiagnostics();
  ok(body().includes('opened directly from disk'), 'file:// is named, not silently empty');
  ok(body().length > 0, 'never renders a blank panel');
}
{
  // The server stopped between the tab loading and the modal opening.
  const { sandbox, body } = makeSandbox({
    fetch: async () => { throw new TypeError('Failed to fetch'); },
  });
  await sandbox.refreshDiagnostics();
  ok(body().includes('Could not reach'), 'an unreachable server is reported, not swallowed');
}
{
  // An older server without the endpoint.
  const { sandbox, body } = makeSandbox({
    fetch: async () => ({ ok: false, status: 404 }),
  });
  await sandbox.refreshDiagnostics();
  ok(body().includes('predates the diagnostics endpoint'),
    '404 is explained as an out-of-date server, with the CLI offered instead');
}
{
  // The session died while the tab was open: the endpoint answers, but with
  // the pre-login payload, which would otherwise render as a mostly-empty
  // report and read as "everything is fine".
  const { sandbox, body } = makeSandbox();
  sandbox.renderDiagnostics({
    schema: 1, tier: 'public', generated: '', build: { version: '1.7.3' },
    auth: { required: true, secret_format: 'argon2id', session: 'stale' },
  });
  ok(body().includes('session has ended'), 'a pre-login payload is explained');
  ok(body().includes('restarted with a different password'),
    'a stale cookie names the server-restart case, which is what it usually is');
}

console.log('\nMARKDOWN (the paste)');
{
  const { sandbox } = makeSandbox();
  const md = sandbox.diagnosticsMarkdown(REPORT);

  ok(md.startsWith('## GobboNet debug report'), 'has a heading');
  ok(md.includes('failed to allocate buffer'), 'carries the llama.cpp error');
  ok(md.includes('| llm /health |'), 'carries the hop table');
  ok(md.includes('47'), 'carries the stranded-history finding');
  ok(md.includes('code page 932'), 'carries the locale note');

  // A pipe or newline inside a value would break the table it sits in, and
  // llama.cpp error bodies contain both.
  const nasty = JSON.parse(JSON.stringify(REPORT));
  nasty.network.hops[0].error = 'one | two\nthree';
  const md2 = sandbox.diagnosticsMarkdown(nasty);
  const row = md2.split('\n').find(l => l.startsWith('| llm /health |'));
  ok(row && !row.includes('\n') && row.includes('\\|'),
    'pipes are escaped and newlines folded, so the table survives');
}

console.log('\nWIRING');
{
  ok(/id="status-label"[^>]*role="button"/.test(CHAT_HTML),
    'the status label is exposed as a button');
  ok(/id="status-label"[^>]*tabindex="0"/.test(CHAT_HTML),
    'the status label is reachable by keyboard');
  ok(/id="status-label"[^>]*onclick="openDiagnostics\(\)"/.test(CHAT_HTML),
    'clicking the status label opens the report');
  ok(CHAT_HTML.includes('id="diag-modal"'), 'the modal markup exists');
  ok(CHAT_HTML.includes('id="diag-body"'), 'the render target exists');
  ok(CHAT_HTML.includes('id="diag-copy-fallback"'),
    'the clipboard fallback exists — navigator.clipboard is blocked on plain HTTP');

  // Load order is a contract: this module uses escapeHtml (18-utils) and
  // IS_SERVED (01-config), and boot must stay last.
  const order = [...CHAT_HTML.matchAll(/src="js\/([^"]+)"/g)].map(m => m[1]);
  ok(order.indexOf('24-diagnostics.js') > order.indexOf('18-utils.js'),
    'loads after 18-utils.js, which defines escapeHtml');
  ok(order[order.length - 1] === '25-boot.js', 'boot is still last');

  ok(CSS.includes('.status-label') && CSS.includes('cursor: pointer'),
    'the label advertises that it is clickable');
  ok(CSS.includes('focus-visible'), 'the label has a visible keyboard focus state');
}

console.log(`\n${pass} passed, ${fail} failed\n`);
process.exit(fail === 0 ? 0 : 1);
