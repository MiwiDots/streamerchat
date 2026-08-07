// StreamerChat frontend
// Communicates with Go backend via Wails runtime events.

const MAX_MESSAGES = 500;
const SEND_TARGETS = ['Twitch', 'YouTube', 'Both'];

// State
let users = new Map(); // key: platform:lowerusername -> {userId, username, displayName, platform, isMod, isVIP, isSub, isBroadcaster, isBot}
// Per-user message log for the current session. Keyed by the same
// platform:lowerusername key as `users`; each entry is an array of
// {ts, text} newest-appended-last. Capped at USER_MSG_CAP per user
// so a spammer can't OOM the tab, but 1000 covers any realistic
// session. Cleared on profile switch.
const userMessages = new Map();
const USER_MSG_CAP = 1000;
let roles = { mods: new Set(), vips: new Set(), bots: new Set(), broadcaster: '' };
let selectedUser = null;
let sendTarget = 0;
let ytEnabled = false;
let emoteCache = new Map(); // name -> {url, animated} or null

// DOM
const chatEl = document.getElementById('chat');
const userListEl = document.getElementById('userList');
const userTitleEl = document.getElementById('userTitle');
const inputEl = document.getElementById('msgInput');
const inputLabelEl = document.getElementById('inputLabel');
const ytPillEl = document.getElementById('ytPill');
const modalBgEl = document.getElementById('modalBg');
const modalUserEl = document.getElementById('modalUser');
const modalInfoEl = document.getElementById('modalInfo');
const modalCloseEl = document.getElementById('modalClose');
const modalMsgsEl = document.getElementById('modalMsgs');

// Helpers
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

function userKey(platform, username) {
  return platform + ':' + username.toLowerCase();
}

// readableColor boosts too-dark hex colors so usernames stay legible on
// the dark chat background — same behavior as Twitch's "Readable
// Colors" filter. Uses PERCEIVED brightness (YIQ luminance) not raw
// HSL lightness, because blue at "50% lightness" has very low
// perceived luminance (0.114 weight) and looks pitch black. The naive
// L-threshold approach missed pure blue entirely. Here we iterate HSL
// L upward keeping hue+saturation until the resulting color hits the
// perceived-brightness target.
function readableColor(hex) {
  if (!hex || typeof hex !== 'string') return '#ffffff';
  let m = hex.replace('#', '');
  if (m.length === 3) m = m.split('').map(c => c + c).join('');
  if (m.length !== 6) return hex;
  const r0 = parseInt(m.slice(0, 2), 16);
  const g0 = parseInt(m.slice(2, 4), 16);
  const b0 = parseInt(m.slice(4, 6), 16);
  // YIQ perceived luminance on the 0..255 scale. Target ≥ 150 keeps
  // even deep-blue readable on our #16161c-ish dark background.
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
  // Walk L upward from max(l, 0.5) in 5% steps until YIQ hits the
  // target. For pure blue this lands around L=0.85; for red/green
  // more like L=0.6. Capped at 0.95 so we never blow all the way to
  // white and lose the user's chosen hue entirely.
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

function applyRoles(u) {
  const name = u.username.toLowerCase();
  if (name === roles.broadcaster.toLowerCase()) u.isBroadcaster = true;
  if (roles.mods.has(name)) u.isMod = true;
  if (roles.vips.has(name)) u.isVIP = true;
  if (roles.bots.has(name)) u.isBot = true;
}

function rolePriority(u) {
  if (u.isBot) return 5;
  if (u.isBroadcaster) return 0;
  if (u.isMod) return 1;
  if (u.isVIP) return 2;
  if (u.isSub) return 3;
  return 4;
}

function sortedUsers() {
  return Array.from(users.values()).sort((a, b) => {
    const pa = rolePriority(a), pb = rolePriority(b);
    if (pa !== pb) return pa - pb;
    const na = (a.displayName || a.username).toLowerCase();
    const nb = (b.displayName || b.username).toLowerCase();
    return na.localeCompare(nb);
  });
}

// Cache for the three well-known global badge image URLs. Resolved from the
// Go-side BadgeRegistry once it has loaded global badges; until then we fall
// back to the colored letter so the list never shows nothing.
const roleBadgeUrls = {};

async function preloadRoleBadges() {
  const wants = [
    ['broadcaster', '1'],
    ['moderator', '1'],
    ['vip', '1'],
    ['subscriber', '0'],
  ];
  let gotAny = false;
  for (const [setID, version] of wants) {
    try {
      const url = await window.go.main.App.LookupBadge('', setID, version);
      if (url) { roleBadgeUrls[setID] = url; gotAny = true; }
    } catch (e) {}
  }
  if (gotAny) refreshUserList();
}

function renderRoleBadge(u) {
  if (u.isBot) return el('span', { class: 'role-bot' }, '~');
  const tryImg = (setID, fallbackLetter, fallbackCls) => {
    const url = roleBadgeUrls[setID];
    if (url) return el('img', { class: 'role-badge-img', src: url, alt: setID, title: setID });
    return el('span', { class: fallbackCls }, fallbackLetter);
  };
  if (u.isBroadcaster) return tryImg('broadcaster', 'B', 'role-b');
  if (u.isMod) return tryImg('moderator', 'M', 'role-m');
  if (u.isVIP) return tryImg('vip', 'V', 'role-v');
  if (u.isSub) return tryImg('subscriber', 'S', 'role-s');
  return el('span', null, ' ');
}

function refreshUserList() {
  const sorted = sortedUsers();
  userTitleEl.textContent = `Users (${sorted.length})`;
  userListEl.replaceChildren(...sorted.map(u => {
    const div = el('div', { class: 'user' + (selectedUser && selectedUser.userId === u.userId ? ' selected' : '') }, renderRoleBadge(u), ' ', u.displayName || u.username);
    div.onclick = () => openUserModal(u);
    return div;
  }));
}

function addOrUpdateUser(msg) {
  if (!msg.username) return;
  const key = userKey(msg.platform, msg.username);
  let u = users.get(key);
  const changed = !u;
  if (!u) {
    u = {
      userId: msg.userId || '',
      username: msg.username,
      displayName: msg.displayName || msg.username,
      platform: msg.platform,
      isMod: !!msg.isMod,
      isVIP: !!msg.isVIP,
      isSub: !!msg.isSub,
      isBroadcaster: !!msg.isBroadcaster,
      isBot: false,
    };
    applyRoles(u);
    users.set(key, u);
  } else {
    let mut = false;
    if (msg.isMod && !u.isMod) { u.isMod = true; mut = true; }
    if (msg.isVIP && !u.isVIP) { u.isVIP = true; mut = true; }
    if (msg.isSub && !u.isSub) { u.isSub = true; mut = true; }
    if (msg.isBroadcaster && !u.isBroadcaster) { u.isBroadcaster = true; mut = true; }
    if (msg.displayName && u.displayName !== msg.displayName) { u.displayName = msg.displayName; }
    if (msg.userId && !u.userId) { u.userId = msg.userId; }
    return mut;
  }
  return changed;
}

// Badge URL lookup with cache
const badgeCache = new Map();
async function lookupBadge(setID, version) {
  const key = setID + '/' + version;
  if (badgeCache.has(key)) return badgeCache.get(key);
  let url = '';
  try { url = await window.go.main.App.LookupBadge('', setID, version); } catch (e) {}
  badgeCache.set(key, url);
  return url;
}
async function renderBadges(badges) {
  const fragment = document.createDocumentFragment();
  if (!badges) return fragment;
  for (const b of badges) {
    const url = await lookupBadge(b.name, b.version);
    if (url) {
      fragment.appendChild(el('img', { class: 'badge-img', src: url, alt: b.name, title: b.name }));
    }
  }
  return fragment;
}

// Emote lookup cache
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

// Render text with emote substitution.
// Native Twitch emotes come from msg.twitchEmotes positions (rune-indexed).
// 3rd party emotes (7TV/BTTV/FFZ) matched by word lookup.
async function renderText(text, nativeEmotes) {
  const fragment = document.createDocumentFragment();
  const runes = Array.from(text);

  // Build native emote map by start position
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
    // Native emote at this position?
    if (nativeMap.has(i)) {
      const e = nativeMap.get(i);
      const url = `https://static-cdn.jtvnw.net/emoticons/v2/${e.id}/default/dark/2.0`;
      fragment.appendChild(el('img', { class: 'emote', src: url, alt: '' }));
      i = e.end + 1;
      continue;
    }

    // Space?
    if (runes[i] === ' ') {
      fragment.appendChild(document.createTextNode(' '));
      i++;
      continue;
    }

    // Word
    const wordStart = i;
    while (i < runes.length && runes[i] !== ' ' && !nativeMap.has(i)) {
      i++;
    }
    const word = runes.slice(wordStart, i).join('');

    // 3rd party emote?
    const emote = await lookupEmote(word);
    if (emote) {
      fragment.appendChild(el('img', { class: 'emote', src: emote.url, alt: word, title: word }));
    } else {
      fragment.appendChild(document.createTextNode(word));
    }
  }

  return fragment;
}

async function renderMessage(msg) {
  const ts = new Date(msg.timestamp || Date.now());
  const tsStr = String(ts.getHours()).padStart(2, '0') + ':' + String(ts.getMinutes()).padStart(2, '0');

  const div = el('div', { class: 'msg' });
  div.appendChild(el('span', { class: 'ts' }, tsStr));

  const platformBadge = msg.platform === 'youtube'
    ? el('span', { class: 'badge badge-yt' }, 'YT')
    : el('span', { class: 'badge badge-tw' }, 'TW');
  div.appendChild(platformBadge);
  div.appendChild(document.createTextNode(' '));

  const type = msg.type || 'chat';

  if (type === 'join') {
    div.appendChild(el('span', { class: 'system' }, `-> ${msg.username} joined`));
    chatEl.appendChild(div);
    trimAndScroll();
    return;
  }
  if (type === 'part') {
    div.appendChild(el('span', { class: 'system' }, `<- ${msg.username} left`));
    chatEl.appendChild(div);
    trimAndScroll();
    return;
  }
  if (type === 'system') {
    div.appendChild(el('span', { class: 'system' }, msg.text || ''));
    chatEl.appendChild(div);
    trimAndScroll();
    return;
  }
  if (type === 'sub' || type === 'giftsub' || type === 'raid' || type === 'membership' || type === 'announcement') {
    div.appendChild(el('span', { class: 'super' }, '★'));
    div.appendChild(el('span', { class: 'system' }, ' ' + (msg.text || '')));
    chatEl.appendChild(div);
    trimAndScroll();
    return;
  }
  if (type === 'ban' || type === 'timeout') {
    div.appendChild(el('span', { class: 'super', style: 'color:#ff4444' }, '⛔'));
    div.appendChild(el('span', { class: 'system' }, ' ' + (msg.text || '')));
    chatEl.appendChild(div);
    trimAndScroll();
    return;
  }
  if (type === 'deleted') {
    div.appendChild(el('span', { class: 'system' }, `[deleted] ${msg.username}: ${msg.text}`));
    chatEl.appendChild(div);
    trimAndScroll();
    return;
  }
  if (type === 'clearchat') {
    div.appendChild(el('span', { class: 'system' }, '--- Chat cleared ---'));
    chatEl.appendChild(div);
    trimAndScroll();
    return;
  }

  // Regular chat message
  if (msg.isSharedChat && msg.sourceChannel) {
    div.appendChild(el('span', { class: 'shared-tag' }, `[${msg.sourceChannel}]`));
  }
  if (msg.badges && msg.badges.length > 0) {
    div.appendChild(await renderBadges(msg.badges));
  } else {
    if (msg.isBroadcaster) div.appendChild(el('span', { class: 'role-b role-prefix' }, 'BC'));
    if (msg.isMod) div.appendChild(el('span', { class: 'role-m role-prefix' }, '[M]'));
    if (msg.isVIP) div.appendChild(el('span', { class: 'role-v role-prefix' }, '[V]'));
    if (msg.isSub) div.appendChild(el('span', { class: 'role-s role-prefix' }, '[S]'));
  }

  if (type === 'superchat' && msg.superChatAmount) {
    div.appendChild(el('span', { class: 'super' }, `[${msg.superChatAmount}]`));
  }

  const name = msg.displayName || msg.username;
  const color = readableColor(msg.color || '#ffffff');
  // 7TV paid users may have a paint (gradient/animated color) applied
  // to their username, or a 7TV badge. Look the sender up in the map
  // the EventAPI WS populates; fall back to plain Twitch color.
  const stv = msg.userId ? sevenTVByUser.get(msg.userId) : null;
  const usernameStyle = stv && stv.paintCSS ? stv.paintCSS : ('color:' + color);
  const usernameEl = el('span', { class: 'username', style: usernameStyle }, name);
  if (stv && stv.badgeURL) {
    div.appendChild(el('img', { class: 'badge-img stv-badge', src: stv.badgeURL, title: stv.badgeName || '7TV' }));
  }
  usernameEl.onclick = () => {
    const u = users.get(userKey(msg.platform, msg.username)) || {
      userId: msg.userId, username: msg.username, displayName: msg.displayName,
      platform: msg.platform, isMod: msg.isMod, isVIP: msg.isVIP, isSub: msg.isSub,
      isBroadcaster: msg.isBroadcaster, isBot: false,
    };
    openUserModal(u, msg.id);
  };
  div.appendChild(usernameEl);
  div.appendChild(document.createTextNode(': '));

  const textFragment = await renderText(msg.text || '', msg.twitchEmotes);
  div.appendChild(textFragment);

  if (msg.bits > 0) {
    div.appendChild(el('span', { class: 'bits' }, ` [${msg.bits} bits]`));
  }

  chatEl.appendChild(div);
  trimAndScroll();
}

// Sticky follow state: we don't re-measure scroll geometry on every
// append (which breaks under ctrl+wheel zoom and tall multi-line
// messages). Instead we set true/false from the user's own scroll
// input, with 40px slack so rounding / image reflow doesn't kick us
// out of follow mode by accident.
let followingBottom = true;
chatEl.addEventListener('scroll', () => {
  const nearBottom = chatEl.scrollHeight - chatEl.scrollTop - chatEl.clientHeight < 40;
  followingBottom = nearBottom;
});

function trimAndScroll() {
  while (chatEl.childElementCount > MAX_MESSAGES) {
    chatEl.removeChild(chatEl.firstChild);
  }
  if (followingBottom) {
    chatEl.scrollTop = chatEl.scrollHeight;
    // Late-loading images / emotes can push the bottom out by a pixel
    // after we just landed there. Re-stick on the next frame.
    requestAnimationFrame(() => {
      if (followingBottom) chatEl.scrollTop = chatEl.scrollHeight;
    });
  }
}

// === Modal ===

async function openUserModal(u, msgId) {
  selectedUser = { ...u, msgId: msgId || '' };
  modalUserEl.textContent = u.displayName || u.username;
  modalInfoEl.textContent = 'Loading user info...';
  renderUserMessages(u);
  modalBgEl.classList.remove('hidden');
  refreshUserList();

  if (u.userId) {
    try {
      const info = await window.go.main.App.GetUserInfo(u.userId);
      if (info) renderModalInfo(info);
    } catch (e) {
      modalInfoEl.textContent = 'Failed to load info: ' + e;
    }
  } else {
    modalInfoEl.textContent = 'No user ID - cannot fetch info';
  }
}

// renderUserMessages fills the modal-msgs box with every message this
// user has sent in the current session. Newest at the bottom so the
// scroll starts at the most recent line (matches how chat itself flows).
function renderUserMessages(u) {
  if (!modalMsgsEl) return;
  modalMsgsEl.replaceChildren();
  const key = userKey(u.platform, u.username);
  const log = userMessages.get(key) || [];
  const title = el('div', { class: 'modal-msgs-title' }, `Messages this session (${log.length})`);
  modalMsgsEl.appendChild(title);
  if (log.length === 0) {
    modalMsgsEl.appendChild(el('div', { class: 'modal-msgs-empty' }, 'no messages yet'));
    return;
  }
  for (const m of log) {
    const d = new Date(m.ts);
    const tsStr = String(d.getHours()).padStart(2, '0') + ':' +
      String(d.getMinutes()).padStart(2, '0') + ':' +
      String(d.getSeconds()).padStart(2, '0');
    modalMsgsEl.appendChild(el('div', { class: 'modal-msg' },
      el('span', { class: 'modal-msg-ts' }, tsStr),
      m.text));
  }
  // Scroll to newest (bottom) after DOM commits.
  requestAnimationFrame(() => { modalMsgsEl.scrollTop = modalMsgsEl.scrollHeight; });
}

function renderModalInfo(info) {
  modalInfoEl.replaceChildren();
  if (info.isBot) {
    modalInfoEl.appendChild(el('div', { class: 'bad' }, '⚠ KNOWN BOT'));
  }
  if (info.isFollower) {
    modalInfoEl.appendChild(el('div', { class: 'ok' }, `✓ Follows since ${info.followedAt} (${info.followAge})`));
  } else {
    modalInfoEl.appendChild(el('div', { class: 'label' }, '✗ Not following'));
  }
  if (info.createdAt) {
    modalInfoEl.appendChild(el('div', { class: 'label' }, `Account: ${info.createdAt} (${info.accountAge})`));
  }
  if (info.description) {
    modalInfoEl.appendChild(el('div', { class: 'desc' }, info.description));
  }
}

function closeModal() {
  modalBgEl.classList.add('hidden');
  selectedUser = null;
  refreshUserList();
}

modalCloseEl.onclick = closeModal;
modalBgEl.onclick = (e) => { if (e.target === modalBgEl) closeModal(); };

document.querySelectorAll('.btn').forEach(btn => {
  btn.onclick = async () => {
    if (!selectedUser) return;
    const action = btn.dataset.action;
    try {
      const result = await window.go.main.App.ModAction(action, selectedUser.userId, selectedUser.msgId || '');
      if (result) {
        await renderMessage({
          type: 'system', platform: 'twitch', timestamp: Date.now(),
          text: `Mod action error: ${result}`
        });
      } else {
        await renderMessage({
          type: 'system', platform: 'twitch', timestamp: Date.now(),
          text: `>>> ${action} on ${selectedUser.displayName || selectedUser.username}`
        });
      }
    } catch (e) {
      console.error(e);
    }
    closeModal();
  };
});

// === Input ===

function updateInputLabel() {
  const target = SEND_TARGETS[sendTarget];
  const prefix = target === 'YouTube' ? 'YT' : target === 'Both' ? 'TW+YT' : 'TW';
  inputLabelEl.textContent = prefix + ' >';
  inputLabelEl.style.color = target === 'YouTube' ? '#ff0000' : '#9146ff';
}

// Mirrors the command set in internal/twitch/slash.go. Order matters
// for Tab cycling — listed alphabetically so /v Tab gives /vip first
// then /vipoff alias, etc. /help and command synonyms (/so, /subs)
// are included so they autocomplete too.
const SLASH_COMMANDS = [
  '/announce', '/announceblue', '/announcegreen', '/announceorange', '/announcepurple',
  '/ban', '/clear',
  '/emoteonly', '/emoteonlyoff',
  '/followers', '/followersoff',
  '/help',
  '/mod',
  '/raid', '/shoutout', '/so',
  '/slow', '/slowoff',
  '/subs', '/subscribers', '/subscribersoff', '/subsoff',
  '/timeout',
  '/unban', '/unmod', '/unraid', '/untimeout', '/unvip',
  '/vip',
];
let slashTabState = null; // {prefix, matches, index} while cycling
let mentionTabState = null; // {tokenStart, prefix, matches, index} for @ Tab

// currentMentionToken finds an @-prefixed word the cursor is on/after.
// Returns {start, prefix} where prefix is the text after "@" up to the
// cursor, or null if the cursor isn't in an @-token. Handles both the
// case where the user is mid-typing (@mi|) and Tab-cycling after a
// space isn't hit yet.
function currentMentionToken() {
  const val = inputEl.value;
  const caret = inputEl.selectionStart || 0;
  // Walk backwards from caret to find "@" or a space.
  let i = caret - 1;
  while (i >= 0 && val[i] !== ' ' && val[i] !== '@') i--;
  if (i < 0 || val[i] !== '@') return null;
  // Everything from the "@" to the caret is the prefix (excluding "@").
  return { start: i, prefix: val.slice(i + 1, caret).toLowerCase() };
}

function mentionCandidates(prefix) {
  const out = [];
  for (const u of users.values()) {
    if (u.platform !== 'twitch') continue;
    if (u.isBot) continue;
    const name = (u.displayName || u.username).toLowerCase();
    if (name.startsWith(prefix) || u.username.toLowerCase().startsWith(prefix)) {
      out.push(u.username);
    }
  }
  return out.sort((a, b) => a.localeCompare(b));
}

inputEl.addEventListener('keydown', async (e) => {
  if (e.key === 'Tab' && inputEl.value.startsWith('/') && !inputEl.value.includes(' ')) {
    e.preventDefault();
    // Re-evaluate matches whenever the typed prefix changes, but keep
    // the cycle position when the user just hits Tab again with the
    // same prefix (so /v Tab Tab Tab walks /vip → /unvip → ...).
    const typed = inputEl.value.toLowerCase();
    if (!slashTabState || slashTabState.prefix !== typed) {
      const stripped = typed.slice(1); // drop the leading "/"
      const matches = SLASH_COMMANDS.filter(c => c.slice(1).startsWith(stripped));
      if (matches.length === 0) return;
      slashTabState = { prefix: typed, matches, index: 0 };
    } else {
      slashTabState.index = (slashTabState.index + (e.shiftKey ? -1 : 1) + slashTabState.matches.length) % slashTabState.matches.length;
    }
    inputEl.value = slashTabState.matches[slashTabState.index] + ' ';
    return;
  }
  // @-mention Tab completion: like slash but for usernames in the
  // current user list. Cursor position matters — a Tab in the middle
  // of "hey @mi | rest" still cycles the @mi token in place.
  if (e.key === 'Tab') {
    const tok = currentMentionToken();
    if (tok) {
      e.preventDefault();
      const key = tok.start + ':' + tok.prefix;
      if (!mentionTabState || mentionTabState.key !== key) {
        const matches = mentionCandidates(tok.prefix);
        if (matches.length === 0) return;
        mentionTabState = { key, start: tok.start, matches, index: 0 };
      } else {
        mentionTabState.index = (mentionTabState.index + (e.shiftKey ? -1 : 1) + mentionTabState.matches.length) % mentionTabState.matches.length;
      }
      const pick = mentionTabState.matches[mentionTabState.index];
      const before = inputEl.value.slice(0, mentionTabState.start);
      const caret = inputEl.selectionStart || 0;
      const after = inputEl.value.slice(caret);
      const insert = '@' + pick + ' ';
      inputEl.value = before + insert + after;
      const newCaret = before.length + insert.length;
      inputEl.setSelectionRange(newCaret, newCaret);
      return;
    }
  }
  // Anything other than Tab resets both cycle states so the next Tab
  // starts fresh from whatever prefix the caret is on.
  if (e.key !== 'Tab' && e.key !== 'Shift') { slashTabState = null; mentionTabState = null; }

  if (e.key === 'Enter') {
    const text = inputEl.value.trim();
    if (text) {
      try {
        await window.go.main.App.SendMessage(text);
      } catch (err) { console.error(err); }
      inputEl.value = '';
    }
  } else if (e.key === 'Escape') {
    if (!modalBgEl.classList.contains('hidden')) {
      closeModal();
    }
  }
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'F2' && ytEnabled) {
    sendTarget = (sendTarget + 1) % 3;
    updateInputLabel();
    e.preventDefault();
  } else if (e.key === 'Escape' && !modalBgEl.classList.contains('hidden')) {
    closeModal();
  }
});

// === Wails events ===

function setupEvents() {
  if (!window.runtime) {
    setTimeout(setupEvents, 100);
    return;
  }

  window.runtime.EventsOn('ready', (data) => {
    ytEnabled = !!data.youtube;
    if (ytEnabled) ytPillEl.classList.remove('hidden');
    if (data.username) setLoggedInUI(true, data.username);
    applyShowTimestamps(data.showTimestamps !== false);
    // Backend loads global badges async. Try a few times so we catch it
    // whether ready fires before or after the badge load finishes.
    preloadRoleBadges();
    setTimeout(preloadRoleBadges, 1500);
    setTimeout(preloadRoleBadges, 4000);
  });

  window.runtime.EventsOn('chat', (msg) => {
    renderMessage(msg);
    // Stream Together: messages from a co-hosted channel show up in our
    // stream with isSharedChat=true. Render them in chat (users want to
    // see them), but DON'T add their author to our sidebar — that
    // sidebar tracks who's in OUR channel, not the merged pool.
    if (msg.type === 'chat' && msg.username && !msg.isSharedChat) {
      const changed = addOrUpdateUser(msg);
      if (changed) refreshUserList();
      playChatSound();
      // Append to the per-user session log so the user-card modal can
      // show the full history of what this person said, not just the
      // last few lines from the visible chat scroll.
      if (msg.text) {
        const key = userKey(msg.platform, msg.username);
        let log = userMessages.get(key);
        if (!log) { log = []; userMessages.set(key, log); }
        log.push({ ts: msg.timestamp || Date.now(), text: msg.text });
        if (log.length > USER_MSG_CAP) log.splice(0, log.length - USER_MSG_CAP);
      }
    }
    // Ban/timeout: the target vanishes from chat — drop them from the
    // sidebar too so it doesn't show ghost users. Twitch doesn't send
    // an IRC PART for banned users, so without this they'd stick.
    if ((msg.type === 'ban' || msg.type === 'timeout') && msg.targetUsername) {
      const key = userKey(msg.platform || 'twitch', msg.targetUsername);
      if (users.delete(key)) refreshUserList();
    }
  });

  // Authoritative chatter list from Helix (polled every 60s by the
  // backend). This is the ground truth for who's in the channel —
  // anyone in our sidebar not in this list has left, so we prune.
  // Role flags are preserved because the roles come from a separate
  // event that populated `roles.*` sets.
  window.runtime.EventsOn('chattersAuthoritative', (list) => {
    const wantedKeys = new Set(list.map(c => userKey('twitch', c.username)));
    let changed = false;
    for (const key of Array.from(users.keys())) {
      // Only prune Twitch users — Helix chatters endpoint knows
      // nothing about YouTube viewers.
      if (!key.startsWith('twitch:')) continue;
      if (!wantedKeys.has(key)) {
        users.delete(key);
        changed = true;
      }
    }
    for (const c of list) {
      const key = userKey('twitch', c.username);
      if (!users.has(key)) {
        const u = {
          userId: c.userId || '', username: c.username, displayName: c.username, platform: 'twitch',
          isMod: false, isVIP: false, isSub: false, isBroadcaster: false, isBot: false,
        };
        applyRoles(u);
        users.set(key, u);
        changed = true;
      } else if (c.userId) {
        users.get(key).userId = c.userId;
      }
    }
    if (changed) refreshUserList();
  });

  // 7TV cosmetic entitlements pushed from the EventAPI WebSocket on
  // the backend. Each event affects one Twitch user_id; we store the
  // paint CSS + badge URL flatly so renderMessage can look them up.
  window.runtime.EventsOn('sevenTVCosmetic', (c) => {
    if (!c || !c.userID) return;
    sevenTVByUser.set(c.userID, {
      paintCSS: c.paintCSS || '',
      badgeURL: c.badgeURL || '',
      badgeName: c.badgeName || '',
    });
  });

  window.runtime.EventsOn('joinPart', (jp) => {
    const key = userKey(jp.platform, jp.username);
    if (jp.isJoin) {
      if (!users.has(key)) {
        const u = {
          userId: '', username: jp.username, displayName: jp.username, platform: jp.platform,
          isMod: false, isVIP: false, isSub: false, isBroadcaster: false, isBot: false,
        };
        applyRoles(u);
        users.set(key, u);
        if (showJoinPart) {
          renderMessage({ type: 'join', platform: jp.platform, username: jp.username, timestamp: Date.now() });
        }
        refreshUserList();
      }
    } else {
      if (users.delete(key)) {
        if (showJoinPart) {
          renderMessage({ type: 'part', platform: jp.platform, username: jp.username, timestamp: Date.now() });
        }
        refreshUserList();
      }
    }
  });

  window.runtime.EventsOn('chatters', (list) => {
    for (const c of list) {
      const key = userKey('twitch', c.username);
      if (!users.has(key)) {
        const u = {
          userId: c.userId || '', username: c.username, displayName: c.username, platform: 'twitch',
          isMod: false, isVIP: false, isSub: false, isBroadcaster: false, isBot: false,
        };
        applyRoles(u);
        users.set(key, u);
      } else if (c.userId) {
        users.get(key).userId = c.userId;
      }
    }
    refreshUserList();
  });

  window.runtime.EventsOn('roles', (data) => {
    if (data.broadcaster) roles.broadcaster = data.broadcaster;
    if (Array.isArray(data.mods)) roles.mods = new Set(data.mods.map(m => m.toLowerCase()));
    if (Array.isArray(data.vips)) roles.vips = new Set(data.vips.map(v => v.toLowerCase()));
    if (Array.isArray(data.bots)) roles.bots = new Set(data.bots.map(b => b.toLowerCase()));
    // Apply to all users
    for (const u of users.values()) applyRoles(u);
    refreshUserList();
  });
}

setupEvents();
updateInputLabel();
inputEl.focus();

// === Cursor-stuck workaround for mouse-sharing software ===
// Synergy / Barrier / Mouse Without Borders / Logitech Flow inject synthetic
// mouse events that don't carry a proper mouseleave on transition; WebView2
// then never clears its "navigating" cursor state and shows the loading
// spinner over the window indefinitely. We override `cursor` on the body
// whenever the user interacts (move/over/click/key/focus). mouseenter
// doesn't bubble, so we listen on bubbling counterparts (mouseover/
// pointerover) which fire whenever the cursor moves over any element.
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

// === Frameless window controls ===
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

// === Boot-time silent update check (auto-banner if newer release exists) ===
let pendingUpdateUrl = '';
async function initUpdateBanner() {
  const banner = document.getElementById('updateBanner');
  const text = document.getElementById('updateText');
  const installBtn = document.getElementById('updateInstallBtn');
  const dismissBtn = document.getElementById('updateDismissBtn');
  if (!banner) return;

  try {
    const res = await window.go.main.App.CheckUpdate();
    if (res && res.available) {
      pendingUpdateUrl = res.downloadUrl || '';
      text.textContent = 'Update available: ' + (res.latest || '?') +
        (res.current ? ' (you have v' + res.current + ')' : '');
      banner.classList.remove('hidden');
    }
  } catch (e) {}

  if (installBtn) {
    installBtn.addEventListener('click', () => applyUpdateFlow(text, installBtn, pendingUpdateUrl));
  }
  if (dismissBtn) {
    dismissBtn.addEventListener('click', () => banner.classList.add('hidden'));
  }
}
initUpdateBanner();

async function applyUpdateFlow(statusEl, btn, url) {
  if (!url) {
    statusEl.textContent = 'No installable asset for this platform — see GitHub releases.';
    return;
  }
  btn.disabled = true;
  statusEl.textContent = 'Downloading…';
  try {
    const err = await window.go.main.App.ApplyUpdate(url);
    if (err) {
      statusEl.textContent = 'Update failed: ' + err;
      btn.disabled = false;
    }
    // success: backend relaunches, this window dies.
  } catch (e) {
    statusEl.textContent = 'Update failed: ' + String(e);
    btn.disabled = false;
  }
}

// === Settings modal (gear icon in titlebar) ===
const settingsBtn = document.getElementById('settingsBtn');
const settingsModalBg = document.getElementById('settingsModalBg');
const settingsClose = document.getElementById('settingsClose');
const settingsDone = document.getElementById('settingsDone');
const settingsVersion = document.getElementById('settingsVersion');

// 7TV paint/badge cosmetics keyed by Twitch user_id. Populated by the
// "sevenTVCosmetic" Wails event the backend emits when its EventAPI
// WebSocket learns about a paint/badge entitlement.
const sevenTVByUser = new Map();

// === Settings tabs ===
document.querySelectorAll('.settings-tab-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    const key = btn.dataset.tab;
    document.querySelectorAll('.settings-tab-btn').forEach(b => b.classList.toggle('active', b === btn));
    document.querySelectorAll('.settings-pane').forEach(p => p.classList.toggle('active', p.dataset.pane === key));
  });
});

// === i18n: hydrate locale from backend, apply on change ===
(async () => {
  try {
    const loc = await window.go.main.App.GetLocale();
    if (loc) i18n.setLocale(loc);
  } catch (_) {}
  i18n.applyI18n();
})();
const localeSelect = document.getElementById('localeSelect');
if (localeSelect) {
  (async () => {
    try {
      const cur = await window.go.main.App.GetLocale();
      if (cur) localeSelect.value = cur;
    } catch (_) {}
  })();
  localeSelect.addEventListener('change', async () => {
    const loc = localeSelect.value;
    i18n.setLocale(loc);
    try { await window.go.main.App.SetLocale(loc); } catch (_) {}
  });
}

// === Font-size: slider + Ctrl+/-/0 + Ctrl+wheel ===
let chatFontSize = 14;
function applyFontSize(px) {
  chatFontSize = Math.max(10, Math.min(28, px | 0));
  document.documentElement.style.setProperty('--chat-font-size', chatFontSize + 'px');
  const lbl = document.getElementById('fontSizeLabel');
  if (lbl) lbl.textContent = chatFontSize + ' px';
  const range = document.getElementById('fontSizeRange');
  if (range) range.value = String(chatFontSize);
}
(async () => {
  try {
    const px = await window.go.main.App.GetFontSize();
    applyFontSize(px || 14);
  } catch (_) { applyFontSize(14); }
})();
async function persistFontSize() {
  try { await window.go.main.App.SetFontSize(chatFontSize); } catch (_) {}
}
const fontSizeRange = document.getElementById('fontSizeRange');
if (fontSizeRange) {
  fontSizeRange.addEventListener('input', () => applyFontSize(+fontSizeRange.value));
  fontSizeRange.addEventListener('change', persistFontSize);
}
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
document.addEventListener('wheel', (e) => {
  if (!e.ctrlKey) return;
  e.preventDefault();
  applyFontSize(chatFontSize + (e.deltaY < 0 ? 1 : -1));
  persistFontSize();
}, { passive: false });

// === join/part visibility toggle ===
let showJoinPart = true;
const showJoinPartCheckbox = document.getElementById('showJoinPartCheckbox');
if (showJoinPartCheckbox) {
  (async () => {
    try {
      const v = await window.go.main.App.GetShowJoinPart();
      showJoinPart = !!v;
      showJoinPartCheckbox.checked = showJoinPart;
    } catch (_) {}
  })();
  showJoinPartCheckbox.addEventListener('change', async () => {
    showJoinPart = !!showJoinPartCheckbox.checked;
    try { await window.go.main.App.SetShowJoinPart(showJoinPart); } catch (_) {}
  });
}
const checkUpdateBtn = document.getElementById('checkUpdateBtn');
const updateStatusEl = document.getElementById('updateStatus');
const updateApplyRow = document.getElementById('updateApplyRow');
const applyUpdateBtn = document.getElementById('applyUpdateBtn');
const updateChannelSelect = document.getElementById('updateChannelSelect');
if (updateChannelSelect) {
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
const autostartSection = document.getElementById('autostartSection');
const autostartCheckbox = document.getElementById('autostartCheckbox');

async function openSettings() {
  settingsModalBg.classList.remove('hidden');
  try {
    settingsVersion.textContent = 'v' + (await window.go.main.App.GetVersion());
  } catch (e) {}
  if (showTimestampsCheckbox) {
    showTimestampsCheckbox.checked = !document.body.classList.contains('no-ts');
  }
  // YouTube config: show whatever's saved so the user can see/edit it.
  try {
    const yt = await window.go.main.App.GetYouTubeConfig();
    if (yt) {
      document.getElementById('ytEnabledCheckbox').checked = !!yt.enabled;
      document.getElementById('ytHandleInput').value = yt.channelHandle || '';
    }
  } catch (e) {}
  // Reflect current login state in the UI every time settings opens.
  // We treat presence of an active IRC connection / non-empty token as
  // logged-in; the backend will refine this via the ready/loginResult events.
  // For boot-after-revocation cases this is "Not logged in" by default.
  // Hide previous run's "available" hint until user re-checks.
  updateApplyRow.classList.add('hidden');
  updateStatusEl.textContent = '';
  // Show autostart row only on Windows.
  try {
    const a = await window.go.main.App.AutostartStatus();
    if (a && a.supported) {
      autostartSection.classList.remove('hidden');
      autostartCheckbox.checked = !!a.enabled;
    } else {
      autostartSection.classList.add('hidden');
    }
  } catch (e) {
    autostartSection.classList.add('hidden');
  }
}
function closeSettings() { settingsModalBg.classList.add('hidden'); }

// === Account profiles ===
// Lists every saved Twitch+YouTube account context, lets the user
// switch via the titlebar dropdown or manage them in Settings →
// Accounts. Backend sends a "profileSwitched" event on switch which
// clears chat / user list and triggers a fresh ready event.
const profileBtnEl = document.getElementById('profileBtn');
const profileBtnNameEl = document.getElementById('profileBtnName');
const profileMenuEl = document.getElementById('profileMenu');
const profilesListEl = document.getElementById('profilesList');
const addProfileBtn = document.getElementById('addProfileBtn');
const newProfileNameEl = document.getElementById('newProfileName');

let cachedProfiles = [];

async function refreshProfiles() {
  try {
    cachedProfiles = await window.go.main.App.ListProfiles() || [];
  } catch (_) { cachedProfiles = []; }
  // Titlebar button text
  const active = cachedProfiles.find(p => p.active);
  if (active && profileBtnNameEl) {
    profileBtnNameEl.textContent = active.name + (active.username ? ` (${active.username})` : '');
  }
  // Settings list (re-rendered each open)
  if (profilesListEl) renderProfilesList();
}

function renderProfilesList() {
  if (!profilesListEl) return;
  profilesListEl.replaceChildren();
  for (const p of cachedProfiles) {
    const row = el('div', { class: 'profile-row' + (p.active ? ' active' : '') });
    const info = el('div', { style: 'flex:1;display:flex;flex-direction:column;' });
    info.appendChild(el('span', { class: 'profile-name' }, p.name));
    if (p.username) info.appendChild(el('span', { class: 'profile-meta' }, '@' + p.username));
    else info.appendChild(el('span', { class: 'profile-meta' }, '(not logged in)'));
    row.appendChild(info);
    if (!p.active) {
      const switchBtn = el('button', { class: 'btn' }, 'Switch');
      switchBtn.addEventListener('click', () => switchToProfile(p.id));
      row.appendChild(switchBtn);
    }
    const renameBtn = el('button', { class: 'btn' }, '✎');
    renameBtn.title = 'Rename';
    renameBtn.addEventListener('click', () => {
      const newName = prompt('Rename profile:', p.name);
      if (newName && newName.trim() && newName !== p.name) {
        window.go.main.App.RenameProfile(p.id, newName.trim()).then(refreshProfiles);
      }
    });
    row.appendChild(renameBtn);
    if (cachedProfiles.length > 1 && !p.active) {
      const removeBtn = el('button', { class: 'btn' }, '✕');
      removeBtn.title = 'Remove';
      removeBtn.addEventListener('click', () => {
        if (confirm(`Remove profile "${p.name}"? This deletes its saved tokens.`)) {
          window.go.main.App.RemoveProfile(p.id).then(refreshProfiles);
        }
      });
      row.appendChild(removeBtn);
    }
    profilesListEl.appendChild(row);
  }
}

async function switchToProfile(id) {
  try {
    const err = await window.go.main.App.SwitchProfile(id);
    if (err) { alert('Switch failed: ' + err); return; }
  } catch (e) { alert('Switch failed: ' + e); }
  // Backend emits "profileSwitched" which clears state — no need to do
  // anything here on success.
  closeProfileMenu();
}

function openProfileMenu() {
  if (!profileMenuEl) return;
  profileMenuEl.replaceChildren();
  for (const p of cachedProfiles) {
    const item = el('div', { class: 'pm-item' + (p.active ? ' active' : '') });
    item.appendChild(el('span', {}, p.name));
    if (p.username) item.appendChild(el('span', { class: 'pm-tag' }, '@' + p.username));
    item.addEventListener('click', () => {
      if (!p.active) switchToProfile(p.id);
      else closeProfileMenu();
    });
    profileMenuEl.appendChild(item);
  }
  profileMenuEl.appendChild(el('div', { class: 'pm-sep' }));
  const manage = el('div', { class: 'pm-item pm-add' }, '⚙  Manage accounts…');
  manage.addEventListener('click', () => {
    closeProfileMenu();
    openSettings();
    // Switch to accounts tab
    document.querySelectorAll('.settings-tab-btn').forEach(b => b.classList.toggle('active', b.dataset.tab === 'accounts'));
    document.querySelectorAll('.settings-pane').forEach(p => p.classList.toggle('active', p.dataset.pane === 'accounts'));
  });
  profileMenuEl.appendChild(manage);

  // Position under the button
  const rect = profileBtnEl.getBoundingClientRect();
  profileMenuEl.style.top = (rect.bottom + 4) + 'px';
  profileMenuEl.style.left = rect.left + 'px';
  profileMenuEl.classList.remove('hidden');
}
function closeProfileMenu() { if (profileMenuEl) profileMenuEl.classList.add('hidden'); }

if (profileBtnEl) {
  profileBtnEl.addEventListener('click', (e) => {
    e.stopPropagation();
    if (profileMenuEl.classList.contains('hidden')) openProfileMenu();
    else closeProfileMenu();
  });
}
document.addEventListener('click', (e) => {
  if (profileMenuEl && !profileMenuEl.classList.contains('hidden')) {
    if (!profileMenuEl.contains(e.target) && e.target !== profileBtnEl) closeProfileMenu();
  }
});

async function submitNewProfile() {
  let name = (newProfileNameEl && newProfileNameEl.value || '').trim();
  if (!name) {
    // Auto-pick a sensible default so an empty click still does
    // something — much better UX than silently no-op'ing.
    name = 'Account ' + (cachedProfiles.length + 1);
  }
  try {
    const id = await window.go.main.App.AddProfile(name);
    if (!id) {
      alert('AddProfile returned no id — backend may be stuck. Check log.');
      return;
    }
    if (newProfileNameEl) newProfileNameEl.value = '';
    await refreshProfiles();
    if (confirm(`Switch to "${name}" now? You'll need to log in to Twitch in the new profile.`)) {
      await switchToProfile(id);
    }
  } catch (e) {
    alert('Add failed: ' + e);
  }
}

if (addProfileBtn) {
  addProfileBtn.addEventListener('click', submitNewProfile);
}
if (newProfileNameEl) {
  newProfileNameEl.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      submitNewProfile();
    }
  });
}

// Clear chat + user list when backend signals a profile switch is in
// progress. The fresh "ready" event arrives moments later from the
// new profile's bootSequence.
function setupProfileSwitchedListener() {
  if (!window.runtime || !window.runtime.EventsOn) {
    setTimeout(setupProfileSwitchedListener, 100);
    return;
  }
  window.runtime.EventsOn('profileSwitched', () => {
    chatEl.replaceChildren();
    users.clear();
    userMessages.clear();
    refreshUserList();
    refreshProfiles();
  });
}
setupProfileSwitchedListener();
// Initial hydrate (delayed so wails runtime is up).
setTimeout(refreshProfiles, 200);

if (settingsBtn) settingsBtn.addEventListener('click', openSettings);
if (settingsClose) settingsClose.addEventListener('click', closeSettings);
if (settingsDone) settingsDone.addEventListener('click', closeSettings);
settingsModalBg.addEventListener('click', (e) => { if (e.target === settingsModalBg) closeSettings(); });
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && !settingsModalBg.classList.contains('hidden')) closeSettings();
});

if (checkUpdateBtn) {
  checkUpdateBtn.addEventListener('click', async () => {
    updateStatusEl.textContent = 'Checking…';
    updateApplyRow.classList.add('hidden');
    pendingUpdateUrl = '';
    try {
      const res = await window.go.main.App.CheckUpdate();
      if (res.error) { updateStatusEl.textContent = 'Failed: ' + res.error; return; }
      if (!res.available) {
        updateStatusEl.textContent = 'Up to date (v' + (res.current || '?') + ')';
        return;
      }
      updateStatusEl.textContent = 'Update available: ' + (res.latest || '?');
      if (res.downloadUrl) {
        pendingUpdateUrl = res.downloadUrl;
        updateApplyRow.classList.remove('hidden');
      } else {
        updateStatusEl.textContent += ' — no Windows asset, see GitHub';
      }
    } catch (e) {
      updateStatusEl.textContent = 'Failed: ' + String(e);
    }
  });
}
if (applyUpdateBtn) {
  applyUpdateBtn.addEventListener('click', () => applyUpdateFlow(updateStatusEl, applyUpdateBtn, pendingUpdateUrl));
}
// === Per-message chat sound ===
// On every incoming chat message, optionally play either a user-supplied .wav
// (loaded once into a data URL via the backend so WebView2 can play it from
// the embedded asset origin) or a built-in oscillator beep. Throttled so a
// burst of messages can't melt the user's ears.
let chatSoundEnabled = false;
let chatSoundVolume = 0.6;
let chatSoundDataUrl = ''; // empty => use built-in oscillator
let chatSoundAudio = null; // pre-built Audio element when we have a data URL
let chatSoundLastPlayed = 0;
const CHAT_SOUND_MIN_INTERVAL_MS = 150;

function rebuildChatSoundAudio() {
  if (!chatSoundDataUrl) { chatSoundAudio = null; return; }
  try {
    chatSoundAudio = new Audio(chatSoundDataUrl);
    chatSoundAudio.preload = 'auto';
    chatSoundAudio.volume = chatSoundVolume;
  } catch (e) {
    console.error('chat sound audio init failed', e);
    chatSoundAudio = null;
  }
}

function playChatSound() {
  if (!chatSoundEnabled) return;
  const now = Date.now();
  if (now - chatSoundLastPlayed < CHAT_SOUND_MIN_INTERVAL_MS) return;
  chatSoundLastPlayed = now;
  if (chatSoundAudio) {
    try {
      // Clone so overlapping calls don't restart the same node.
      const a = chatSoundAudio.cloneNode();
      a.volume = chatSoundVolume;
      a.play().catch(() => {});
    } catch (e) {}
    return;
  }
  // Fallback: built-in short beep via Web Audio.
  try {
    const ctx = ensureBeepCtx();
    if (!ctx) return;
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.connect(gain);
    gain.connect(ctx.destination);
    osc.frequency.value = 660;
    gain.gain.value = 0.06 * chatSoundVolume;
    osc.start();
    setTimeout(() => { osc.stop(); }, 80);
  } catch (e) {}
}

let beepCtx = null;
function ensureBeepCtx() {
  if (!beepCtx) {
    try { beepCtx = new (window.AudioContext || window.webkitAudioContext)(); } catch (e) { return null; }
  }
  if (beepCtx.state === 'suspended') beepCtx.resume().catch(() => {});
  return beepCtx;
}
document.addEventListener('click', ensureBeepCtx, { once: true });
document.addEventListener('keydown', ensureBeepCtx, { once: true });

// Wire up settings UI
const chatSoundCheckbox = document.getElementById('chatSoundCheckbox');
const chatSoundPickBtn = document.getElementById('chatSoundPickBtn');
const chatSoundClearBtn = document.getElementById('chatSoundClearBtn');
const chatSoundFileLabel = document.getElementById('chatSoundFile');
const chatSoundVolumeInput = document.getElementById('chatSoundVolume');
const chatSoundVolumeLabel = document.getElementById('chatSoundVolumeLabel');
const chatSoundTestBtn = document.getElementById('chatSoundTestBtn');

async function loadChatSoundConfig() {
  try {
    const c = await window.go.main.App.GetChatSoundConfig();
    chatSoundEnabled = !!c.enabled;
    chatSoundVolume = Math.max(0, Math.min(100, c.volume || 60)) / 100;
    if (chatSoundCheckbox) chatSoundCheckbox.checked = chatSoundEnabled;
    if (chatSoundVolumeInput) chatSoundVolumeInput.value = String(Math.round(chatSoundVolume * 100));
    if (chatSoundVolumeLabel) chatSoundVolumeLabel.textContent = String(Math.round(chatSoundVolume * 100));
    if (chatSoundFileLabel) chatSoundFileLabel.textContent = c.file || '(built-in beep)';
    chatSoundDataUrl = c.file ? await window.go.main.App.LoadChatSoundData() : '';
    rebuildChatSoundAudio();
  } catch (e) {}
}
loadChatSoundConfig();

if (chatSoundCheckbox) {
  chatSoundCheckbox.addEventListener('change', () => {
    chatSoundEnabled = chatSoundCheckbox.checked;
    try { window.go.main.App.SetChatSoundEnabled(chatSoundEnabled); } catch (e) {}
  });
}
if (chatSoundPickBtn) {
  chatSoundPickBtn.addEventListener('click', async () => {
    try {
      const path = await window.go.main.App.PickChatSoundFile();
      if (path) {
        chatSoundFileLabel.textContent = path;
        chatSoundDataUrl = await window.go.main.App.LoadChatSoundData();
        rebuildChatSoundAudio();
      }
    } catch (e) { alert('Pick failed: ' + e); }
  });
}
if (chatSoundClearBtn) {
  chatSoundClearBtn.addEventListener('click', async () => {
    try { await window.go.main.App.ClearChatSoundFile(); } catch (e) {}
    chatSoundDataUrl = '';
    chatSoundAudio = null;
    chatSoundFileLabel.textContent = '(built-in beep)';
  });
}
if (chatSoundVolumeInput) {
  chatSoundVolumeInput.addEventListener('input', () => {
    const v = parseInt(chatSoundVolumeInput.value, 10) || 0;
    chatSoundVolume = v / 100;
    if (chatSoundAudio) chatSoundAudio.volume = chatSoundVolume;
    if (chatSoundVolumeLabel) chatSoundVolumeLabel.textContent = String(v);
  });
  chatSoundVolumeInput.addEventListener('change', () => {
    const v = parseInt(chatSoundVolumeInput.value, 10) || 0;
    try { window.go.main.App.SetChatSoundVolume(v); } catch (e) {}
  });
}
if (chatSoundTestBtn) {
  chatSoundTestBtn.addEventListener('click', () => {
    const prev = chatSoundEnabled;
    chatSoundEnabled = true;
    chatSoundLastPlayed = 0; // bypass throttle
    playChatSound();
    chatSoundEnabled = prev;
  });
}

// === Timestamps toggle ===
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

// === YouTube settings ===
const ytSaveBtn = document.getElementById('ytSaveBtn');
const ytStatus = document.getElementById('ytStatus');
const ytEnabledCheckbox = document.getElementById('ytEnabledCheckbox');
const ytHandleInput = document.getElementById('ytHandleInput');
if (ytSaveBtn) {
  ytSaveBtn.addEventListener('click', async () => {
    const enabled = !!ytEnabledCheckbox.checked;
    const handle = ytHandleInput.value;
    try {
      const err = await window.go.main.App.SetYouTubeConfig(enabled, handle);
      if (err) {
        ytStatus.textContent = 'Failed: ' + err;
        ytStatus.style.color = '#ff6b6b';
      } else {
        ytStatus.textContent = enabled ? ('Saved. Polling ' + (handle || '') + '…') : 'Disabled.';
        ytStatus.style.color = '#7fdc7f';
        // Re-read so we get the normalized handle ("@" prepended etc.)
        try {
          const yt = await window.go.main.App.GetYouTubeConfig();
          if (yt) ytHandleInput.value = yt.channelHandle || '';
        } catch (e) {}
      }
    } catch (e) {
      ytStatus.textContent = 'Failed: ' + String(e);
      ytStatus.style.color = '#ff6b6b';
    }
  });
}

// === Twitch login (device-code flow) ===
const authInfo = document.getElementById('authInfo');
const loginBtn = document.getElementById('loginBtn');
const logoutBtn = document.getElementById('logoutBtn');
const authHelp = document.getElementById('authHelp');
const authUrl = document.getElementById('authUrl');
const authCode = document.getElementById('authCode');

function setLoggedInUI(loggedIn, username) {
  if (!authInfo) return;
  if (loggedIn) {
    authInfo.textContent = 'Logged in as @' + (username || '?');
    loginBtn.classList.add('hidden');
    logoutBtn.classList.remove('hidden');
  } else {
    authInfo.textContent = 'Not logged in';
    loginBtn.classList.remove('hidden');
    logoutBtn.classList.add('hidden');
  }
}

if (loginBtn) {
  loginBtn.addEventListener('click', async () => {
    loginBtn.disabled = true;
    loginBtn.textContent = 'Requesting…';
    try {
      const res = await window.go.main.App.StartLogin();
      if (res.error) {
        alert('Login failed: ' + res.error);
        loginBtn.disabled = false;
        loginBtn.textContent = 'Login';
        return;
      }
      authUrl.textContent = res.verificationUri;
      authUrl.onclick = () => window.go.main.App.OpenURL(res.verificationUri);
      authCode.textContent = res.userCode;
      authHelp.classList.remove('hidden');
      loginBtn.textContent = 'Waiting…';
      window.go.main.App.OpenURL(res.verificationUri);
    } catch (e) {
      alert('Login error: ' + e);
      loginBtn.disabled = false;
      loginBtn.textContent = 'Login';
    }
  });
}
if (logoutBtn) {
  logoutBtn.addEventListener('click', async () => {
    try { await window.go.main.App.Logout(); } catch (e) {}
    setLoggedInUI(false, '');
  });
}

// Backend tells us when device-code polling finished.
if (window.runtime && window.runtime.EventsOn) {
  window.runtime.EventsOn('loginResult', (data) => {
    if (!data) return;
    if (data.error) {
      alert('Login failed: ' + data.error);
      authHelp.classList.add('hidden');
      loginBtn.disabled = false;
      loginBtn.textContent = 'Login';
      return;
    }
    if (data.success) {
      authHelp.classList.add('hidden');
      loginBtn.disabled = false;
      loginBtn.textContent = 'Login';
      setLoggedInUI(true, data.username);
    }
  });
  // Backend emits 'authExpired' when boot validate + refresh both
  // fail OR when we switch to a profile that has no Twitch token yet.
  // Auto-open settings and jump straight to the Twitch tab so the
  // Login button is one click away — no hunting through tabs.
  window.runtime.EventsOn('authExpired', () => {
    setLoggedInUI(false, '');
    if (settingsModalBg && settingsModalBg.classList.contains('hidden')) {
      openSettings();
    }
    document.querySelectorAll('.settings-tab-btn').forEach(b => b.classList.toggle('active', b.dataset.tab === 'twitch'));
    document.querySelectorAll('.settings-pane').forEach(p => p.classList.toggle('active', p.dataset.pane === 'twitch'));
  });
}

if (autostartCheckbox) {
  autostartCheckbox.addEventListener('change', async () => {
    const want = autostartCheckbox.checked;
    try {
      const err = await window.go.main.App.SetAutostart(want);
      if (err) {
        alert('Autostart: ' + err);
        autostartCheckbox.checked = !want;
      }
    } catch (e) {
      alert('Autostart failed: ' + e);
      autostartCheckbox.checked = !want;
    }
  });
}
