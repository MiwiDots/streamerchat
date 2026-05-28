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
const autostartRow = document.getElementById('autostartRow');
const autostartCheckbox = document.getElementById('autostartCheckbox');
const localeSelect = document.getElementById('localeSelect');
const versionLabelEl = document.getElementById('versionLabel');
const checkUpdateBtn = document.getElementById('checkUpdateBtn');
const updateStatusEl = document.getElementById('updateStatus');
const updateApplyRow = document.getElementById('updateApplyRow');
const applyUpdateBtn = document.getElementById('applyUpdateBtn');
const releaseUrlEl = document.getElementById('releaseUrl');
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

// === Channel tabs ===
async function loadHistoryForChannel(channel) {
  try {
    const history = await window.go.main.App.GetHistory(channel, 200);
    if (history && history.length > 0) {
      // Render historical separator
      const state = channels.get(channel);
      if (state) {
        const sep = el('div', { class: 'msg history-sep' }, '── ' + i18n.t('earlierMessages') + ' ──');
        state.chatViewEl.appendChild(sep);
      }
      for (const msg of history) {
        await renderMessage(msg);
      }
      const state2 = channels.get(channel);
      if (state2) {
        const sep = el('div', { class: 'msg history-sep' }, '── ' + i18n.t('newMessages') + ' ──');
        state2.chatViewEl.appendChild(sep);
        state2.chatViewEl.scrollTop = state2.chatViewEl.scrollHeight;
      }
    }
  } catch (e) {
    console.error('history load failed', e);
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
  state.tabEl.remove();
  state.chatViewEl.remove();
  channels.delete(channel);
  liveStatus.delete(channel);

  if (activeChannel === channel) {
    activeChannel = null;
    const first = channels.keys().next().value;
    if (first) switchToChannel(first);
    else emptyState.classList.remove('hidden');
  }
}

function switchToChannel(channel) {
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
}

function applyOnlyLiveFilter() {
  for (const [ch, state] of channels.entries()) {
    if (onlyShowLive && !liveStatus.get(ch)) {
      state.tabEl.classList.add('hidden');
    } else {
      state.tabEl.classList.remove('hidden');
    }
  }
}

// === Emote lookup ===
async function lookupEmote(name) {
  if (emoteCache.has(name)) return emoteCache.get(name);
  let result = null;
  try {
    const info = await window.go.main.App.LookupEmote(name);
    if (info && info.url) result = info;
  } catch (e) {}
  emoteCache.set(name, result);
  return result;
}

// Badge URL lookup with cache
// Cache key includes the channel because sub-tier artwork differs per channel.
const badgeCache = new Map(); // "channel/set/version" -> url
async function lookupBadge(channel, setID, version) {
  const key = (channel || '') + '/' + setID + '/' + version;
  if (badgeCache.has(key)) return badgeCache.get(key);
  let url = '';
  try {
    url = await window.go.main.App.LookupBadge(channel || '', setID, version);
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

async function renderText(text, nativeEmotes) {
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
  let i = 0;
  while (i < runes.length) {
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
    const emote = await lookupEmote(word);
    if (emote) {
      fragment.appendChild(el('img', { class: 'emote', src: emote.url, alt: word, title: word }));
    } else {
      fragment.appendChild(document.createTextNode(word));
    }
  }
  return fragment;
}

function isMention(text) {
  if (!text) return false;
  const lower = text.toLowerCase();
  if (myUsername && lower.includes('@' + myUsername.toLowerCase())) return true;
  if (myUsername && lower.includes(myUsername.toLowerCase())) return true;
  for (const h of highlights) {
    if (h && lower.includes(h)) return true;
  }
  return false;
}

async function renderMessage(msg) {
  const state = channels.get(msg.channel);
  if (!state) return;

  // Track chatters for @-autocomplete. Move existing names to the end so
  // recently-active users rank higher.
  if (msg.username) {
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
    if (msg.type === 'chat' && msg.text) {
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
      if (u.messages.length > 50) u.messages.shift();
    }
  }

  const ts = new Date(msg.timestamp || Date.now());
  const tsStr = String(ts.getHours()).padStart(2, '0') + ':' + String(ts.getMinutes()).padStart(2, '0');
  const div = el('div', { class: msg.historical ? 'msg historical' : 'msg' });
  div.appendChild(el('span', { class: 'ts' }, tsStr));

  const type = msg.type || 'chat';

  if (type === 'system') {
    div.appendChild(el('span', { class: 'system' }, i18n.localizeSystemMessage(msg.text || '')));
    appendAndScroll(state, div);
    return;
  }
  if (type === 'join' || type === 'part') return;
  if (type === 'sub' || type === 'giftsub' || type === 'raid' || type === 'announcement') {
    div.appendChild(el('span', { class: 'super' }, '★'));
    div.appendChild(el('span', { class: 'system' }, ' ' + (msg.text || '')));
    appendAndScroll(state, div);
    markUnread(msg.channel, false);
    return;
  }
  if (type === 'ban' || type === 'timeout') {
    div.appendChild(el('span', { class: 'super', style: 'color:#ff4444' }, '⛔'));
    div.appendChild(el('span', { class: 'system' }, ' ' + (msg.text || '')));
    appendAndScroll(state, div);
    return;
  }
  if (type === 'deleted') {
    div.appendChild(el('span', { class: 'system' }, `${i18n.t('deletedPrefix')} ${msg.username}: ${msg.text}`));
    appendAndScroll(state, div);
    return;
  }
  if (type === 'clearchat') {
    div.appendChild(el('span', { class: 'system' }, '--- ' + i18n.t('chatCleared') + ' ---'));
    appendAndScroll(state, div);
    return;
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
  const color = msg.color || '#ffffff';
  const usernameEl = el('span', { class: 'username', style: 'color:' + color }, name);
  usernameEl.addEventListener('click', (e) => {
    e.stopPropagation();
    openUserCard(msg.channel, msg.username, msg.userId);
  });
  div.appendChild(usernameEl);
  div.appendChild(document.createTextNode(': '));

  const textFragment = await renderText(msg.text || '', msg.twitchEmotes);
  div.appendChild(textFragment);

  if (msg.bits > 0) {
    div.appendChild(el('span', { class: 'bits' }, ` [${msg.bits} bits]`));
  }

  const mention = isMention(msg.text);
  if (mention) {
    div.classList.add('highlight');
    if (soundOnMention !== 'none' && msg.channel !== activeChannel) playBeep();
  }

  appendAndScroll(state, div);
  markUnread(msg.channel, mention);
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

function appendAndScroll(state, div) {
  state.chatViewEl.appendChild(div);
  while (state.chatViewEl.childElementCount > MAX_MESSAGES_PER_CHANNEL) {
    state.chatViewEl.removeChild(state.chatViewEl.firstChild);
  }
  if (state.chatViewEl.style.display !== 'none') {
    const atBottom = state.chatViewEl.scrollHeight - state.chatViewEl.scrollTop - state.chatViewEl.clientHeight < 50;
    if (atBottom) state.chatViewEl.scrollTop = state.chatViewEl.scrollHeight;
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
  if (showTimestampsCheckbox) showTimestampsCheckbox.checked = !document.body.classList.contains('no-ts');
  // Autostart toggle: only show the row on Windows
  try {
    const a = await window.go.main.App.AutostartStatus();
    if (a && a.supported) {
      autostartRow.classList.remove('hidden');
      autostartCheckbox.checked = !!a.enabled;
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
      const err = await window.go.main.App.ApplyUpdate(pendingUpdateUrl);
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
    try {
      const err = await window.go.main.App.SetAutostart(want);
      if (err) {
        alert('Autostart: ' + err);
        autostartCheckbox.checked = !want; // revert UI on failure
      }
    } catch (e) {
      alert('Autostart failed: ' + e);
      autostartCheckbox.checked = !want;
    }
  });
}
function closeSettings() {
  settingsModalBg.classList.add('hidden');
  highlights = highlightInput.value.split(',').map(s => s.trim().toLowerCase()).filter(Boolean);
  soundOnMention = soundSelect.value;
  try { window.go.main.App.SetNotifSound(soundOnMention); } catch (e) {}
  const newOnlyLive = onlyLiveCheckbox && onlyLiveCheckbox.checked;
  if (newOnlyLive !== onlyShowLive) {
    onlyShowLive = newOnlyLive;
    window.go.main.App.SetOnlyShowLive(onlyShowLive);
    applyOnlyLiveFilter();
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

async function openChatterList() {
  if (!activeChannel) return;
  const ch = activeChannel;
  chatterListBg.classList.remove('hidden');
  renderChatterList('', { loading: true });
  // Fire Helix call in the background; render again when it returns. If we
  // already have a cached list show it immediately, then refresh on top.
  // helixChatters is the richer [{login, isBot}, …] shape from v0.2.25+.
  let helixChatters = helixChattersCache.get(ch) || null;
  let helixSource = 'unknown';
  if (helixChatters) renderChatterList('', { helixChatters, helixSource: 'helix' });
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
    renderChatterList('', { helixChatters, helixSource });
  }
}

function renderChatterList(filterText, opts) {
  opts = opts || {};
  const state = channels.get(activeChannel);
  chatterListModal.replaceChildren();
  const header = el('div', { class: 'modal-header' },
    el('span', null, i18n.t('chatterListTitle', { channel: activeChannel })),
    el('span', { class: 'modal-close' }, '✕'));
  header.querySelector('.modal-close').addEventListener('click', closeChatterList);
  chatterListModal.appendChild(header);

  const search = el('input', { class: 'cl-search', type: 'text', placeholder: i18n.t('chatterListSearch') });
  search.value = filterText || '';
  search.addEventListener('input', () => renderChatterList(search.value, opts));
  chatterListModal.appendChild(search);

  // Surface the data source so the user understands why "lurkers" may be
  // missing. Twitch's /chat/chatters endpoint only returns the full list
  // when the caller is broadcaster or moderator of the channel.
  if (opts.helixSource && opts.helixSource !== 'helix' && !opts.loading) {
    const note = el('div', { class: 'cl-note' }, i18n.t('chatterListRestricted'));
    chatterListModal.appendChild(note);
  }

  const body = el('div', { class: 'cl-body' });
  chatterListModal.appendChild(body);

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
      const row = el('div', { class: 'cl-name', style: u.color ? 'color:' + u.color : '' }, u.displayName || name);
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

async function openUserCard(channel, username, userId) {
  if (!username) return;
  userCardEl.replaceChildren(el('div', { class: 'user-card-loading' }, i18n.t('waiting') || 'Loading…'));
  userCardBg.classList.remove('hidden');

  let data = null;
  try {
    data = await window.go.main.App.GetUserCard(channel, userId || username);
  } catch (e) { data = { error: String(e) }; }

  const state = channels.get(channel);
  const local = state && state.userMessages.get(username);

  const displayName = (data && data.displayName) || (local && local.displayName) || username;
  const color = (local && local.color) || '#ddd';
  const avatar = data && data.avatar;
  const userId2 = (data && data.userId) || (local && local.userId) || '';

  const header = el('div', { class: 'uc-header' });
  if (avatar) header.appendChild(el('img', { class: 'uc-avatar', src: avatar, alt: '' }));
  const headerText = el('div', { class: 'uc-head-text' });
  headerText.appendChild(el('div', { class: 'uc-name', style: 'color:' + color }, displayName));
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

  // Recent local messages
  if (local && local.messages.length > 0) {
    const list = el('div', { class: 'uc-msgs' });
    list.appendChild(el('div', { class: 'uc-msgs-title' }, 'Recent messages'));
    for (const m of local.messages.slice(-15)) {
      const ts = new Date(m.ts);
      const tsStr = String(ts.getHours()).padStart(2, '0') + ':' + String(ts.getMinutes()).padStart(2, '0');
      list.appendChild(el('div', { class: 'uc-msg' },
        el('span', { class: 'uc-msg-ts' }, tsStr),
        ' ',
        el('span', { class: 'uc-msg-text' }, m.text)));
    }
    userCardEl.appendChild(list);
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
  // The mention handler above intercepts Enter when the popup is open.
  if (mentionState) return;
  if (e.key === 'Enter' && activeChannel && msgInput.value.trim()) {
    const text = msgInput.value.trim();
    const meta = parseChannelKey(activeChannel);
    if (meta.platform !== 'twitch') {
      // YouTube send is in v0.3.1 (needs Google OAuth + YouTube Data API).
      statusText.textContent = 'Send for ' + meta.platform + ' is not wired up yet — coming in the next release.';
      return;
    }
    msgInput.value = '';
    try {
      // Backend SendMessage expects the Twitch channel name (no prefix).
      const err = await window.go.main.App.SendMessage(meta.displayName, text);
      if (err) statusText.textContent = 'Send error: ' + err;
    } catch (ex) { console.error(ex); }
  }
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
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
  applyShowTimestamps(data.showTimestamps !== false);
  if (Array.isArray(data.highlights)) highlights = data.highlights.map(h => h.toLowerCase());
  if (Array.isArray(data.channels)) {
    for (const ch of data.channels) {
      if (!channels.has(ch)) addChannelTab(ch);
    }
  }
  // Apply known live status to existing tabs
  if (data.liveStatus) {
    for (const [ch, live] of Object.entries(data.liveStatus)) {
      setLiveStatus(ch, !!live);
    }
  }
  onlyShowLive = !!data.onlyShowLive;
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
    renderMessage(msg);
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
