// web/static/app.js
//
// Shared client helpers for timemon's phone-shell pages. Moved here from
// the per-template <script> blocks that used to duplicate them (monitor /
// ranking / archive migrated so far; register / setup use the fetch and
// DOM-builder helpers only, since they have no avatars or SSE stream).
// Where templates had slightly different implementations, the version below
// is the one implementation that covers every current call site - see
// CLAUDE.md / the dedup task notes for which differences were reconciled
// and why. Loaded synchronously (no `defer`) because inline page <script>
// blocks reference these functions as soon as they run.
//
// IMPORTANT: bump the ?v= query param on every <script src="/static/app.js?v=..">
// tag whenever this file's contents change, so browsers don't keep serving a
// stale cached copy.
'use strict';

/* ---------- tiny DOM builder (no innerHTML anywhere) ---------- */
function h(tag, attrs, ...children){
  const e = document.createElement(tag);
  if (attrs) for (const k in attrs){
    if (k === 'class') e.className = attrs[k];
    else if (k === 'style') e.setAttribute('style', attrs[k]);
    else if (k.indexOf('on') === 0 && typeof attrs[k] === 'function') e.addEventListener(k.slice(2), attrs[k]);
    else e.setAttribute(k, attrs[k]);
  }
  for (const c of children.flat(Infinity)){
    if (c === null || c === undefined) continue;
    e.appendChild((typeof c === 'string' || typeof c === 'number') ? document.createTextNode(String(c)) : c);
  }
  return e;
}
function clear(el){ while (el.firstChild) el.removeChild(el.firstChild); }

/* ---------- fetch wrapper ----------
   Unifies monitor/ranking/archive's read-only apiGet(), register's separate
   apiGet()+apiPost(), and setup's inline fetch() call into the one api()/
   apiGet() pair already used by mypage.html/admin.html. register.html's old
   apiPost(url, body) and setup.html's inline POST are byte-for-byte the same
   request/error-handling shape as api('POST', url, body), so those two are a
   like-for-like swap. The one observable difference is for monitor/ranking/
   archive: their old apiGet() always threw a generic "HTTP <status>" on
   failure, while this version parses the error body and throws its `.error`
   message when present (matching mypage/admin's existing behavior). Call
   sites only surface that message via toast() or a swallowed catch, so this
   is a minor improvement, not a behavior most users could distinguish. */
async function api(method, url, body){
  const opt = { method, credentials: 'same-origin' };
  if (body !== undefined){
    opt.headers = { 'Content-Type': 'application/json' };
    opt.body = JSON.stringify(body);
  }
  const res = await fetch(url, opt);
  let data = null;
  try { data = await res.json(); } catch (e) {}
  if (!res.ok) throw new Error((data && data.error) || ('HTTP ' + res.status));
  return data;
}
const apiGet = url => api('GET', url);

/* ---------- time formatting ---------- */
function fmt1(ms){ // running clock, 1/10s: m:ss.s
  if (ms < 0) ms = 0;
  const s = ms / 1000;
  return Math.floor(s / 60) + ':' + (s % 60).toFixed(1).padStart(4, '0');
}
// confirmed time, 1/1000s: m:ss.mmm. Null/undefined fallback unified to '—'
// (ranking/archive/mypage/admin's convention); monitor.html previously used
// '—:—' here, but that branch is unreachable in practice (finish/final times
// are only ever formatted once known non-null), so this is a no-op in
// practice - see task report for details.
function fmt3(ms){
  if (ms === null || ms === undefined) return '—';
  const s = Math.floor(ms / 1000), mmm = ((ms % 1000) + 1000) % 1000;
  return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0') + '.' + String(mmm).padStart(3, '0');
}

/* ---------- avatars ---------- */
let iconRev = Date.now();
function avatarInto(el, driverId, name, hasIcon){
  clear(el);
  if (hasIcon){
    const img = h('img', { src: '/api/drivers/' + driverId + '/icon?v=' + iconRev, alt: '' });
    img.addEventListener('error', () => { clear(el); el.textContent = (name || '?').slice(0, 1); });
    el.appendChild(img);
  } else {
    el.textContent = (name || '?').slice(0, 1);
  }
}
function vehicleAvatarInto(el, vehicleId, name, hasIcon){
  clear(el);
  if (hasIcon){
    const img = h('img', { src: '/api/vehicles/' + vehicleId + '/icon?v=' + iconRev, alt: '' });
    img.addEventListener('error', () => { clear(el); el.textContent = (name || '?').slice(0, 1); });
    el.appendChild(img);
  } else {
    el.textContent = (name || '?').slice(0, 1);
  }
}

/* ---------- copy-to-clipboard token/URL boxes ---------- */
function bindCopyable(el){
  el.addEventListener('click', async () => {
    const url = el.querySelector('span').textContent;
    if (!url) return;
    try { await navigator.clipboard.writeText(url); }
    catch (e) {
      const ta = document.createElement('textarea');
      ta.value = url; document.body.appendChild(ta);
      ta.select(); document.execCommand('copy'); ta.remove();
    }
    el.classList.add('copied');
    el.querySelector('.cp').textContent = '✓ コピーしました';
    setTimeout(() => {
      el.classList.remove('copied');
      el.querySelector('.cp').textContent = '📋 タップでコピー';
    }, 1600);
  });
}

/* ---------- modal helpers ----------
   Generic version (matching mypage.html's implementation): just toggles the
   'on' class. admin.html additionally chains a deferred directory-refresh
   through its own closeModal() - that page-specific behavior stays local to
   admin.html when it migrates, rather than living here. Not yet wired up by
   any of the currently-migrated pages (monitor/ranking/archive/register/
   setup have no shared-style modals, or handle backdrop-click inline) - it's
   staged here for the mypage/admin migration. */
function closeModal(id){
  document.getElementById(id).classList.remove('on');
}
function bindModalClose(id){
  const bg = document.getElementById(id);
  bg.addEventListener('click', e => { if (e.target === bg) closeModal(id); });
  bg.querySelector('.modal').addEventListener('click', e => e.stopPropagation());
}
function anyModalOpen(){
  return !!document.querySelector('.modal-bg.on');
}

/* ---------- server time sync + SSE subscription ---------- */
let offset = 0, offsetKnown = false;
function nowMs(){ return Date.now() + offset; }

// openStream(topics, handlers) opens /api/stream for the given topic list and
// wires up one addEventListener per handlers key. When topics includes
// 'time', the offset/offsetKnown clock-sync listener is registered
// automatically - callers never need a 'time' entry in handlers.
function openStream(topics, handlers){
  const es = new EventSource('/api/stream?topics=' + topics.join(','));
  if (topics.includes('time')){
    es.addEventListener('time', e => {
      const d = JSON.parse(e.data);
      offset = d.server_ms - Date.now();
      offsetKnown = true;
    });
  }
  for (const name in (handlers || {})){
    es.addEventListener(name, e => {
      handlers[name](e.data ? JSON.parse(e.data) : undefined, e);
    });
  }
  return es;
}
