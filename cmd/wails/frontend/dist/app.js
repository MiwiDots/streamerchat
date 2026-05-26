// StreamerChat frontend
// Communicates with Go backend via Wails runtime events.

const MAX_MESSAGES = 500;
const SEND_TARGETS = ['Twitch', 'YouTube', 'Both'];

// State
let users = new Map(); // key: platform:lowerusername -> {userId, username, displayName, platform, isMod, isVIP, isSub, isBroadcaster, isBot}
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
      fragment.appendChild(el('img', { class: 'badge-img', src: url, alt: b.name, title: b.name, loading: 'lazy', decoding: 'async' }));
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
      fragment.appendChild(el('img', { class: 'emote', src: url, alt: '', loading: 'lazy', decoding: 'async' }));
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
      fragment.appendChild(el('img', { class: 'emote', src: emote.url, alt: word, title: word, loading: 'lazy', decoding: 'async' }));
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
  const color = msg.color || '#ffffff';
  const usernameEl = el('span', { class: 'username', style: 'color:' + color }, name);
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

function trimAndScroll() {
  while (chatEl.childElementCount > MAX_MESSAGES) {
    chatEl.removeChild(chatEl.firstChild);
  }
  // Auto-scroll if already at bottom
  const atBottom = chatEl.scrollHeight - chatEl.scrollTop - chatEl.clientHeight < 50;
  if (atBottom) chatEl.scrollTop = chatEl.scrollHeight;
}

// === Modal ===

async function openUserModal(u, msgId) {
  selectedUser = { ...u, msgId: msgId || '' };
  modalUserEl.textContent = u.displayName || u.username;
  modalInfoEl.textContent = 'Loading user info...';
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

inputEl.addEventListener('keydown', async (e) => {
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
    if (msg.type === 'chat' && msg.username) {
      const changed = addOrUpdateUser(msg);
      if (changed) refreshUserList();
    }
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
        renderMessage({ type: 'join', platform: jp.platform, username: jp.username, timestamp: Date.now() });
        refreshUserList();
      }
    } else {
      if (users.delete(key)) {
        renderMessage({ type: 'part', platform: jp.platform, username: jp.username, timestamp: Date.now() });
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
const checkUpdateBtn = document.getElementById('checkUpdateBtn');
const updateStatusEl = document.getElementById('updateStatus');
const updateApplyRow = document.getElementById('updateApplyRow');
const applyUpdateBtn = document.getElementById('applyUpdateBtn');
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
  // Backend emits 'authExpired' when boot validate + refresh both fail.
  // Auto-open settings so the user knows what to do.
  window.runtime.EventsOn('authExpired', () => {
    setLoggedInUI(false, '');
    if (settingsModalBg && settingsModalBg.classList.contains('hidden')) {
      openSettings();
    }
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
