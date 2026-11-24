// ChatHub frontend - multi-channel Twitch viewer

const MAX_MESSAGES_PER_CHANNEL = 500;

const channels = new Map();
let activeChannel = null;
let highlights = [];
let soundOnMention = 'none';
let onlyShowLive = false;
let loggedIn = false;
let myUsername = '';
let emoteCache = new Map();
const liveStatus = new Map(); // channel -> bool

// DOM
const tabBar = document.getElementById('tabBar');
const tabAddBtn = document.getElementById('tabAdd');
const chatArea = document.getElementById('chatArea');
const emptyState = document.getElementById('emptyState');
const statusTextEl = document.getElementById('statusText');
const statusOverlay = document.getElementById('statusOverlay');
// setStatus shows a transient toast at the bottom of the chat area.
// Empty text or no overlay = no-op. Auto-hides after 4s unless persist=true.
let statusHideTimer = null;
function setStatus(text, opts) {
  if (!statusOverlay || !statusTextEl) return;
  if (!text) { statusOverlay.classList.add('hidden'); return; }
  statusTextEl.textContent = text;
  statusOverlay.classList.remove('hidden');
  if (statusHideTimer) clearTimeout(statusHideTimer);
  const persist = opts && opts.persist;
  if (!persist) {
    statusHideTimer = setTimeout(() => statusOverlay.classList.add('hidden'), 4000);
  }
}
// Backwards-compatible shim for the existing statusText.textContent = "..."
// pattern scattered across the file.
const statusText = {
  set textContent(v) { setStatus(v); },
  get textContent() { return statusTextEl ? statusTextEl.innerText : ''; },
};
const authStatus = document.getElementById('authStatus');
const settingsBtn = document.getElementById('settingsBtn');
const addModalBg = document.getElementById('addModalBg');
const channelInput = document.getElementById('channelInput');
const addChannelBtn = document.getElementById('addChannelBtn');
const settingsModalBg = document.getElementById('settingsModalBg');
const highlightInput = document.getElementById('highlightInput');
const soundSelect = document.getElementById('soundSelect');
const onlyLiveCheckbox = document.getElementById('onlyLiveCheckbox');
const liveNotifyCheckbox = document.getElementById('liveNotifyCheckbox');
const autostartRow = document.getElementById('autostartRow');
const autostartCheckbox = document.getElementById('autostartCheckbox');
const localeSelect = document.getElementById('localeSelect');
const versionLabelEl = document.getElementById('versionLabel');
const checkUpdateBtn = document.getElementById('checkUpdateBtn');
const updateStatusEl = document.getElementById('updateStatus');
const updateApplyRow = document.getElementById('updateApplyRow');
const applyUpdateBtn = document.getElementById('applyUpdateBtn');
const releaseUrlEl = document.getElementById('releaseUrl');
const updateChannelSelect = document.getElementById('updateChannelSelect');
if (updateChannelSelect) {
  // Hydrate from backend on settings open and persist on change. Empty
  // string from a fresh install collapses to "stable" on the Go side.
  (async () => {
    try {
      const cur = await window.go.main.App.GetUpdateChannel();
      if (cur) updateChannelSelect.value = cur;
    } catch (_) {}
  })();
  updateChannelSelect.addEventListener('change', async () => {
    try {
      await window.go.main.App.SetUpdateChannel(updateChannelSelect.value);
    } catch (_) {}
  });
}
let pendingUpdateUrl = '';
const loginBtn = document.getElementById('loginBtn');
const logoutBtn = document.getElementById('logoutBtn');
const authInfo = document.getElementById('authInfo');
const authHelp = document.getElementById('authHelp');
const authUrl = document.getElementById('authUrl');
const authCode = document.getElementById('authCode');
const inputRow = document.getElementById('inputRow');
const inputPrefix = document.getElementById('inputPrefix');
const msgInput = document.getElementById('msgInput');

// === Helpers ===
// readableColor boosts too-dark hex colors so usernames stay legible on
// the dark chat background — Twitch's "Readable Colors" filter. Uses
// PERCEIVED brightness (YIQ luminance) not raw HSL lightness because
// blue at 50% L has very low perceived luminance (0.114 weight) and
// looks pitch black against #16161c. Iterates L upward keeping hue+sat
// until YIQ hits target 150.
function readableColor(hex) {
  if (!hex || typeof hex !== 'string') return '#ffffff';
  let m = hex.replace('#', '');
  if (m.length === 3) m = m.split('').map(c => c + c).join('');
  if (m.length !== 6) return hex;
  const r0 = parseInt(m.slice(0, 2), 16);
  const g0 = parseInt(m.slice(2, 4), 16);
  const b0 = parseInt(m.slice(4, 6), 16);
  const y0 = 0.299 * r0 + 0.587 * g0 + 0.114 * b0;
  if (y0 >= 150) return hex;
  const r = r0 / 255, g = g0 / 255, b = b0 / 255;
  const max = Math.max(r, g, b), min = Math.min(r, g, b);
  let h = 0, s = 0;
  const l = (max + min) / 2;
  if (max !== min) {
    const d = max - min;
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
    switch (max) {
      case r: h = (g - b) / d + (g < b ? 6 : 0); break;
      case g: h = (b - r) / d + 2; break;
      case b: h = (r - g) / d + 4; break;
    }
    h /= 6;
  }
  const hue2rgb = (p, q, t) => {
    if (t < 0) t += 1; if (t > 1) t -= 1;
    if (t < 1 / 6) return p + (q - p) * 6 * t;
    if (t < 1 / 2) return q;
    if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6;
    return p;
  };
  const toRGB = (nl) => {
    const q = nl < 0.5 ? nl * (1 + s) : nl + s - nl * s;
    const p = 2 * nl - q;
    return [
      Math.round(hue2rgb(p, q, h + 1 / 3) * 255),
      Math.round(hue2rgb(p, q, h) * 255),
      Math.round(hue2rgb(p, q, h - 1 / 3) * 255),
    ];
  };
  let nl = Math.max(l, 0.5);
  let out = toRGB(nl);
  while (nl < 0.95) {
    const y = 0.299 * out[0] + 0.587 * out[1] + 0.114 * out[2];
    if (y >= 150) break;
    nl += 0.05;
    out = toRGB(nl);
  }
  return '#' + out.map(v => v.toString(16).padStart(2, '0')).join('');
}

function el(tag, attrs, ...children) {
  const e = document.createElement(tag);
  if (attrs) {
    for (const k in attrs) {
      if (k === 'class') e.className = attrs[k];
      else if (k === 'style') e.setAttribute('style', attrs[k]);
      else if (k.startsWith('on') && typeof attrs[k] === 'function') e[k] = attrs[k];
      else e.setAttribute(k, attrs[k]);
    }
  }
  for (const c of children) {
    if (c == null || c === false) continue;
    e.appendChild(typeof c === 'string' || typeof c === 'number' ? document.createTextNode(String(c)) : c);
  }
  return e;
}

// 7TV cosmetics indexed by Twitch user_id. Populated from the
// "sevenTVCosmetic" Wails event the backend emits whenever the
// EventAPI WebSocket learns about a paint or badge entitlement.
const sevenTVByUser = new Map();

// Go lookups (emotes/badges) are awaited inside the per-channel render
// chain; if one never answered, every later message of that channel would
// wait forever. withTimeout resolves to `fallback` after `ms` instead.
const LOOKUP_TIMEOUT_MS = 3000;
function withTimeout(promise, ms, fallback) {
  return new Promise((resolve) => {
    const timer = setTimeout(() => resolve(fallback), ms);
    Promise.resolve(promise).then(
      (v) => { clearTimeout(timer); resolve(v); },
      () => { clearTimeout(timer); resolve(fallback); },
    );
  });
}

// === Channel tabs ===
// renderMessage is async (Go lookups per uncached word / badge), so messages
// must be rendered through a per-channel promise chain or they would append
// out of arrival order. channel -> tail of the chain.
const renderQueues = new Map();
function enqueueRender(msg) {
  const prev = renderQueues.get(msg.channel) || Promise.resolve();
  const next = prev.then(() => renderMessage(msg)).catch((e) => console.error('render failed', e));
  renderQueues.set(msg.channel, next);
  return next;
}

async function loadHistoryForChannel(channel) {
  console.log('[history] loading for', channel);
  const state = channels.get(channel);
  // Gate the channel's queue synchronously: live messages arriving while
  // history is still loading wait behind it, so history renders first and
  // live messages land below the "new messages" separator.
  let release;
  renderQueues.set(channel, new Promise((resolve) => { release = resolve; }));
  try {
    const history = await window.go.main.App.GetHistory(channel, 200);
    console.log('[history] got', history ? history.length : 0, 'messages for', channel);
    // Tab may have been closed (or closed and re-added) while loading.
    if (history && history.length > 0 && state && channels.get(channel) === state) {
      state.chatViewEl.appendChild(el('div', { class: 'msg history-sep' }, '── ' + i18n.t('earlierMessages') + ' ──'));
      for (const msg of history) {
        if (channels.get(channel) !== state) break;
        try { await renderMessage(msg); } catch (e) { console.error('history render failed', e); }
      }
      if (channels.get(channel) === state) {
        state.chatViewEl.appendChild(el('div', { class: 'msg history-sep' }, '── ' + i18n.t('newMessages') + ' ──'));
        state.chatViewEl.scrollTop = state.chatViewEl.scrollHeight;
      }
    }
  } catch (e) {
    console.error('history load failed', e);
  } finally {
    release();
  }
}

// === Tab drag-reorder ===
let draggingChannel = null;

function persistTabOrder() {
  const order = Array.from(tabBar.querySelectorAll('.tab'))
    .map(el => el.dataset.channel)
    .filter(Boolean);
  try { window.go.main.App.SetChannelOrder(order); } catch (e) { console.error('persistTabOrder', e); }
}

function setupTabDrag(tab, channel) {
  tab.addEventListener('dragstart', (e) => {
    hideTabPreview(true);
    draggingChannel = channel;
    tab.classList.add('dragging');
    // Required for Firefox to start the drag
    e.dataTransfer.effectAllowed = 'move';
    try { e.dataTransfer.setData('text/plain', channel); } catch (_) {}
  });
  tab.addEventListener('dragend', () => {
    tab.classList.remove('dragging');
    document.querySelectorAll('.tab.drag-over').forEach(el => el.classList.remove('drag-over'));
    draggingChannel = null;
    persistTabOrder();
  });
  tab.addEventListener('dragover', (e) => {
    if (!draggingChannel || draggingChannel === channel) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    tab.classList.add('drag-over');
  });
  tab.addEventListener('dragleave', () => {
    tab.classList.remove('drag-over');
  });
  tab.addEventListener('drop', (e) => {
    e.preventDefault();
    tab.classList.remove('drag-over');
    if (!draggingChannel || draggingChannel === channel) return;
    const srcState = channels.get(draggingChannel);
    if (!srcState) return;
    // Decide insert before or after based on horizontal midpoint
    const rect = tab.getBoundingClientRect();
    const before = e.clientX < rect.left + rect.width / 2;
    if (before) {
      tabBar.insertBefore(srcState.tabEl, tab);
    } else {
      tabBar.insertBefore(srcState.tabEl, tab.nextSibling);
    }
  });
}

// Parse a stored channel key like "yt:@DEmiwitv" or plain "miwitv" into
// { platform, displayName } so the tab/UI can render the right pill and
// strip the prefix. Matches the Go-side parseChannelRef semantics.
function parseChannelKey(key) {
  const i = (key || '').indexOf(':');
  if (i > 0 && i < 6) {
    const prefix = key.slice(0, i).toLowerCase();
    if (prefix === 'yt' || prefix === 'youtube') {
      return { platform: 'youtube', displayName: key.slice(i + 1) };
    }
    if (prefix === 'tw' || prefix === 'twitch') {
      return { platform: 'twitch', displayName: key.slice(i + 1) };
    }
  }
  return { platform: 'twitch', displayName: key };
}

// === Chat search (Ctrl+F) ===
// Filters the visible chat in place: non-matching rows are hidden, the
// match count is shown, Esc restores everything. New messages arriving
// while the search is open are filtered too.
// Syntax (space-separated, all terms must match, "!" negates a term):
//   free text             message text contains it (case-insensitive)
//   from:<user>           sent by that user
//   has:link|emote|gif    contains a link / emote / GIF
//   is:first-msg|returning|deleted|highlighted|redemption|sub|raid|
//      mention|watchstreak|system
const SEARCH_DEBOUNCE_MS = 120;
const searchState = { open: false, terms: [], timer: null, container: null };
const searchInputEl = el('input', { class: 'chat-search-input', type: 'text', spellcheck: 'false', autocomplete: 'off' });
const searchCountEl = el('span', { class: 'chat-search-count' }, '');
const searchCloseEl = el('span', { class: 'chat-search-close', title: 'Esc' }, '✕');
const searchBarEl = el('div', { class: 'chat-search hidden' },
  el('span', { class: 'chat-search-icon' }, '🔍'), searchInputEl, searchCountEl, searchCloseEl);

function renderSearchPlaceholder() { searchInputEl.placeholder = i18n.t('searchPlaceholder'); }
renderSearchPlaceholder();
if (i18n.onLocaleChange) i18n.onLocaleChange(() => { renderSearchPlaceholder(); if (searchState.open) applySearch(); });

function parseSearchQuery(q) {
  const terms = [];
  for (let tok of q.trim().split(/\s+/)) {
    if (!tok) continue;
    let neg = false;
    if (tok.length > 1 && tok[0] === '!') { neg = true; tok = tok.slice(1); }
    const m = /^(from|has|is):(.+)$/i.exec(tok);
    if (m) terms.push({ neg, kind: m[1].toLowerCase(), value: m[2].toLowerCase().replace(/^@/, '') });
    else terms.push({ neg, kind: 'text', value: tok.toLowerCase() });
  }
  return terms;
}

function searchTermMatches(row, t) {
  const text = row.textContent.toLowerCase();
  const type = row.dataset.type || '';
  switch (t.kind) {
    case 'from': return (row.dataset.user || '') === t.value;
    case 'has':
      if (t.value === 'link') return !!row.querySelector('a') || /https?:\/\/|www\./i.test(text);
      if (t.value === 'emote') return !!row.querySelector('img.emote');
      if (t.value === 'gif') return !!row.querySelector('img.chat-gif');
      return false;
    case 'is':
      switch (t.value) {
        case 'first-msg': case 'first': return row.classList.contains('first-msg');
        case 'returning': return row.classList.contains('returning-chatter');
        case 'deleted': return row.classList.contains('deleted');
        case 'highlighted': return row.classList.contains('highlighted-msg');
        case 'redemption': case 'reward': return type === 'redemption' || row.classList.contains('reward-msg');
        case 'sub': return type === 'sub' || type === 'giftsub';
        case 'raid': return type === 'raid';
        case 'watchstreak': case 'streak': return type === 'watchstreak';
        case 'system': return type === 'system' || type === 'join' || type === 'part' || !row.dataset.user;
        case 'mention': return searchIsMention(row, text);
        case 'reply': return !!row.querySelector('.reply-context');
        case 'powerup': return row.classList.contains('powerup-gigantify') || row.classList.contains('powerup-effect');
        default: return false;
      }
    default: return text.includes(t.value);
  }
}

function searchRowMatches(row) {
  for (const t of searchState.terms) {
    if (searchTermMatches(row, t) === t.neg) return false;
  }
  return true;
}

// Re-filters the whole current container (or clears the filter).
function applySearch() {
  const container = searchContainer();
  if (searchState.container && searchState.container !== container) clearSearchIn(searchState.container);
  searchState.container = container;
  if (!container) return;
  if (!searchState.open || searchState.terms.length === 0) {
    clearSearchIn(container);
    searchCountEl.textContent = '';
    return;
  }
  let n = 0;
  for (const row of container.querySelectorAll('.msg')) {
    const ok = searchRowMatches(row);
    row.classList.toggle('search-hidden', !ok);
    if (ok) n++;
  }
  searchCountEl.textContent = i18n.t('searchCount', { n });
  container.scrollTop = container.scrollHeight;
}

function clearSearchIn(container) {
  for (const row of container.querySelectorAll('.msg.search-hidden')) row.classList.remove('search-hidden');
}

// Called for every newly appended row.
function applySearchToRow(row, container) {
  if (!searchState.open || searchState.terms.length === 0 || container !== searchState.container) return;
  const ok = searchRowMatches(row);
  row.classList.toggle('search-hidden', !ok);
  if (ok) {
    const n = container.querySelectorAll('.msg:not(.search-hidden)').length;
    searchCountEl.textContent = i18n.t('searchCount', { n });
  }
}

function openSearch() {
  searchState.open = true;
  searchBarEl.classList.remove('hidden');
  searchInputEl.focus();
  searchInputEl.select();
  applySearch();
}

function closeSearch() {
  searchState.open = false;
  searchState.terms = [];
  searchInputEl.value = '';
  searchBarEl.classList.add('hidden');
  if (searchState.container) clearSearchIn(searchState.container);
  searchCountEl.textContent = '';
}

searchInputEl.addEventListener('input', () => {
  clearTimeout(searchState.timer);
  searchState.timer = setTimeout(() => {
    searchState.terms = parseSearchQuery(searchInputEl.value);
    applySearch();
  }, SEARCH_DEBOUNCE_MS);
});
searchInputEl.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeSearch(); }
});
searchCloseEl.addEventListener('click', closeSearch);
document.addEventListener('keydown', (e) => {
  if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'f') {
    e.preventDefault();
    openSearch();
  }
});

// chathub: per-channel views; the bar floats over the active one.
chatArea.appendChild(searchBarEl);
function searchContainer() {
  const st = activeChannel && channels.get(activeChannel);
  return st ? st.chatViewEl : null;
}
function searchIsMention(row) { return row.classList.contains('highlight'); }

// Gigantify an Emote: Twitch enlarges the LAST emote of the message.
// Use the largest CDN scale for it (3.0 = 112px source) so it stays sharp.
function applyGigantify(div, msg) {
  if (msg.powerUp !== 'gigantify') return;
  const emotes = div.querySelectorAll('img.emote');
  const last = emotes[emotes.length - 1];
  if (!last) return;
  last.classList.add('giant-emote');
  if (last.src.includes('static-cdn.jtvnw.net/emoticons/v2/')) last.src = last.src.replace(/\/2\.0$/, '/3.0');
}

// === Reply threads ===
// Messages that answer another message get a "↪ @user: original" line
// (click jumps to the original if it's still in the chat). Hovering a
// Twitch message shows a ↩ button; the next message is then sent as a
// reply (chip above the input, ✕ or Esc cancels).
let replyTarget = null; // { id, user, text, channel }
const replyChipEl = el('div', { class: 'reply-chip hidden' });
msgInput.closest('.input-row').parentElement.insertBefore(replyChipEl, msgInput.closest('.input-row'));

function setReplyTarget(t) { replyTarget = t; renderReplyChip(); msgInput.focus(); }
function clearReplyTarget() { replyTarget = null; renderReplyChip(); }
function renderReplyChip() {
  if (!replyTarget) { replyChipEl.classList.add('hidden'); replyChipEl.replaceChildren(); return; }
  const x = el('span', { class: 'reply-chip-close', title: 'Esc' }, '✕');
  x.addEventListener('click', clearReplyTarget);
  replyChipEl.replaceChildren(
    el('span', { class: 'reply-chip-label' }, '↩ ' + i18n.t('replyingTo', { user: replyTarget.user })),
    el('span', { class: 'reply-chip-text' }, replyTarget.text), x);
  replyChipEl.classList.remove('hidden');
}
if (i18n.onLocaleChange) i18n.onLocaleChange(renderReplyChip);

// Context line on a reply. Inserted first, so marker labels (inserted
// later at the top) end up above it.
function addReplyContext(div, msg) {
  if (!msg.replyParentUser && !msg.replyParentId) return;
  const body = msg.replyParentBody || '';
  const ctx = el('div', { class: 'reply-context', title: body },
    '↪ @' + (msg.replyParentUser || '?') + (body ? ': ' + body : ''));
  if (msg.replyParentId) ctx.addEventListener('click', () => jumpToMessage(div.parentElement, msg.replyParentId));
  div.insertBefore(ctx, div.firstChild);
}

// ↩ button at the end of a Twitch chat message.
function addReplyButton(div, msg) {
  // Older payloads / history lines have no platform field: derive it from
  // the channel key ("yt:@handle" = YouTube, which has no reply threads).
  const platform = msg.platform || (msg.channel ? parseChannelKey(msg.channel).platform : '');
  if (platform !== 'twitch' || !msg.id || msg.historical) return;
  const b = el('span', { class: 'reply-btn', title: i18n.t('reply') }, '↩');
  b.addEventListener('click', (e) => {
    e.stopPropagation();
    setReplyTarget({ id: msg.id, user: msg.displayName || msg.username || '?', text: msg.text || '', channel: msg.channel });
  });
  div.appendChild(b);
}

function jumpToMessage(container, id) {
  if (!container) return;
  for (const row of container.querySelectorAll('.msg')) {
    if (row.dataset.msgId === id) {
      row.scrollIntoView({ block: 'center', behavior: 'smooth' });
      row.classList.add('flash');
      setTimeout(() => row.classList.remove('flash'), 1600);
      return;
    }
  }
}

// === Emote picker + ":name" Tab completion ===
// A 😀 button next to the input opens a searchable grid of the emotes
// usable here (Twitch channel + global, 7TV / BTTV / FFZ channel sets and
// globals; from App.GetEmotePicker). Clicking inserts the name at the
// cursor. Typing ":ke" + Tab completes emote names (Tab again cycles).
const EMOTE_PICKER_CACHE_MS = 5 * 60 * 1000;
const EMOTE_COMPLETE_MIN = 2; // characters after ":" before Tab completes
const emotePickerCache = new Map(); // key -> { at, sections }
const emotePicker = { open: false, key: null, sections: [], complete: null };

const emotePickerBtn = el('button', { class: 'emote-picker-btn', type: 'button', title: 'Emotes' }, '😀');
msgInput.parentElement.insertBefore(emotePickerBtn, msgInput);
const emoteSearchEl = el('input', { class: 'emote-picker-search', type: 'text', autocomplete: 'off', spellcheck: 'false' });
const emoteGridEl = el('div', { class: 'emote-picker-grid' });
const emotePickerEl = el('div', { class: 'emote-picker hidden' }, emoteSearchEl, emoteGridEl);
document.body.appendChild(emotePickerEl);

async function loadEmotePicker() {
  const key = (activeChannel || '');
  const hit = emotePickerCache.get(key);
  if (hit && Date.now() - hit.at < EMOTE_PICKER_CACHE_MS) return hit.sections;
  let sections = [];
  try { sections = (await window.go.main.App.GetEmotePicker(key)) || []; } catch (e) { console.error('emote picker', e); }
  emotePickerCache.set(key, { at: Date.now(), sections });
  return sections;
}

function emoteSectionTitle(s) {
  const src = { twitch: 'Twitch', '7tv': '7TV', bttv: 'BTTV', ffz: 'FFZ' }[s.source] || s.source;
  return src + ' · ' + (s.scope === 'channel' ? i18n.t('emoteScopeChannel') : i18n.t('emoteScopeGlobal'));
}

function renderEmotePicker() {
  const q = emoteSearchEl.value.trim().toLowerCase();
  const nodes = [];
  for (const s of emotePicker.sections) {
    const list = q ? s.emotes.filter((e) => e.name.toLowerCase().includes(q)) : s.emotes;
    if (!list.length) continue;
    nodes.push(el('div', { class: 'emote-picker-title' }, emoteSectionTitle(s) + ' (' + list.length + ')'));
    const grid = el('div', { class: 'emote-picker-row' });
    for (const e of list) {
      const b = el('button', { class: 'emote-picker-item', type: 'button', title: e.name },
        el('img', { src: e.url, alt: e.name, loading: 'lazy' }));
      b.addEventListener('click', () => insertEmoteName(e.name));
      grid.appendChild(b);
    }
    nodes.push(grid);
  }
  if (!nodes.length) {
    nodes.push(el('div', { class: 'emote-picker-empty' }, emotePicker.sections.length ? i18n.t('emoteNoMatch') : i18n.t('emotePickerEmpty')));
  }
  emoteGridEl.replaceChildren(...nodes);
}

async function openEmotePicker() {
  emotePicker.open = true;
  emoteSearchEl.placeholder = i18n.t('emoteSearch');
  emoteSearchEl.value = '';
  emoteGridEl.replaceChildren(el('div', { class: 'emote-picker-empty' }, i18n.t('tabPreviewLoading')));
  positionEmotePicker();
  emotePickerEl.classList.remove('hidden');
  emoteSearchEl.focus();
  emotePicker.key = (activeChannel || '');
  emotePicker.sections = await loadEmotePicker();
  if (emotePicker.open) renderEmotePicker();
}
function closeEmotePicker() {
  emotePicker.open = false;
  emotePickerEl.classList.add('hidden');
}
function positionEmotePicker() {
  const r = msgInput.closest('.input-row').getBoundingClientRect();
  emotePickerEl.style.bottom = (window.innerHeight - r.top + 6) + 'px';
  emotePickerEl.style.left = Math.max(8, r.left) + 'px';
}

function insertEmoteName(name) {
  const input = msgInput;
  const start = input.selectionStart ?? input.value.length;
  const end = input.selectionEnd ?? start;
  const before = input.value.slice(0, start);
  const after = input.value.slice(end);
  const pre = before && !/\s$/.test(before) ? ' ' : '';
  const post = after.startsWith(' ') ? '' : ' ';
  input.value = before + pre + name + post + after;
  const caret = (before + pre + name + post).length;
  input.focus();
  input.setSelectionRange(caret, caret);
}

emotePickerBtn.addEventListener('click', (e) => {
  e.stopPropagation();
  if (emotePicker.open) closeEmotePicker(); else openEmotePicker();
});
emoteSearchEl.addEventListener('input', renderEmotePicker);
emoteSearchEl.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeEmotePicker(); msgInput.focus(); }
});
document.addEventListener('mousedown', (e) => {
  if (emotePicker.open && !emotePickerEl.contains(e.target) && e.target !== emotePickerBtn) closeEmotePicker();
});
window.addEventListener('resize', () => { if (emotePicker.open) positionEmotePicker(); });

// ":name" + Tab completion. Runs after the app's own Tab handlers and only
// acts if none of them handled the key (defaultPrevented).
msgInput.addEventListener('keydown', async (e) => {
  if (e.key !== 'Tab') { if (e.key !== 'Shift') emotePicker.complete = null; return; }
  if (e.defaultPrevented) return;
  const input = msgInput;
  const st = emotePicker.complete;
  if (st && input.value === st.applied) {
    e.preventDefault();
    st.index = (st.index + (e.shiftKey ? -1 : 1) + st.matches.length) % st.matches.length;
    applyEmoteCompletion(st);
    return;
  }
  const caret = input.selectionStart ?? input.value.length;
  const m = /(^|\s):([^\s:]+)$/.exec(input.value.slice(0, caret));
  if (!m || m[2].length < EMOTE_COMPLETE_MIN) return;
  e.preventDefault();
  const prefix = m[2].toLowerCase();
  const names = [...new Set((await loadEmotePicker()).flatMap((s) => s.emotes.map((x) => x.name)))];
  const starts = names.filter((n) => n.toLowerCase().startsWith(prefix));
  const contains = names.filter((n) => !n.toLowerCase().startsWith(prefix) && n.toLowerCase().includes(prefix));
  const matches = [...starts.sort(), ...contains.sort()];
  if (!matches.length) return;
  emotePicker.complete = { start: caret - m[2].length - 1, end: caret, matches, index: 0, applied: '' };
  applyEmoteCompletion(emotePicker.complete);
});
function applyEmoteCompletion(st) {
  const input = msgInput;
  const pick = st.matches[st.index] + ' ';
  input.value = input.value.slice(0, st.start) + pick + input.value.slice(st.end);
  st.end = st.start + pick.length;
  st.applied = input.value;
  input.setSelectionRange(st.end, st.end);
}

// === Tab hover preview ===
// Hovering a tab shows a card with the stream thumbnail, title, game,
// viewers and uptime plus an "Open in browser" button. Data comes from
// App.GetTabPreview (cached briefly per tab); the URL to open is built by
// the backend (App.OpenChannelInBrowser), the frontend never passes one.
const TAB_PREVIEW_SHOW_DELAY_MS = 350;  // avoid flashing while moving across tabs
const TAB_PREVIEW_HIDE_DELAY_MS = 200;  // time to move the mouse into the card
const TAB_PREVIEW_CACHE_MS = 60000;     // Twitch refreshes thumbnails every few minutes
const tabPreviewCache = new Map();      // channel -> { at, data }
const tabPreview = { channel: null, showTimer: null, hideTimer: null, req: 0 };
const tabPreviewEl = el('div', { class: 'tab-preview hidden' });
document.body.appendChild(tabPreviewEl);
tabPreviewEl.addEventListener('mouseenter', () => clearTimeout(tabPreview.hideTimer));
tabPreviewEl.addEventListener('mouseleave', () => scheduleHideTabPreview());

function attachTabPreview(tab, channel) {
  tab.addEventListener('mouseenter', () => {
    clearTimeout(tabPreview.hideTimer);
    clearTimeout(tabPreview.showTimer);
    if (draggingChannel) return;
    tabPreview.showTimer = setTimeout(() => showTabPreview(tab, channel), TAB_PREVIEW_SHOW_DELAY_MS);
  });
  tab.addEventListener('mouseleave', () => {
    clearTimeout(tabPreview.showTimer);
    scheduleHideTabPreview();
  });
}

function scheduleHideTabPreview() {
  clearTimeout(tabPreview.hideTimer);
  tabPreview.hideTimer = setTimeout(() => hideTabPreview(false), TAB_PREVIEW_HIDE_DELAY_MS);
}

function hideTabPreview(immediate) {
  clearTimeout(tabPreview.showTimer);
  clearTimeout(tabPreview.hideTimer);
  if (immediate || !tabPreviewEl.matches(':hover')) {
    tabPreviewEl.classList.add('hidden');
    tabPreview.channel = null;
  }
}

async function showTabPreview(tab, channel) {
  const req = ++tabPreview.req;
  tabPreview.channel = channel;
  const cached = tabPreviewCache.get(channel);
  if (cached && Date.now() - cached.at < TAB_PREVIEW_CACHE_MS) {
    renderTabPreview(tab, channel, cached.data);
    return;
  }
  renderTabPreview(tab, channel, null); // loading state right away
  let data = null;
  try { data = await window.go.main.App.GetTabPreview(channel); } catch (e) { data = { error: String(e) }; }
  if (data && !data.error) tabPreviewCache.set(channel, { at: Date.now(), data });
  // Ignore stale answers (mouse already on another tab / card closed).
  if (req !== tabPreview.req || tabPreview.channel !== channel) return;
  renderTabPreview(tab, channel, data);
}

function renderTabPreview(tab, channel, data) {
  const meta = parseChannelKey(channel);
  const head = el('div', { class: 'tp-head' },
    el('span', { class: 'platform-pill ' + (meta.platform === 'youtube' ? 'yt' : 'tw') }, meta.platform === 'youtube' ? 'YT' : 'TW'),
    el('span', { class: 'tp-name' }, (meta.platform === 'youtube' ? '' : '#') + meta.displayName));
  const body = [];
  if (!data) {
    body.push(el('div', { class: 'tp-status' }, i18n.t('tabPreviewLoading')));
  } else if (data.live) {
    head.appendChild(el('span', { class: 'tp-live' }, '● LIVE'));
    if (data.thumbnailUrl) {
      body.push(el('div', { class: 'tp-thumb' }, el('img', { src: data.thumbnailUrl, alt: '' })));
    }
    if (data.title) body.push(el('div', { class: 'tp-title', title: data.title }, data.title));
    const facts = [];
    if (data.gameName) facts.push('🎮 ' + data.gameName);
    if (data.viewerCount != null && data.platform !== 'youtube') facts.push('👁 ' + Number(data.viewerCount).toLocaleString());
    if (data.startedAt) facts.push('⏱ ' + formatUptime(data.startedAt));
    if (facts.length) body.push(el('div', { class: 'tp-facts' }, facts.join('  ·  ')));
  } else {
    body.push(el('div', { class: 'tp-status' }, data.error ? i18n.t('tabPreviewError') : i18n.t('tabPreviewOffline')));
  }
  const openBtn = el('button', { class: 'btn btn-primary tp-open' }, '↗ ' + i18n.t('openInBrowser'));
  openBtn.addEventListener('click', () => {
    window.go.main.App.OpenChannelInBrowser(channel);
    hideTabPreview(true);
  });
  body.push(el('div', { class: 'tp-actions' }, openBtn, el('span', { class: 'tp-hint' }, i18n.t('tabPreviewMiddleClick'))));
  tabPreviewEl.replaceChildren(head, ...body);

  // Position under the tab, clamped to the window.
  tabPreviewEl.classList.remove('hidden');
  const r = tab.getBoundingClientRect();
  const w = tabPreviewEl.offsetWidth;
  const left = Math.max(8, Math.min(r.left, window.innerWidth - w - 8));
  tabPreviewEl.style.left = left + 'px';
  tabPreviewEl.style.top = (r.bottom + 6) + 'px';
}

function addChannelTab(channel) {
  if (channels.has(channel)) return;
  const meta = parseChannelKey(channel);

  const chatViewEl = el('div', { class: 'chat-view', 'data-channel': channel, style: 'display:none' });
  chatArea.appendChild(chatViewEl);

  const tab = el('div', { class: 'tab', 'data-channel': channel, draggable: 'true' });
  const liveDotEl = el('span', { class: 'live-dot hidden', title: 'Live' });
  const pillEl = el('span', { class: 'platform-pill ' + (meta.platform === 'youtube' ? 'yt' : 'tw') }, meta.platform === 'youtube' ? 'YT' : 'TW');
  const nameEl = el('span', { class: 'channel-name' }, (meta.platform === 'youtube' ? '' : '#') + meta.displayName);
  const unreadEl = el('span', { class: 'unread hidden' }, '0');
  const closeBtn = el('span', { class: 'tab-close', title: 'Close' }, '✕');
  closeBtn.onclick = (e) => {
    e.stopPropagation();
    window.go.main.App.RemoveChannel(channel);
    removeChannelTab(channel);
  };
  tab.appendChild(liveDotEl);
  tab.appendChild(pillEl);
  tab.appendChild(nameEl);
  tab.appendChild(unreadEl);
  tab.appendChild(closeBtn);
  tab.onclick = () => switchToChannel(channel);
  // Middle click: open the stream / channel in the default browser.
  tab.addEventListener('auxclick', (e) => {
    if (e.button !== 1) return;
    e.preventDefault();
    window.go.main.App.OpenChannelInBrowser(channel);
  });
  tab.addEventListener('mousedown', (e) => { if (e.button === 1) e.preventDefault(); }); // no autoscroll cursor
  attachTabPreview(tab, channel);
  setupTabDrag(tab, channel);

  tabBar.insertBefore(tab, tabAddBtn);

  channels.set(channel, {
    chatViewEl, tabEl: tab, liveDotEl, unreadEl,
    unread: 0, hasMention: false,
    // Per-channel set of seen chatter names + a parallel array kept in
    // insertion order (most-recent last) so we can rank suggestions by
    // recency without traversing the set.
    chatters: new Set(),
    chatterOrder: [],
    // username -> { userId, displayName, color, messages: [{ts, text}] }
    // Drives the user-card popup. Last ~50 messages per user are kept.
    userMessages: new Map(),
  });

  applyOnlyLiveFilter();
  emptyState.classList.add('hidden');

  if (!activeChannel) switchToChannel(channel);

  // Load and render history (last 200 messages from disk)
  loadHistoryForChannel(channel);
}

function removeChannelTab(channel) {
  const state = channels.get(channel);
  if (!state) return;
  if (tabPreview.channel === channel) hideTabPreview(true);
  tabPreviewCache.delete(channel);
  state.tabEl.remove();
  state.chatViewEl.remove();
  channels.delete(channel);
  liveStatus.delete(channel);
  renderQueues.delete(channel);

  if (activeChannel === channel) {
    activeChannel = null;
    const first = channels.keys().next().value;
    if (first) switchToChannel(first);
    else emptyState.classList.remove('hidden');
  }
  applyOnlyLiveFilter();
}

function switchToChannel(channel) {
  if (replyTarget && replyTarget.channel !== channel) clearReplyTarget();
  if (activeChannel) {
    const prev = channels.get(activeChannel);
    if (prev) {
      prev.chatViewEl.style.display = 'none';
      prev.tabEl.classList.remove('active');
    }
  }
  const state = channels.get(channel);
  if (!state) return;
  state.chatViewEl.style.display = '';
  state.tabEl.classList.add('active');
  state.tabEl.classList.remove('has-mention');
  state.unread = 0;
  state.hasMention = false;
  state.unreadEl.classList.add('hidden');
  activeChannel = channel;
  statusText.textContent = i18n.t('watching', { channel });
  msgInput.placeholder = loggedIn ? i18n.t('sendTo', { channel }) : i18n.t('loginRequired');
  state.chatViewEl.scrollTop = state.chatViewEl.scrollHeight;
  refreshStreamMeta();
  applyOnlyLiveFilter(); // the tab we left may now be hidden (offline)
}

// === Stream meta banner ===
// Fetched from Helix via the backend; shown only when the active channel
// is currently live. Refreshes on tab-switch and once per minute while the
// same tab stays active.
const streamMetaEl = document.getElementById('streamMeta');
const smChannelEl = document.getElementById('smChannel');
const smUptimeEl = document.getElementById('smUptime');
const smViewersEl = document.getElementById('smViewers');
const smTitleEl = document.getElementById('smTitle');
let streamMetaTimer = null;

function formatUptime(startedAt) {
  if (!startedAt) return '';
  const ms = Date.now() - new Date(startedAt).getTime();
  if (ms < 0 || !isFinite(ms)) return '';
  const total = Math.floor(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

function applyStreamMeta(channel, info) {
  // We keep the banner visible even when offline so the 👥 chatter list
  // button stays reachable. Only the live-specific fields hide in that case.
  streamMetaEl.classList.remove('hidden');
  const live = !!(info && info.live);
  streamMetaEl.classList.toggle('offline', !live);
  smChannelEl.textContent = '#' + channel + (live && info.gameName ? ' · ' + info.gameName : '');
  smUptimeEl.textContent = live ? formatUptime(info.startedAt) : i18n.t('streamOffline');
  smViewersEl.textContent = live ? (info.viewerCount || 0).toLocaleString() : '—';
  smTitleEl.textContent = live ? (info.title || '') : '';
}

async function refreshStreamMeta() {
  if (streamMetaTimer) { clearInterval(streamMetaTimer); streamMetaTimer = null; }
  const ch = activeChannel;
  if (!ch) { streamMetaEl.classList.add('hidden'); return; }
  // The stream-meta bar is Helix-driven (Twitch only). For non-Twitch
  // tabs (YouTube etc.) just hide it — they don't have the same metadata.
  if (parseChannelKey(ch).platform !== 'twitch') {
    streamMetaEl.classList.add('hidden');
    return;
  }
  try {
    const info = await window.go.main.App.GetStreamInfo(ch);
    if (activeChannel === ch) applyStreamMeta(ch, info);
  } catch (e) { streamMetaEl.classList.add('hidden'); }
  // A newer call (fast tab switching) may have superseded us during the await.
  if (activeChannel !== ch) return;
  // Overlapping calls for the same channel: drop the other's interval first.
  if (streamMetaTimer) { clearInterval(streamMetaTimer); streamMetaTimer = null; }
  // Tick uptime locally every 30s without re-hitting Helix; full refresh
  // every 60s in case the title/game/viewer count changes.
  streamMetaTimer = setInterval(async () => {
    if (activeChannel !== ch) return;
    try {
      const info = await window.go.main.App.GetStreamInfo(ch);
      if (activeChannel === ch) applyStreamMeta(ch, info);
    } catch (e) {}
  }, 60000);
}

function markUnread(channel, isMention) {
  const state = channels.get(channel);
  if (!state || activeChannel === channel) return;
  state.unread++;
  state.unreadEl.textContent = state.unread > 99 ? '99+' : String(state.unread);
  state.unreadEl.classList.remove('hidden');
  if (isMention) {
    state.hasMention = true;
    state.tabEl.classList.add('has-mention');
  }
}

let lastLiveBeepAt = 0;
function setLiveStatus(channel, live) {
  const prev = liveStatus.get(channel);
  liveStatus.set(channel, live);
  const state = channels.get(channel);
  if (!state) return;
  if (live) {
    state.liveDotEl.classList.remove('hidden');
  } else {
    state.liveDotEl.classList.add('hidden');
  }
  applyOnlyLiveFilter();
  // Beep on offline->live transitions AND on the first-seen-live case
  // (initial status snapshot at boot when prev === undefined). Throttle
  // to one beep per 800ms so that opening the app with N already-live
  // channels doesn't produce a wall of overlapping tones.
  if (live && prev !== true && soundOnMention !== 'none') {
    const now = Date.now();
    if (now - lastLiveBeepAt >= 800) {
      lastLiveBeepAt = now;
      playBeep();
    }
  }
  // Notification only on a real offline -> live switch (prev === false).
  // The first status after startup (prev undefined) is not a "went live"
  // event — opening the app with 20 live channels must not show 20 cards.
  if (live && prev === false && liveNotificationsEnabled) {
    showLiveNotification(channel);
  }
}

// === Live notifications ===
// A card bottom-right when a watched channel goes live, with "Go to tab"
// and "Open in browser". While ChatHub is in the background the taskbar
// button also flashes (Windows). Title/game are filled in from
// GetTabPreview once it answers.
const LIVE_NOTIFY_DISMISS_MS = 20000; // auto-close
const LIVE_NOTIFY_MAX = 4;            // older cards are dropped beyond this
let liveNotificationsEnabled = true;
const liveNotifyStackEl = el('div', { class: 'live-notify-stack' });
document.body.appendChild(liveNotifyStackEl);

function showLiveNotification(channel) {
  const meta = parseChannelKey(channel);
  const name = (meta.platform === 'youtube' ? '' : '#') + meta.displayName;
  const detailEl = el('div', { class: 'ln-detail' }, '');
  const close = () => { clearTimeout(timer); card.remove(); };
  const goBtn = el('button', { class: 'btn btn-primary' }, i18n.t('goToTab'));
  goBtn.addEventListener('click', () => {
    if (channels.has(channel)) switchToChannel(channel);
    close();
  });
  const openBtn = el('button', { class: 'btn' }, '↗ ' + i18n.t('openInBrowser'));
  openBtn.addEventListener('click', () => { window.go.main.App.OpenChannelInBrowser(channel); close(); });
  const xBtn = el('span', { class: 'ln-close', title: 'Close' }, '✕');
  xBtn.addEventListener('click', close);
  const card = el('div', { class: 'live-notify' },
    el('div', { class: 'ln-head' },
      el('span', { class: 'ln-dot' }, '●'),
      el('span', { class: 'ln-title' }, i18n.t('liveNotifyTitle', { channel: name })),
      xBtn),
    detailEl,
    el('div', { class: 'ln-actions' }, goBtn, openBtn));
  liveNotifyStackEl.appendChild(card);
  while (liveNotifyStackEl.childElementCount > LIVE_NOTIFY_MAX) {
    liveNotifyStackEl.removeChild(liveNotifyStackEl.firstChild);
  }
  const timer = setTimeout(close, LIVE_NOTIFY_DISMISS_MS);
  if (!document.hasFocus()) {
    try { window.go.main.App.FlashTaskbar(); } catch (_) {}
  }
  window.go.main.App.GetTabPreview(channel).then((info) => {
    if (!info || !card.isConnected) return;
    const parts = [info.title, info.gameName].filter(Boolean);
    detailEl.textContent = parts.join(' · ');
  }).catch(() => {});
}

// "Only live" tab filter. The active tab always stays visible (hiding the
// chat you are reading would be confusing); hidden tabs are counted in a
// "+N offline" chip at the end of the bar that switches back to "all".
const tabHiddenChip = el('button', { class: 'tab-hidden-chip hidden' });
tabHiddenChip.onclick = () => setOnlyShowLive(false);
tabBar.appendChild(tabHiddenChip);

function applyOnlyLiveFilter() {
  let hidden = 0;
  for (const [ch, state] of channels.entries()) {
    const hide = onlyShowLive && !liveStatus.get(ch) && ch !== activeChannel;
    state.tabEl.classList.toggle('hidden', hide);
    if (hide) hidden++;
  }
  tabHiddenChip.textContent = i18n.t('tabHiddenChip', { n: hidden });
  tabHiddenChip.title = i18n.t('tabHiddenChipTitle', { n: hidden });
  tabHiddenChip.classList.toggle('hidden', hidden === 0);
}

function setOnlyShowLive(only) {
  only = !!only;
  if (onlyLiveCheckbox) onlyLiveCheckbox.checked = only;
  if (only === onlyShowLive) return;
  onlyShowLive = only;
  window.go.main.App.SetOnlyShowLive(onlyShowLive);
  applyOnlyLiveFilter();
}

// Right-click anywhere on the tab bar: choose all channels or live only.
const tabFilterMenu = el('div', { class: 'tab-filter-menu hidden', role: 'menu' });
document.body.appendChild(tabFilterMenu);

function closeTabFilterMenu() { tabFilterMenu.classList.add('hidden'); }

function openTabFilterMenu(x, y) {
  const item = (label, active, onPick) => {
    const b = el('button', { class: 'tfm-item' + (active ? ' active' : ''), role: 'menuitemradio', 'aria-checked': String(active) },
      el('span', { class: 'tfm-check' }, active ? '✓' : ''), label);
    b.onclick = () => { closeTabFilterMenu(); onPick(); };
    return b;
  };
  const total = channels.size;
  let live = 0;
  for (const ch of channels.keys()) if (liveStatus.get(ch)) live++;
  tabFilterMenu.replaceChildren(
    item(i18n.t('tabFilterAll', { n: total }), !onlyShowLive, () => setOnlyShowLive(false)),
    item(i18n.t('tabFilterLive', { n: live }), onlyShowLive, () => setOnlyShowLive(true)));
  tabFilterMenu.classList.remove('hidden');
  // Keep the menu inside the window.
  const r = tabFilterMenu.getBoundingClientRect();
  tabFilterMenu.style.left = Math.max(4, Math.min(x, window.innerWidth - r.width - 4)) + 'px';
  tabFilterMenu.style.top = Math.max(4, Math.min(y, window.innerHeight - r.height - 4)) + 'px';
  tabFilterMenu.querySelector('.tfm-item').focus();
}

tabBar.addEventListener('contextmenu', (e) => {
  e.preventDefault();
  hideTabPreview(true);
  openTabFilterMenu(e.clientX, e.clientY);
});
document.addEventListener('mousedown', (e) => {
  if (!tabFilterMenu.classList.contains('hidden') && !tabFilterMenu.contains(e.target)) closeTabFilterMenu();
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && !tabFilterMenu.classList.contains('hidden')) {
    e.stopPropagation();
    closeTabFilterMenu();
  }
}, true);
window.addEventListener('blur', closeTabFilterMenu);
window.addEventListener('resize', closeTabFilterMenu);
i18n.onLocaleChange(applyOnlyLiveFilter);

// === Emote lookup ===
const EMOTE_CACHE_MAX = 4000;
let emoteEpoch = 0; // bumped whenever negative entries are flushed
// Cache key includes the channel: 7TV/BTTV/FFZ channel emotes are per
// channel, so the same word can be an emote in one tab and text in another.
async function lookupEmote(channel, name) {
  const key = channel + '\u0000' + name;
  if (emoteCache.has(key)) return emoteCache.get(key);
  const epoch = emoteEpoch;
  let result = null;
  try {
    const info = await withTimeout(window.go.main.App.LookupEmote(channel, name), LOOKUP_TIMEOUT_MS, null);
    if (info && info.url) result = info;
  } catch (e) { return null; } // don't cache transient failures
  // A lookup that started before emotes finished loading may carry a stale
  // "not an emote"; don't cache it if the cache was flushed meanwhile.
  if (epoch !== emoteEpoch && !result) return result;
  emoteCache.set(key, result);
  // Bounded: evict oldest (Map keeps insertion order).
  while (emoteCache.size > EMOTE_CACHE_MAX) {
    emoteCache.delete(emoteCache.keys().next().value);
  }
  return result;
}
function clearNegativeEmoteCache() {
  emoteEpoch++;
  for (const [k, v] of Array.from(emoteCache.entries())) {
    if (!v) emoteCache.delete(k);
  }
}

// Badge URL lookup with cache
// Cache key includes the channel because sub-tier artwork differs per channel.
const badgeCache = new Map(); // "channel/set/version" -> url
async function lookupBadge(channel, setID, version) {
  const key = (channel || '') + '/' + setID + '/' + version;
  if (badgeCache.has(key)) return badgeCache.get(key);
  let url = '';
  try {
    url = await withTimeout(window.go.main.App.LookupBadge(channel || '', setID, version), LOOKUP_TIMEOUT_MS, '');
  } catch (e) {}
  badgeCache.set(key, url);
  return url;
}

async function renderBadges(channel, badges) {
  const fragment = document.createDocumentFragment();
  if (!badges || badges.length === 0) return fragment;
  for (const b of badges) {
    const url = await lookupBadge(channel, b.name, b.version);
    if (url) {
      const img = el('img', { class: 'badge-img', src: url, alt: b.name, title: b.name });
      fragment.appendChild(img);
    }
  }
  return fragment;
}

async function renderText(text, nativeEmotes, channel, gifs) {
  const fragment = document.createDocumentFragment();
  const runes = Array.from(text);
  const nativeMap = new Map();
  if (nativeEmotes) {
    for (const e of nativeEmotes) {
      if (e.start >= 0 && e.end < runes.length) {
        nativeMap.set(e.start, { end: e.end, id: e.id });
      }
    }
  }
  // GIFs posted in chat (Twitch "gifs" tag): the placeholder text range
  // ("[Yes GIF by …]") is replaced by the image. Twitch requires using the
  // URL exactly as provided; only https URLs are accepted.
  if (gifs) {
    for (const g of gifs) {
      if (g && g.start >= 0 && g.end < runes.length && typeof g.url === 'string' && g.url.startsWith('https://')) {
        nativeMap.set(g.start, { end: g.end, gifUrl: g.url, alt: runes.slice(g.start, g.end + 1).join('') });
      }
    }
  }
  let i = 0;
  while (i < runes.length) {
    if (nativeMap.has(i) && nativeMap.get(i).gifUrl) {
      const g = nativeMap.get(i);
      fragment.appendChild(el('img', { class: 'chat-gif', src: g.gifUrl, alt: g.alt, title: g.alt }));
      i = g.end + 1;
      continue;
    }
    if (nativeMap.has(i)) {
      const e = nativeMap.get(i);
      fragment.appendChild(el('img', { class: 'emote', src: `https://static-cdn.jtvnw.net/emoticons/v2/${e.id}/default/dark/2.0`, alt: '' }));
      i = e.end + 1;
      continue;
    }
    if (runes[i] === ' ') {
      fragment.appendChild(document.createTextNode(' '));
      i++;
      continue;
    }
    const wordStart = i;
    while (i < runes.length && runes[i] !== ' ' && !nativeMap.has(i)) i++;
    const word = runes.slice(wordStart, i).join('');
    const emote = await lookupEmote(channel, word);
    if (emote) {
      fragment.appendChild(el('img', { class: 'emote', src: emote.url, alt: word, title: word }));
    } else {
      fragment.appendChild(document.createTextNode(word));
    }
  }
  return fragment;
}

// Mention regex for myUsername, rebuilt only when the name changes
// (isMention runs for every message in every channel).
let mentionRe = null;
let mentionReFor = '';
function mentionRegex() {
  if (mentionReFor !== myUsername) {
    mentionReFor = myUsername;
    // Word-boundary match (an optional leading @ is not a word char, so it
    // is covered too); a bare substring made short names match everything.
    const esc = myUsername.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    mentionRe = myUsername ? new RegExp('(^|[^a-z0-9_])' + esc + '($|[^a-z0-9_])', 'i') : null;
  }
  return mentionRe;
}

function isMention(text) {
  if (!text) return false;
  const lower = text.toLowerCase();
  const re = mentionRegex();
  if (re && re.test(text)) return true;
  for (const h of highlights) {
    if (h && lower.includes(h)) return true;
  }
  return false;
}

async function renderMessage(msg) {
  const state = channels.get(msg.channel);
  if (!state) return;

  // Ban / timeout: drop the target from the local chatter list so it
  // doesn't linger as a ghost in @-autocomplete / userMessages. Same
  // fix streamerchat got — Twitch doesn't send IRC PART for bans.
  if ((msg.type === 'ban' || msg.type === 'timeout') && msg.targetUsername) {
    const tgt = msg.targetUsername;
    if (state.chatters.delete(tgt)) {
      const idx = state.chatterOrder.indexOf(tgt);
      if (idx >= 0) state.chatterOrder.splice(idx, 1);
      state.userMessages.delete(tgt);
    }
  }

  // Track chatters for @-autocomplete. Move existing names to the end so
  // recently-active users rank higher. SKIP Stream-Together messages:
  // their authors live in a co-hosted channel, not ours, and would
  // pollute the sidebar / autocomplete with strangers.
  if (msg.username && !msg.isSharedChat) {
    const name = msg.username;
    if (state.chatters.has(name)) {
      const idx = state.chatterOrder.indexOf(name);
      if (idx >= 0) state.chatterOrder.splice(idx, 1);
    } else {
      state.chatters.add(name);
    }
    state.chatterOrder.push(name);
    if (state.chatterOrder.length > 500) {
      const dropped = state.chatterOrder.shift();
      state.chatters.delete(dropped);
      state.userMessages.delete(dropped);
    }
    // Keep a small ring of recent messages + role flags for the usercard
    // and the chatter-list popup.
    if ((msg.type === 'chat' || msg.type === 'watchstreak') && msg.text) {
      let u = state.userMessages.get(name);
      if (!u) {
        u = { userId: msg.userId || '', displayName: msg.displayName || name, color: msg.color || '', messages: [], isMod: false, isVIP: false, isBroadcaster: false, isSub: false };
        state.userMessages.set(name, u);
      }
      u.userId = msg.userId || u.userId;
      u.displayName = msg.displayName || u.displayName;
      u.color = msg.color || u.color;
      // Roles are sticky — once a user appears as mod/VIP/broadcaster we
      // keep that flag even if a later message lacks the badge (some IRC
      // tags can be missing).
      if (msg.isMod) u.isMod = true;
      if (msg.isVIP) u.isVIP = true;
      if (msg.isBroadcaster) u.isBroadcaster = true;
      if (msg.isSub) u.isSub = true;
      u.messages.push({ ts: msg.timestamp || Date.now(), text: msg.text });
      // Keep the full per-user session log so the user-card shows
      // everything they said, not just a tiny recent slice. Cap at
      // 1000 per user so a spammer can't OOM the tab.
      if (u.messages.length > 1000) u.messages.splice(0, u.messages.length - 1000);
    }
  }

  const ts = new Date(msg.timestamp || Date.now());
  const tsStr = String(ts.getHours()).padStart(2, '0') + ':' + String(ts.getMinutes()).padStart(2, '0');
  const div = el('div', { class: msg.historical ? 'msg historical' : 'msg' });
  div.dataset.type = msg.type || 'chat'; // used by chat search (is:sub, is:system, …)
  div.appendChild(el('span', { class: 'ts' }, tsStr));

  const type = msg.type || 'chat';

  if (type === 'system') {
    div.appendChild(el('span', { class: 'system' }, i18n.localizeSystemMessage(msg.text || '')));
    appendAndScroll(state, div);
    return;
  }
  if (type === 'join' || type === 'part') return;
  if (type === 'sub' || type === 'giftsub' || type === 'raid' || type === 'announcement') {
    // Sub tier (msg-param-sub-plan): tier 2/3 get a shimmering card,
    // tier 1 / Prime a small badge. Gift bombs show the count.
    if ((type === 'sub' || type === 'giftsub') && msg.subTier > 0) {
      div.classList.add('sub-card', 'sub-tier-' + msg.subTier);
      const label = msg.subPrime ? 'PRIME' : 'TIER ' + msg.subTier;
      div.appendChild(el('span', { class: 'sub-tier-badge' }, label + (msg.giftCount > 1 ? ' × ' + msg.giftCount : '')));
    }
    div.appendChild(el('span', { class: 'super' }, '★'));
    div.appendChild(el('span', { class: 'system' }, ' ' + (msg.text || '')));
    appendAndScroll(state, div);
    if (!msg.historical) markUnread(msg.channel, false);
    return;
  }
  if (type === 'ban' || type === 'timeout') {
    markMessagesDeleted(state, msg.targetUsername, null);
    div.appendChild(el('span', { class: 'super', style: 'color:#ff4444' }, '⛔'));
    div.appendChild(el('span', { class: 'system' }, ' ' + (msg.text || '')));
    appendAndScroll(state, div);
    return;
  }
  if (type === 'deleted') {
    // Backend puts the deleted message's id in msg.id and the author's login
    // in targetUsername. The notice deliberately omits the deleted text.
    markMessagesDeleted(state, null, msg.id);
    const who = msg.targetUsername || msg.username || '';
    div.appendChild(el('span', { class: 'system' }, `${i18n.t('deletedPrefix')} ${who}`.trim()));
    appendAndScroll(state, div);
    return;
  }
  if (type === 'clearchat') {
    markMessagesDeleted(state, null, null, true);
    div.appendChild(el('span', { class: 'system' }, '--- ' + i18n.t('chatCleared') + ' ---'));
    appendAndScroll(state, div);
    return;
  }

  // Tag the row so later delete / ban / clear events can find it. Scoped to
  // this channel's own container, so other channels are never touched.
  if (msg.id) div.dataset.msgId = String(msg.id);
  div.dataset.user = (msg.username || '').toLowerCase();
  addReplyContext(div, msg);
  // Twitch "first-msg": the user's very first message in this channel.
  // Highlighted like Twitch does for the broadcaster/mods so new chatters
  // can be welcomed. The label is a block line ABOVE the message, so it is
  // inserted as the first child (the timestamp is already in the row).
  // Twitch watch streak shared by the viewer (USERNOTICE viewermilestone):
  // rendered as a normal chat line (name, badges, their message) with an
  // orange streak marker above it, so it stands out from regular chat.
  if (type === 'watchstreak') {
    div.classList.add('watch-streak');
    const label = msg.streak > 0 ? i18n.t('watchStreak', { n: msg.streak }) : i18n.t('watchStreakNoCount');
    div.insertBefore(el('div', { class: 'watch-streak-label' }, '🔥 ' + label), div.firstChild);
  } else if (msg.powerUp === 'gigantify') {
    // Bits Power-up "Gigantify an Emote": the last emote is shown huge
    // (see applyGigantify after the text is rendered).
    div.classList.add('powerup-gigantify');
    div.insertBefore(el('div', { class: 'powerup-label' }, '⚡ ' + i18n.t('powerUpGigantify')), div.firstChild);
  } else if (msg.powerUp === 'effect') {
    // Bits Power-up "Message Effect": animated row; known effect ids get
    // their own palette, unknown ones a generic one.
    const fx = /^[a-z0-9-]+$/i.test(msg.animationId || '') ? msg.animationId.toLowerCase() : 'generic';
    div.classList.add('powerup-effect', 'fx-' + fx);
    div.insertBefore(el('div', { class: 'powerup-label' }, '✨ ' + i18n.t('powerUpEffect')), div.firstChild);
  } else if (msg.highlighted) {
    // "Highlight My Message" channel-points reward — as loud as on Twitch.
    div.classList.add('highlighted-msg');
    div.insertBefore(el('div', { class: 'highlighted-msg-label' }, '✨ ' + i18n.t('highlightedMessage')), div.firstChild);
  } else if (msg.rewardId) {
    // Text a viewer typed for a channel-points reward (custom-reward-id).
    div.classList.add('reward-msg');
    div.insertBefore(el('div', { class: 'reward-msg-label' }, '🎁 ' + i18n.t('rewardMessage')), div.firstChild);
  } else if (msg.firstMsg) {
    div.classList.add('first-msg');
    div.insertBefore(el('div', { class: 'first-msg-label' }, '✦ ' + i18n.t('firstMessage')), div.firstChild);
  } else if (msg.returningChatter) {
    // Twitch "returning-chatter": a viewer coming back. Subtler marker
    // than a first message (thin bar + small label, no tint).
    div.classList.add('returning-chatter');
    div.insertBefore(el('div', { class: 'returning-chatter-label' }, '↺ ' + i18n.t('returningChatter')), div.firstChild);
  }

  // Render real Twitch badge images if available, fall back to text
  if (msg.badges && msg.badges.length > 0) {
    div.appendChild(await renderBadges(msg.channel, msg.badges));
  } else {
    if (msg.isBroadcaster) div.appendChild(el('span', { class: 'role-b' }, 'BC'));
    if (msg.isMod) div.appendChild(el('span', { class: 'role-m' }, '[M]'));
    if (msg.isVIP) div.appendChild(el('span', { class: 'role-v' }, '[V]'));
    if (msg.isSub) div.appendChild(el('span', { class: 'role-s' }, '[S]'));
  }

  const name = msg.displayName || msg.username;
  const color = readableColor(msg.color || '#ffffff');
  // 7TV paid users may have a paint applied to their username — a CSS
  // gradient/background-clip recipe sent over the EventAPI WebSocket.
  // Falls back to plain Twitch color when the user has nothing custom.
  const stv = msg.userId ? sevenTVByUser.get(msg.userId) : null;
  const usernameStyle = stv && stv.paintCSS ? stv.paintCSS : ('color:' + color);
  const usernameEl = el('span', { class: 'username', style: usernameStyle }, name);
  usernameEl.addEventListener('click', (e) => {
    e.stopPropagation();
    openUserCard(msg.channel, msg.username, msg.userId);
  });
  // 7TV badge (paid cosmetic) renders right before the username so the
  // user can see who's a 7TV subscriber.
  if (stv && stv.badgeURL) {
    const b = el('img', { class: 'badge-img stv-badge', src: stv.badgeURL, title: stv.badgeName || '7TV' });
    div.appendChild(b);
  }
  div.appendChild(usernameEl);
  if (type !== 'watchstreak' || msg.text) div.appendChild(document.createTextNode(': '));

  const textFragment = await renderText(msg.text || '', msg.twitchEmotes, msg.channel, msg.gifs);
  div.appendChild(textFragment);

  if (msg.bits > 0) {
    div.appendChild(el('span', { class: 'bits' }, ` [${msg.bits} bits]`));
  }

  const mention = isMention(msg.text);
  if (mention) {
    div.classList.add('highlight');
    // History replay must not beep or count as unread.
    if (!msg.historical && soundOnMention !== 'none' && msg.channel !== activeChannel) playBeep();
  }

  applyGigantify(div, msg);
  addReplyButton(div, msg);
  appendAndScroll(state, div);
  if (!msg.historical) markUnread(msg.channel, mention);
}

// Replaces matching chat rows in ONE channel with a muted placeholder.
// Match by message id, by lowercase author, or all rows (clear chat).
function markMessagesDeleted(state, username, msgId, all) {
  const user = (username || '').toLowerCase();
  if (!all && !user && !msgId) return;
  // Compare via dataset instead of building selectors from untrusted ids.
  for (const row of state.chatViewEl.querySelectorAll('.msg[data-user]')) {
    if (row.classList.contains('deleted')) continue;
    if (!all) {
      const hit = msgId ? row.dataset.msgId === String(msgId) : row.dataset.user === user;
      if (!hit) continue;
    }
    const ts = row.querySelector('.ts');
    row.classList.add('deleted');
    row.classList.remove('highlight', 'first-msg', 'returning-chatter', 'watch-streak', 'highlighted-msg', 'reward-msg', 'powerup-gigantify', 'powerup-effect');
    const note = el('span', { class: 'deleted-text' }, i18n.t('messageDeleted'));
    if (ts) row.replaceChildren(ts, note); else row.replaceChildren(note);
  }
}

// Batch join/part events so we don't spam the chat with one line per user.
// Pending events are flushed after a short delay into a single
// "X, Y, Z joined" or "X, Y, Z left" line — like Chatterino does.
const JOIN_PART_FLUSH_MS = 1500;
const JOIN_PART_MAX_NAMES = 8;
const joinPartBuffers = new Map(); // channel -> { joins:[], parts:[], timer }

function renderJoinPart(channel, username, isJoin) {
  let buf = joinPartBuffers.get(channel);
  if (!buf) {
    buf = { joins: [], parts: [], timer: null };
    joinPartBuffers.set(channel, buf);
  }
  const list = isJoin ? buf.joins : buf.parts;
  if (!list.includes(username)) list.push(username);
  if (buf.timer) return;
  buf.timer = setTimeout(() => flushJoinPart(channel), JOIN_PART_FLUSH_MS);
}

function flushJoinPart(channel) {
  const buf = joinPartBuffers.get(channel);
  if (!buf) return;
  const state = channels.get(channel);
  joinPartBuffers.delete(channel);
  if (!state) return;
  const lines = [];
  if (buf.joins.length) lines.push(formatJoinPartLine(buf.joins, true));
  if (buf.parts.length) lines.push(formatJoinPartLine(buf.parts, false));
  for (const text of lines) {
    const div = el('div', { class: 'msg join-part' }, text);
    appendAndScroll(state, div);
  }
}

function formatJoinPartLine(names, isJoin) {
  let shown = names;
  let suffix = '';
  if (names.length > JOIN_PART_MAX_NAMES) {
    shown = names.slice(0, JOIN_PART_MAX_NAMES);
    suffix = ` (+${names.length - JOIN_PART_MAX_NAMES})`;
  }
  const list = shown.join(', ') + suffix;
  return i18n.t(isJoin ? 'userJoined' : 'userParted', { name: list });
}

// followingBottom is a sticky boolean stored as a property on the
// chat-view element. We update it from user scroll events only — never
// re-measure on append. This survives ctrl+wheel zoom (which changes
// scrollHeight without any user intent to leave follow mode) and the
// case where a tall multi-line message would push the bottom out of
// the 120px tolerance window. Default: true (follow until the user
// scrolls up themselves).
function isFollowingBottom(chatViewEl) {
  return chatViewEl._followingBottom !== false;
}
function setFollowingBottom(chatViewEl, v) {
  chatViewEl._followingBottom = !!v;
}
function attachScrollFollow(chatViewEl) {
  if (chatViewEl._followAttached) return;
  chatViewEl._followAttached = true;
  chatViewEl._followingBottom = true;
  chatViewEl.addEventListener('scroll', () => {
    // 40px slack so a rounding error / image reflow doesn't flip us
    // out of follow mode by accident.
    const nearBottom = chatViewEl.scrollHeight - chatViewEl.scrollTop - chatViewEl.clientHeight < 40;
    chatViewEl._followingBottom = nearBottom;
  });
}

function appendAndScroll(state, div) {
  attachScrollFollow(state.chatViewEl);
  const following = isFollowingBottom(state.chatViewEl);
  state.chatViewEl.appendChild(div);
  applySearchToRow(div, state.chatViewEl);
  while (state.chatViewEl.childElementCount > MAX_MESSAGES_PER_CHANNEL) {
    state.chatViewEl.removeChild(state.chatViewEl.firstChild);
  }
  if (state.chatViewEl.style.display !== 'none' && following) {
    state.chatViewEl.scrollTop = state.chatViewEl.scrollHeight;
    // Late-loading images (badges, emotes) can grow the message after
    // append. Re-stick to the bottom once more on the next frame so
    // the user isn't left one image-height short.
    requestAnimationFrame(() => {
      if (isFollowingBottom(state.chatViewEl)) {
        state.chatViewEl.scrollTop = state.chatViewEl.scrollHeight;
      }
    });
  }
}

let beepCtx = null;
function ensureAudioCtx() {
  if (!beepCtx) {
    try {
      beepCtx = new (window.AudioContext || window.webkitAudioContext)();
    } catch (e) { return null; }
  }
  if (beepCtx.state === 'suspended') {
    beepCtx.resume().catch(() => {});
  }
  return beepCtx;
}
function playBeep() {
  const ctx = ensureAudioCtx();
  if (!ctx) return;
  try {
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.connect(gain);
    gain.connect(ctx.destination);
    osc.frequency.value = soundOnMention === 'ping' ? 880 : 440;
    gain.gain.value = 0.15;
    osc.start();
    setTimeout(() => { osc.stop(); }, 200);
  } catch (e) { console.error('beep failed', e); }
}
// Initialize audio on any user interaction (required by browsers)
document.addEventListener('click', ensureAudioCtx, { once: true });
document.addEventListener('keydown', ensureAudioCtx, { once: true });

// === Auth ===
function setLoggedInUI(state, username) {
  loggedIn = state;
  myUsername = username || '';
  // Always clear both transient classes; the branches below re-add the
  // right one. Without this an "expired" state would persist through a
  // subsequent successful login.
  authStatus.classList.remove('logged-in', 'expired');
  if (state) {
    authStatus.textContent = `@${username}`;
    authStatus.classList.add('logged-in');
    authInfo.textContent = i18n.t('loggedInAs', { name: username });
    loginBtn.classList.add('hidden');
    logoutBtn.classList.remove('hidden');
    msgInput.disabled = false;
    msgInput.placeholder = activeChannel ? i18n.t('sendTo', { channel: activeChannel }) : i18n.t('addChannelFirst');
    inputPrefix.classList.add('active');
    inputPrefix.textContent = `@${username}`;
  } else {
    authStatus.textContent = i18n.t('notLoggedIn');
    authStatus.classList.remove('logged-in');
    authInfo.textContent = i18n.t('notLoggedIn');
    loginBtn.classList.remove('hidden');
    logoutBtn.classList.add('hidden');
    msgInput.disabled = true;
    msgInput.placeholder = i18n.t('loginRequired');
    inputPrefix.classList.remove('active');
    inputPrefix.textContent = i18n.t('readOnly');
  }
}

loginBtn.onclick = async () => {
  loginBtn.disabled = true;
  loginBtn.textContent = i18n.t('requestingCode');
  try {
    const result = await window.go.main.App.StartLogin();
    if (result.error) {
      alert('Login error: ' + result.error);
      loginBtn.disabled = false;
      loginBtn.textContent = 'Login';
      return;
    }
    authUrl.textContent = result.verificationUri;
    authUrl.classList.add('clickable');
    authUrl.onclick = () => window.go.main.App.OpenURL(result.verificationUri);
    authCode.textContent = result.userCode;
    authHelp.classList.remove('hidden');
    loginBtn.textContent = i18n.t('waiting');
    // Auto-open browser
    window.go.main.App.OpenURL(result.verificationUri);
  } catch (e) {
    alert('Login failed: ' + e);
    loginBtn.disabled = false;
    loginBtn.textContent = 'Login';
  }
};

logoutBtn.onclick = async () => {
  await window.go.main.App.Logout();
  setLoggedInUI(false, '');
};

// === Modals ===
function openAddModal() {
  addModalBg.classList.remove('hidden');
  channelInput.value = '';
  channelInput.focus();
}
function closeAddModal() { addModalBg.classList.add('hidden'); }
async function openSettings() {
  settingsModalBg.classList.remove('hidden');
  highlightInput.value = highlights.join(', ');
  soundSelect.value = soundOnMention;
  if (localeSelect) localeSelect.value = i18n.locale;
  if (onlyLiveCheckbox) onlyLiveCheckbox.checked = onlyShowLive;
  if (liveNotifyCheckbox) liveNotifyCheckbox.checked = liveNotificationsEnabled;
  if (showTimestampsCheckbox) showTimestampsCheckbox.checked = !document.body.classList.contains('no-ts');
  // Autostart toggle: only show the row on Windows
  try {
    const a = await window.go.main.App.AutostartStatus();
    if (a && a.supported) {
      autostartRow.classList.remove('hidden');
      autostartCheckbox.checked = !!a.enabled;
      showAutostartHint(a.blocked ? i18n.t('autostartBlocked') : '');
    } else {
      autostartRow.classList.add('hidden');
    }
  } catch (e) {
    autostartRow.classList.add('hidden');
  }
  // Version label
  try {
    versionLabelEl.textContent = await window.go.main.App.GetVersion();
  } catch (e) {}
  // Show debug paths
  try {
    const cfg = await window.go.main.App.GetConfigPath();
    const log = await window.go.main.App.GetLogPath();
    document.getElementById('cfgPath').textContent = cfg;
    document.getElementById('logPath').textContent = log;
  } catch (e) {}
  highlightInput.focus();
}

if (checkUpdateBtn) {
  checkUpdateBtn.addEventListener('click', async () => {
    updateStatusEl.textContent = i18n.t('updateChecking');
    updateApplyRow.classList.add('hidden');
    pendingUpdateUrl = '';
    try {
      const res = await window.go.main.App.CheckUpdate();
      if (res.error) {
        updateStatusEl.textContent = i18n.t('updateFailed', { error: res.error });
        return;
      }
      if (!res.available) {
        updateStatusEl.textContent = i18n.t('upToDate') + ' (v' + (res.current || '?') + ')';
        return;
      }
      updateStatusEl.textContent = i18n.t('updateAvailable', { version: res.latest || '?' });
      if (res.downloadUrl) {
        pendingUpdateUrl = res.downloadUrl;
        updateApplyRow.classList.remove('hidden');
      } else {
        // Non-Windows: no exe asset matches. Offer the release page instead.
        updateStatusEl.textContent += ' — ' + i18n.t('updateUnsupported');
      }
      if (res.releaseUrl) {
        releaseUrlEl.href = res.releaseUrl;
        releaseUrlEl.onclick = (e) => {
          e.preventDefault();
          try { window.go.main.App.OpenURL(res.releaseUrl); } catch (_) {}
        };
      }
    } catch (e) {
      updateStatusEl.textContent = i18n.t('updateFailed', { error: String(e) });
    }
  });
}

if (applyUpdateBtn) {
  applyUpdateBtn.addEventListener('click', async () => {
    if (!pendingUpdateUrl) return;
    applyUpdateBtn.disabled = true;
    updateStatusEl.textContent = i18n.t('updateChecking');
    try {
      // No URL is passed: the backend re-resolves the release itself and
      // verifies the download's SHA-256 (see selfupdate.Update).
      // pendingUpdateUrl only tells us an installable asset exists.
      const err = await window.go.main.App.ApplyUpdate();
      if (err) {
        updateStatusEl.textContent = i18n.t('updateFailed', { error: err });
        applyUpdateBtn.disabled = false;
      }
      // On success the backend relaunches and the current window is gone.
    } catch (e) {
      updateStatusEl.textContent = i18n.t('updateFailed', { error: String(e) });
      applyUpdateBtn.disabled = false;
    }
  });
}

// === Font-size: slider + Ctrl+/-/0 shortcuts ===
let chatFontSize = 14;
function applyFontSize(px) {
  chatFontSize = Math.max(10, Math.min(28, px | 0));
  document.documentElement.style.setProperty('--chat-font-size', chatFontSize + 'px');
  const lbl = document.getElementById('fontSizeLabel');
  if (lbl) lbl.textContent = chatFontSize + ' px';
  const range = document.getElementById('fontSizeRange');
  if (range) range.value = String(chatFontSize);
}
async function loadFontSize() {
  try {
    const px = await window.go.main.App.GetFontSize();
    applyFontSize(px || 14);
  } catch (_) { applyFontSize(14); }
}
async function persistFontSize() {
  try { await window.go.main.App.SetFontSize(chatFontSize); } catch (_) {}
}
loadFontSize();
const fontSizeRange = document.getElementById('fontSizeRange');
if (fontSizeRange) {
  fontSizeRange.addEventListener('input', () => applyFontSize(+fontSizeRange.value));
  fontSizeRange.addEventListener('change', persistFontSize);
}
// Ctrl + (=/+), Ctrl - , Ctrl 0 — intercept BEFORE the browser zoom kicks
// in, otherwise WebView2's own zoom would change layout geometry and
// break the scroll-follow heuristics (which is what we just fixed by
// switching to a sticky boolean, but better to also keep zoom off).
document.addEventListener('keydown', (e) => {
  if (!e.ctrlKey || e.altKey || e.metaKey) return;
  if (e.key === '+' || e.key === '=') {
    e.preventDefault();
    applyFontSize(chatFontSize + 1);
    persistFontSize();
  } else if (e.key === '-' || e.key === '_') {
    e.preventDefault();
    applyFontSize(chatFontSize - 1);
    persistFontSize();
  } else if (e.key === '0') {
    e.preventDefault();
    applyFontSize(14);
    persistFontSize();
  }
});
// Also intercept Ctrl+wheel so it doesn't zoom-blow the layout. We map
// it onto our own slider instead.
document.addEventListener('wheel', (e) => {
  if (!e.ctrlKey) return;
  e.preventDefault();
  applyFontSize(chatFontSize + (e.deltaY < 0 ? 1 : -1));
  persistFontSize();
}, { passive: false });

// Locale change: apply immediately so the user sees the effect, persist to config.
if (localeSelect) {
  localeSelect.addEventListener('change', () => {
    const loc = localeSelect.value;
    i18n.setLocale(loc);
    try { window.go.main.App.SetLocale(loc); } catch (e) {}
    // Refresh any dynamic text that's already on screen
    if (activeChannel) {
      statusText.textContent = i18n.t('watching', { channel: activeChannel });
    } else {
      statusText.textContent = i18n.t('ready');
    }
    msgInput.placeholder = loggedIn
      ? (activeChannel ? i18n.t('sendTo', { channel: activeChannel }) : i18n.t('addChannelFirst'))
      : i18n.t('loginRequired');
    inputPrefix.textContent = loggedIn ? '@' + myUsername : i18n.t('readOnly');
    authStatus.textContent = loggedIn ? '@' + myUsername : i18n.t('notLoggedIn');
    if (loggedIn) authInfo.textContent = i18n.t('loggedInAs', { name: myUsername });
    else authInfo.textContent = i18n.t('notLoggedIn');
  });
}

// Apply / persist the "show timestamps" preference. CSS hides .ts when
// body has .no-ts so we don't need to re-render anything.
function applyShowTimestamps(show) {
  document.body.classList.toggle('no-ts', !show);
}
const showTimestampsCheckbox = document.getElementById('showTimestampsCheckbox');
if (showTimestampsCheckbox) {
  showTimestampsCheckbox.addEventListener('change', () => {
    const show = showTimestampsCheckbox.checked;
    applyShowTimestamps(show);
    try { window.go.main.App.SetShowTimestamps(show); } catch (e) {}
  });
}

// One-click toggle: persist immediately on change rather than wait for modal close.
if (autostartCheckbox) {
  autostartCheckbox.addEventListener('change', async () => {
    const want = autostartCheckbox.checked;
    let err = '';
    try {
      err = await window.go.main.App.SetAutostart(want);
    } catch (e) {
      err = String(e);
    }
    if (err) {
      autostartCheckbox.checked = !want; // revert UI on failure
      showAutostartHint(i18n.t('autostartError', { err }), true);
      return;
    }
    showAutostartHint('');
  });
}

// Hint under the autostart checkbox: Windows blocked the entry, or an
// error from the last change. Empty text hides it.
function showAutostartHint(text, isError) {
  const hintEl = document.getElementById('autostartHint');
  if (!hintEl) return;
  hintEl.textContent = text;
  hintEl.classList.toggle('hidden', !text);
  hintEl.classList.toggle('error', !!isError);
}
function closeSettings() {
  settingsModalBg.classList.add('hidden');
  highlights = highlightInput.value.split(',').map(s => s.trim().toLowerCase()).filter(Boolean);
  soundOnMention = soundSelect.value;
  try { window.go.main.App.SetNotifSound(soundOnMention); } catch (e) {}
  if (onlyLiveCheckbox) setOnlyShowLive(onlyLiveCheckbox.checked);
  const newLiveNotify = !liveNotifyCheckbox || liveNotifyCheckbox.checked;
  if (newLiveNotify !== liveNotificationsEnabled) {
    liveNotificationsEnabled = newLiveNotify;
    window.go.main.App.SetLiveNotifications(liveNotificationsEnabled);
  }
  window.go.main.App.SetHighlights(highlights);
}

tabAddBtn.onclick = openAddModal;
settingsBtn.onclick = openSettings;
document.getElementById('testSaveBtn').onclick = async () => {
  const result = await window.go.main.App.TestSave();
  const resEl = document.getElementById('testSaveResult');
  if (result) {
    resEl.textContent = '❌ ' + result;
    resEl.style.color = '#ff4444';
  } else {
    resEl.textContent = '✓ saved';
    resEl.style.color = '#00ff00';
  }
};
document.getElementById('testSoundBtn').onclick = () => {
  // Use the currently-selected sound for testing
  const prev = soundOnMention;
  soundOnMention = soundSelect.value === 'none' ? 'bell' : soundSelect.value;
  playBeep();
  soundOnMention = prev;
};
document.querySelectorAll('[data-close="add"]').forEach(el => el.onclick = closeAddModal);
document.querySelectorAll('[data-close="settings"]').forEach(el => el.onclick = closeSettings);
addChannelBtn.onclick = async () => {
  const rawName = channelInput.value.trim().replace(/^#/, '');
  if (!rawName) return;
  const platformInput = document.querySelector('input[name="addPlatform"]:checked');
  const platform = platformInput ? platformInput.value : 'twitch';
  // Twitch logins are case-insensitive, YouTube handles preserve case.
  const name = platform === 'twitch' ? rawName.toLowerCase() : rawName;
  // Stored key matches the Go-side ref string: "yt:@handle" or plain "miwitv".
  const tabKey = platform === 'youtube' ? 'yt:' + name : name;
  try {
    const err = await window.go.main.App.AddChannelOn(platform, name);
    if (err) { alert(err); return; }
    addChannelTab(tabKey);
    closeAddModal();
  } catch (e) { console.error(e); }
};
channelInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') addChannelBtn.click();
  if (e.key === 'Escape') closeAddModal();
});

// === Chatter list popup ===
// Categorizes the locally-tracked chatters of the active channel by their
// IRC-tag roles (broadcaster / mod / VIP / sub / other). No Helix call —
// the data is whatever we've observed since the tab opened. Clicking a
// name opens the existing user card.
const chatterListBg = el('div', { class: 'modal-bg hidden', id: 'chatterListBg' });
const chatterListModal = el('div', { class: 'modal chatter-list' });
chatterListBg.appendChild(chatterListModal);
chatterListBg.addEventListener('click', (e) => { if (e.target === chatterListBg) closeChatterList(); });
document.body.appendChild(chatterListBg);

function closeChatterList() { chatterListBg.classList.add('hidden'); }

// Cache of last-fetched Helix chatter logins per channel so re-opening the
// modal feels instant. Refreshed on every open.
const helixChattersCache = new Map();

// Header, search input and note are created once; only the list body is
// re-rendered, so typing in the search box never loses focus.
const clTitleEl = el('span', null, '');
const clCloseEl = el('span', { class: 'modal-close' }, '✕');
clCloseEl.addEventListener('click', closeChatterList);
const clSearchEl = el('input', { class: 'cl-search', type: 'text', placeholder: i18n.t('chatterListSearch') });
const clNoteEl = el('div', { class: 'cl-note hidden' }, '');
const clBodyEl = el('div', { class: 'cl-body' });
let chatterListOpts = {};
clSearchEl.addEventListener('input', () => renderChatterList(chatterListOpts));
chatterListModal.append(el('div', { class: 'modal-header' }, clTitleEl, clCloseEl), clSearchEl, clNoteEl, clBodyEl);

async function openChatterList() {
  if (!activeChannel) return;
  const ch = activeChannel;
  chatterListBg.classList.remove('hidden');
  clSearchEl.value = '';
  clSearchEl.placeholder = i18n.t('chatterListSearch');
  renderChatterList({ loading: true });
  clSearchEl.focus();
  // Fire Helix call in the background; render again when it returns. If we
  // already have a cached list show it immediately, then refresh on top.
  // helixChatters is the richer [{login, isBot}, …] shape from v0.2.25+.
  let helixChatters = helixChattersCache.get(ch) || null;
  let helixSource = 'unknown';
  if (helixChatters) renderChatterList({ helixChatters, helixSource: 'helix' });
  try {
    const res = await window.go.main.App.GetChannelChatters(ch);
    helixSource = (res && res.source) || 'none';
    if (res && Array.isArray(res.chatters)) {
      helixChatters = res.chatters;
    } else if (res && Array.isArray(res.users)) {
      helixChatters = res.users.map(login => ({ login, isBot: false }));
    }
    if (helixChatters && helixChatters.length > 0) helixChattersCache.set(ch, helixChatters);
  } catch (e) { helixSource = 'error'; }
  if (activeChannel === ch && !chatterListBg.classList.contains('hidden')) {
    // Keeps whatever the user has typed in the search box meanwhile.
    renderChatterList({ helixChatters, helixSource });
  }
}

function renderChatterList(opts) {
  opts = opts || {};
  chatterListOpts = opts;
  const filterText = clSearchEl.value;
  const state = channels.get(activeChannel);
  clTitleEl.textContent = i18n.t('chatterListTitle', { channel: activeChannel });
  const body = clBodyEl;
  body.replaceChildren();

  // Surface the data source so the user understands why "lurkers" may be
  // missing. Twitch's /chat/chatters endpoint only returns the full list
  // when the caller is broadcaster or moderator of the channel.
  const showNote = !!(opts.helixSource && opts.helixSource !== 'helix' && !opts.loading);
  clNoteEl.classList.toggle('hidden', !showNote);
  clNoteEl.textContent = showNote ? i18n.t('chatterListRestricted') : '';

  if (opts.loading && !opts.helixChatters) {
    body.appendChild(el('div', { class: 'cl-empty' }, i18n.t('chatterListLoading')));
    return;
  }

  // Combine sources: Helix gives us EVERY current viewer (lurkers + bots),
  // with a flag for known bots. Local userMessages gives us roles for users
  // we've seen chat in this session. Merge by username, prefer local role
  // data (and never demote a Helix-flagged bot if our local data is silent
  // about it).
  const merged = new Map(); // login -> { name, displayName, color, isMod, isVIP, isBroadcaster, isSub, isBot, userId }
  if (Array.isArray(opts.helixChatters)) {
    for (const c of opts.helixChatters) {
      const login = (c.login || '').toString();
      if (!login) continue;
      merged.set(login.toLowerCase(), {
        name: login,
        displayName: login,
        color: '',
        isMod: false, isVIP: false, isBroadcaster: false, isSub: false,
        isBot: !!c.isBot,
        userId: '',
      });
    }
  }
  if (state) {
    for (const [name, u] of state.userMessages.entries()) {
      const key = name.toLowerCase();
      const existing = merged.get(key) || { name, displayName: u.displayName || name, color: u.color || '', isMod: false, isVIP: false, isBroadcaster: false, isSub: false, isBot: false, userId: u.userId || '' };
      existing.displayName = u.displayName || existing.displayName;
      existing.color = u.color || existing.color;
      if (u.isMod) existing.isMod = true;
      if (u.isVIP) existing.isVIP = true;
      if (u.isBroadcaster) existing.isBroadcaster = true;
      if (u.isSub) existing.isSub = true;
      existing.userId = u.userId || existing.userId;
      // Don't clear the bot flag if Helix already set it.
      merged.set(key, existing);
    }
  }

  if (merged.size === 0) {
    body.appendChild(el('div', { class: 'cl-empty' }, i18n.t('chatterListEmpty')));
    return;
  }

  // Bots go to their own bucket regardless of any other role. A bot that
  // also happens to be a mod (channels do this with their own bots) is
  // more usefully grouped with the other bots than with the human mods.
  const buckets = { broadcaster: [], mod: [], vip: [], sub: [], other: [], bot: [] };
  const q = (filterText || '').toLowerCase().trim();
  for (const u of merged.values()) {
    if (q && !u.name.toLowerCase().includes(q) && !(u.displayName || '').toLowerCase().includes(q)) continue;
    if (u.isBot) buckets.bot.push({ name: u.name, u });
    else if (u.isBroadcaster) buckets.broadcaster.push({ name: u.name, u });
    else if (u.isMod) buckets.mod.push({ name: u.name, u });
    else if (u.isVIP) buckets.vip.push({ name: u.name, u });
    else if (u.isSub) buckets.sub.push({ name: u.name, u });
    else buckets.other.push({ name: u.name, u });
  }
  for (const b of Object.values(buckets)) {
    b.sort((a, b) => a.name.localeCompare(b.name));
  }

  const sectionDefs = [
    ['broadcaster', i18n.t('chatterListSectionBroadcaster'), buckets.broadcaster],
    ['mod',         i18n.t('chatterListSectionMods'),        buckets.mod],
    ['vip',         i18n.t('chatterListSectionVIPs'),        buckets.vip],
    ['sub',         i18n.t('chatterListSectionSubs'),        buckets.sub],
    ['chat',        i18n.t('chatterListSectionChatters'),    buckets.other],
    ['bot',         i18n.t('chatterListSectionBots'),        buckets.bot],
  ];
  let anyShown = false;
  for (const [cls, title, list] of sectionDefs) {
    if (list.length === 0) continue;
    anyShown = true;
    const section = el('div', { class: 'cl-section cl-section-' + cls });
    section.appendChild(el('div', { class: 'cl-section-title' }, `${title} (${list.length})`));
    for (const { name, u } of list) {
      const row = el('div', { class: 'cl-name', style: u.color ? 'color:' + readableColor(u.color) : '' }, u.displayName || name);
      row.addEventListener('click', () => {
        closeChatterList();
        openUserCard(activeChannel, name, u.userId);
      });
      section.appendChild(row);
    }
    body.appendChild(section);
  }
  if (!anyShown) {
    body.appendChild(el('div', { class: 'cl-empty' }, i18n.t('chatterListNoMatch')));
  }
}

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && !chatterListBg.classList.contains('hidden')) closeChatterList();
});
const chatterListBtnEl = document.getElementById('chatterListBtn');
if (chatterListBtnEl) chatterListBtnEl.addEventListener('click', openChatterList);

// === User card popup ===
// Click on any username -> floating card with Helix-sourced metadata
// (avatar, account age, follower count, following-since, sub status) plus
// the locally-tracked recent messages from that user in this channel.
const userCardBg = el('div', { class: 'modal-bg hidden', id: 'userCardBg' });
const userCardEl = el('div', { class: 'modal user-card' });
userCardBg.appendChild(userCardEl);
userCardBg.addEventListener('click', (e) => { if (e.target === userCardBg) closeUserCard(); });
document.body.appendChild(userCardBg);

function closeUserCard() { userCardBg.classList.add('hidden'); }

let userCardRequestSeq = 0;
async function openUserCard(channel, username, userId) {
  if (!username) return;
  const reqId = ++userCardRequestSeq;
  userCardEl.replaceChildren(el('div', { class: 'user-card-loading' }, i18n.t('waiting') || 'Loading…'));
  userCardBg.classList.remove('hidden');

  let data = null;
  try {
    data = await window.go.main.App.GetUserCard(channel, userId || username);
  } catch (e) { data = { error: String(e) }; }
  // A newer card was opened while this response was in flight: drop it.
  if (reqId !== userCardRequestSeq) return;

  const state = channels.get(channel);
  const local = state && state.userMessages.get(username);

  const displayName = (data && data.displayName) || (local && local.displayName) || username;
  const color = readableColor((local && local.color) || '#ddd');
  const avatar = data && data.avatar;
  const userId2 = (data && data.userId) || (local && local.userId) || '';

  // Kick off a 7TV lookup for users we've never seen chat from yet
  // (e.g. opening the card for a username from history). The backend
  // dedupes by user id; the event handler at the top of this script
  // populates sevenTVByUser when the response arrives.
  if (userId2) {
    try { window.go.main.App.LookupSevenTV(userId2); } catch (_) {}
  }
  const stv = userId2 ? sevenTVByUser.get(userId2) : null;
  const nameStyle = stv && stv.paintCSS ? stv.paintCSS : ('color:' + color);

  const header = el('div', { class: 'uc-header' });
  if (avatar) header.appendChild(el('img', { class: 'uc-avatar', src: avatar, alt: '' }));
  const headerText = el('div', { class: 'uc-head-text' });
  const nameRow = el('div', { class: 'uc-name-row' });
  if (stv && stv.badgeURL) {
    nameRow.appendChild(el('img', { class: 'uc-stv-badge', src: stv.badgeURL, title: stv.badgeName || '7TV' }));
  }
  nameRow.appendChild(el('span', { class: 'uc-name', style: nameStyle }, displayName));
  headerText.appendChild(nameRow);
  if (userId2) headerText.appendChild(el('div', { class: 'uc-meta' }, 'ID: ' + userId2));
  header.appendChild(headerText);
  userCardEl.replaceChildren(header);

  const facts = el('div', { class: 'uc-facts' });
  if (data && data.createdAt) {
    facts.appendChild(el('div', { class: 'uc-fact' }, 'Created: ' + data.createdAt.slice(0, 10)));
  }
  if (data && typeof data.followers === 'number') {
    facts.appendChild(el('div', { class: 'uc-fact' }, 'Followers: ' + data.followers.toLocaleString()));
  }
  if (data && data.followingSince) {
    facts.appendChild(el('div', { class: 'uc-fact' }, 'Following since: ' + data.followingSince.slice(0, 10)));
  }
  if (data && data.subscribed) {
    const tier = ({ '1000': 'Tier 1', '2000': 'Tier 2', '3000': 'Tier 3' }[data.subTier] || data.subTier || 'Subscribed');
    facts.appendChild(el('div', { class: 'uc-fact' }, '★ ' + tier));
  }
  if (data && data.description) {
    facts.appendChild(el('div', { class: 'uc-fact uc-desc' }, data.description));
  }
  if (data && data.error) {
    facts.appendChild(el('div', { class: 'uc-fact uc-err' }, data.error));
  }
  if (facts.childElementCount > 0) userCardEl.appendChild(facts);

  // Full per-user session log — every message this user has sent in
  // this channel since the app opened, scrollable.
  if (local && local.messages.length > 0) {
    const list = el('div', { class: 'uc-msgs' });
    list.appendChild(el('div', { class: 'uc-msgs-title' }, `Messages this session (${local.messages.length})`));
    for (const m of local.messages) {
      const ts = new Date(m.ts);
      const tsStr = String(ts.getHours()).padStart(2, '0') + ':' +
        String(ts.getMinutes()).padStart(2, '0') + ':' +
        String(ts.getSeconds()).padStart(2, '0');
      list.appendChild(el('div', { class: 'uc-msg' },
        el('span', { class: 'uc-msg-ts' }, tsStr),
        ' ',
        el('span', { class: 'uc-msg-text' }, m.text)));
    }
    userCardEl.appendChild(list);
    // Scroll to newest (bottom) once the DOM commits.
    requestAnimationFrame(() => { list.scrollTop = list.scrollHeight; });
  }
}

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && !userCardBg.classList.contains('hidden')) closeUserCard();
});

// === @username autocomplete ===
// Floating popup anchored above the input. Triggered when the word at the
// caret starts with '@'. Tab or Enter accepts, ArrowUp/Down navigate,
// Escape closes. Suggestions are sorted by recency in the active channel
// (newest chatters first), then alphabetically.
const mentionPopup = el('div', { class: 'mention-popup hidden', id: 'mentionPopup' });
document.body.appendChild(mentionPopup);
let mentionState = null; // { start, query, items, index }

function getMentionContext() {
  const v = msgInput.value;
  const caret = msgInput.selectionStart || 0;
  // Walk back from the caret to find the start of the current word
  let i = caret;
  while (i > 0 && v[i - 1] !== ' ' && v[i - 1] !== '\t') i--;
  if (v[i] !== '@') return null;
  const query = v.slice(i + 1, caret).toLowerCase();
  // Allow empty query right after typing '@'
  if (/[^a-z0-9_]/.test(query)) return null;
  return { start: i, query };
}

function buildSuggestions(query) {
  if (!activeChannel) return [];
  const state = channels.get(activeChannel);
  if (!state) return [];
  const order = state.chatterOrder;
  const seen = new Set();
  const matches = [];
  for (let i = order.length - 1; i >= 0; i--) {
    const name = order[i];
    if (seen.has(name)) continue;
    seen.add(name);
    if (!query || name.toLowerCase().startsWith(query)) matches.push(name);
    if (matches.length >= 8) break;
  }
  return matches;
}

function showMentions() {
  const ctx = getMentionContext();
  if (!ctx) { hideMentions(); return; }
  const items = buildSuggestions(ctx.query);
  if (items.length === 0) { hideMentions(); return; }
  mentionState = { start: ctx.start, query: ctx.query, items, index: 0 };
  renderMentionPopup();
}

function renderMentionPopup() {
  mentionPopup.replaceChildren(...mentionState.items.map((name, i) => {
    const itemEl = el('div', { class: 'mention-item' + (i === mentionState.index ? ' active' : '') }, name);
    itemEl.addEventListener('mousedown', (e) => { e.preventDefault(); applyMention(i); });
    return itemEl;
  }));
  // Anchor above the input
  const rect = msgInput.getBoundingClientRect();
  mentionPopup.style.left = rect.left + 'px';
  mentionPopup.style.bottom = (window.innerHeight - rect.top + 4) + 'px';
  mentionPopup.classList.remove('hidden');
}

function hideMentions() {
  mentionState = null;
  mentionPopup.classList.add('hidden');
}

function applyMention(i) {
  if (!mentionState) return;
  const pick = mentionState.items[i];
  if (!pick) return;
  const v = msgInput.value;
  const caret = msgInput.selectionStart || 0;
  const before = v.slice(0, mentionState.start);
  const after = v.slice(caret);
  const insert = '@' + pick + (after.startsWith(' ') ? '' : ' ');
  msgInput.value = before + insert + after;
  const newCaret = (before + insert).length;
  msgInput.setSelectionRange(newCaret, newCaret);
  hideMentions();
}

msgInput.addEventListener('input', showMentions);
msgInput.addEventListener('blur', () => setTimeout(hideMentions, 100));
msgInput.addEventListener('keydown', (e) => {
  if (!mentionState) return;
  if (e.key === 'ArrowDown') {
    e.preventDefault();
    mentionState.index = (mentionState.index + 1) % mentionState.items.length;
    renderMentionPopup();
  } else if (e.key === 'ArrowUp') {
    e.preventDefault();
    mentionState.index = (mentionState.index - 1 + mentionState.items.length) % mentionState.items.length;
    renderMentionPopup();
  } else if (e.key === 'Tab' || (e.key === 'Enter' && mentionState.items.length > 0)) {
    e.preventDefault();
    applyMention(mentionState.index);
  } else if (e.key === 'Escape') {
    e.preventDefault();
    hideMentions();
  }
});

msgInput.addEventListener('keydown', async (e) => {
  // The mention handler above intercepts Enter when the popup is open. It
  // clears mentionState while handling the event, so check defaultPrevented
  // too or the same Enter would accept the mention AND send the message.
  if (mentionState || e.defaultPrevented) return;
  if (e.key === 'Enter' && activeChannel && msgInput.value.trim()) {
    const text = msgInput.value.trim();
    const meta = parseChannelKey(activeChannel);
    msgInput.value = '';
    // /clear is intercepted client-side and wipes only the current
    // channel's chat view + user-message log — same as chatterino.
    // Twitch's server-side chat clear (broadcaster/mod only) would
    // fail for most users anyway.
    if (text.toLowerCase() === '/clear') {
      const state = channels.get(activeChannel);
      if (state) {
        if (state.chatViewEl) state.chatViewEl.replaceChildren();
        state.userMessages.clear();
      }
      return;
    }
    try {
      let err = '';
      if (meta.platform === 'youtube') {
        // YouTube send uses the full tabKey ("yt:@handle") on the backend.
        err = await window.go.main.App.SendYouTubeMessage(activeChannel, text);
      } else {
        // Twitch send takes the plain channel name without any prefix.
        if (replyTarget && replyTarget.channel === activeChannel) {
          err = await window.go.main.App.SendReply(meta.displayName, text, replyTarget.id);
        } else {
          err = await window.go.main.App.SendMessage(meta.displayName, text);
        }
        if (!err) clearReplyTarget();
      }
      if (err) statusText.textContent = 'Send error: ' + err;
    } catch (ex) { console.error(ex); }
  }
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    if (replyTarget && document.activeElement === msgInput) clearReplyTarget();
    if (!addModalBg.classList.contains('hidden')) closeAddModal();
    if (!settingsModalBg.classList.contains('hidden')) closeSettings();
  }
});

let initialStateApplied = false;
async function applyInitialState() {
  if (initialStateApplied) return;
  try {
    const data = await window.go.main.App.GetInitialState();
    applyState(data);
    initialStateApplied = true;
  } catch (e) {
    console.error('GetInitialState failed', e);
  }
}

function applyState(data) {
  if (!data) return;
  if (data.locale) i18n.setLocale(data.locale);
  if (data.notifSound) soundOnMention = data.notifSound;
  if (typeof data.liveNotifications === 'boolean') liveNotificationsEnabled = data.liveNotifications;
  applyShowTimestamps(data.showTimestamps !== false);
  if (Array.isArray(data.highlights)) highlights = data.highlights.map(h => h.toLowerCase());
  // Set onlyShowLive BEFORE adding tabs so addChannelTab's initial
  // applyOnlyLiveFilter call hides offline tabs from the start instead
  // of showing them and then collapsing the list a second later when
  // the live poll fills in. Same for the seeded liveStatus map.
  onlyShowLive = !!data.onlyShowLive;
  if (data.liveStatus) {
    for (const [ch, live] of Object.entries(data.liveStatus)) {
      liveStatus.set(ch, !!live);
    }
  }
  if (Array.isArray(data.channels)) {
    for (const ch of data.channels) {
      if (!channels.has(ch)) addChannelTab(ch);
    }
  }
  // Re-apply liveStatus through setLiveStatus so the live-dot DOM ends
  // up in sync with the map we just seeded.
  if (data.liveStatus) {
    for (const [ch, live] of Object.entries(data.liveStatus)) {
      setLiveStatus(ch, !!live);
    }
  }
  setLoggedInUI(!!data.loggedIn, data.username || '');
  // Keep the status bar clean by default. Transient messages (errors,
  // "watching #x", "auth expired") still write into it as before.
}

// === Wails events ===
function setupEvents() {
  if (!window.runtime || !window.go || !window.go.main || !window.go.main.App) {
    setTimeout(setupEvents, 100);
    return;
  }

  // Pull initial state (avoids race condition with backend emit order)
  applyInitialState();

  window.runtime.EventsOn('ready', (data) => {
    // Backup channel in case the initial pull hadn't happened yet
    applyState(data);
  });

  window.runtime.EventsOn('chat', (msg) => {
    if (!msg.channel || !channels.has(msg.channel)) return;
    enqueueRender(msg);
  });

  window.runtime.EventsOn('joinPart', (data) => {
    if (!data || !data.channel || !channels.has(data.channel)) return;
    renderJoinPart(data.channel, data.username, !!data.isJoin);
  });

  // When backend finishes loading channel-specific badge artwork, drop any
  // stale entries we cached earlier (which would have been the global
  // fallback). The next render will re-fetch and now get the channel's
  // actual sub-tier / custom badge URLs.
  window.runtime.EventsOn('badgesReady', (data) => {
    const ch = data && data.channel;
    if (!ch) return;
    // Channel emotes load right before this event; earlier "not an emote"
    // results for words like channel emotes are now stale.
    clearNegativeEmoteCache();
    const prefix = ch + '/';
    for (const key of Array.from(badgeCache.keys())) {
      if (key.startsWith(prefix)) badgeCache.delete(key);
    }
    // Re-render the channel's view by replaying its DOM messages would be
    // expensive; the user will see correct badges on the next incoming
    // message. For the existing messages, broken-cache effect was only
    // visible until the next message anyway.
  });

  window.runtime.EventsOn('liveStatus', (data) => {
    setLiveStatus(data.channel, !!data.live);
  });

  // 7TV cosmetic updates: backend pushes one of these per affected
  // Twitch user_id whenever a paint/badge entitlement is created or
  // updated for a channel we're watching. We keep a flat map so the
  // chat-render path can look up cosmetics by sender in O(1).
  window.runtime.EventsOn('sevenTVCosmetic', (c) => {
    if (!c || !c.userID) return;
    sevenTVByUser.set(c.userID, {
      paintCSS: c.paintCSS || '',
      badgeURL: c.badgeURL || '',
      badgeName: c.badgeName || '',
    });
  });

  window.runtime.EventsOn('authExpired', () => {
    setLoggedInUI(false, '');
    // Distinguish "session expired" from "never logged in" — both block
    // sending but the user has a different mental model about each.
    authStatus.textContent = i18n.t('sessionExpired');
    authStatus.classList.add('expired');
    statusText.textContent = i18n.t('authExpired');
  });

  window.runtime.EventsOn('loginResult', (data) => {
    if (data.error) {
      alert('Login failed: ' + data.error);
      loginBtn.disabled = false;
      loginBtn.textContent = 'Login';
      authHelp.classList.add('hidden');
      return;
    }
    if (data.success) {
      authHelp.classList.add('hidden');
      loginBtn.disabled = false;
      loginBtn.textContent = 'Login';
      setLoggedInUI(true, data.username);
    }
  });
}

// Apply default-locale strings to the static HTML immediately so the user
// never sees raw english placeholders even before backend state arrives.
// applyState() will overwrite with the persisted locale once GetInitialState resolves.
i18n.applyI18n();

// === Cursor-stuck workaround for mouse-sharing software ===
// Synergy / Barrier / Mouse Without Borders / Logitech Flow inject synthetic
// mouse events that don't carry a proper mouseleave on transition; WebView2
// then never clears its "navigating" cursor state and shows the loading
// spinner over the window indefinitely. We override `cursor` on the body
// whenever the user interacts. mouseenter doesn't bubble, so we listen on
// bubbling counterparts (mouseover / pointerover / mousemove) which fire
// whenever the cursor moves over any element.
let cursorBumpQueued = false;
function bumpCursorState() {
  if (cursorBumpQueued) return;
  cursorBumpQueued = true;
  document.body.style.cursor = 'default';
  requestAnimationFrame(() => {
    document.body.style.cursor = '';
    cursorBumpQueued = false;
  });
}
window.addEventListener('focus', bumpCursorState);
window.addEventListener('blur', bumpCursorState);
document.addEventListener('mouseover', bumpCursorState);
document.addEventListener('pointerover', bumpCursorState);
document.addEventListener('mousemove', bumpCursorState);
document.addEventListener('keydown', bumpCursorState);

// === Settings tabs ===
// Each .settings-tab-btn toggles its sibling .settings-pane by data-tab key.
// Default-active tab is whichever has .active set in the HTML at page load.
document.querySelectorAll('.settings-tab-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    const key = btn.dataset.tab;
    document.querySelectorAll('.settings-tab-btn').forEach(b => b.classList.toggle('active', b === btn));
    document.querySelectorAll('.settings-pane').forEach(p => p.classList.toggle('active', p.dataset.pane === key));
  });
});

// === YouTube login wiring ===
const ytAuthInfo = document.getElementById('ytAuthInfo');
const ytLoginBtn = document.getElementById('ytLoginBtn');
const ytLogoutBtn = document.getElementById('ytLogoutBtn');

function setYTLoginUI(loggedIn) {
  if (!ytAuthInfo) return;
  if (loggedIn) {
    ytAuthInfo.textContent = 'Connected';
    ytAuthInfo.style.color = '#4ade80';
    ytLoginBtn.classList.add('hidden');
    ytLogoutBtn.classList.remove('hidden');
  } else {
    ytAuthInfo.textContent = 'Not connected';
    ytAuthInfo.style.color = '#ff5555';
    ytLoginBtn.classList.remove('hidden');
    ytLogoutBtn.classList.add('hidden');
  }
}

async function refreshYTLoginUI() {
  try {
    const has = await window.go.main.App.HasYouTubeLogin();
    setYTLoginUI(!!has);
  } catch (e) {}
}
refreshYTLoginUI();

if (ytLoginBtn) {
  ytLoginBtn.addEventListener('click', async () => {
    ytLoginBtn.disabled = true;
    ytAuthInfo.textContent = 'Browser window opening — log in there…';
    ytAuthInfo.style.color = '#fbbf24';
    try {
      const err = await window.go.main.App.StartYouTubeLogin();
      if (err) {
        alert('YouTube login failed: ' + err);
        setYTLoginUI(false);
      } else {
        setYTLoginUI(true);
      }
    } catch (e) {
      alert('YouTube login error: ' + e);
      setYTLoginUI(false);
    }
    ytLoginBtn.disabled = false;
  });
}
if (ytLogoutBtn) {
  ytLogoutBtn.addEventListener('click', async () => {
    try { await window.go.main.App.LogoutYouTube(); } catch (e) {}
    setYTLoginUI(false);
  });
}
if (window.runtime && window.runtime.EventsOn) {
  window.runtime.EventsOn('youtubeLoginChanged', (data) => {
    setYTLoginUI(!!(data && data.loggedIn));
  });
}

// === Frameless window controls (Windows; harmless on macOS) ===
function bindWindowBtn(id, fn) {
  const elBtn = document.getElementById(id);
  if (!elBtn) return;
  elBtn.addEventListener('click', () => {
    try { fn(); } catch (e) { console.error('window control', e); }
  });
}
bindWindowBtn('winMinBtn', () => window.runtime.WindowMinimise());
bindWindowBtn('winMaxBtn', () => window.runtime.WindowToggleMaximise());
bindWindowBtn('winCloseBtn', () => window.runtime.Quit());

setupEvents();
