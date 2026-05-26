// Tiny i18n for ChatHub. No deps. Two locales, runtime switch.

const STRINGS = {
  en: {
    emptyAdd: 'Click <strong>+</strong> to add a Twitch channel',
    emptyHint: 'Anonymous read-only by default. Login via the cog to send messages.',
    readOnly: 'read-only',
    loginRequired: 'Login required to send messages',
    ready: 'Ready',
    notLoggedIn: 'Not logged in',
    settings: 'Settings',
    addChannel: 'Add Channel',
    channelPlaceholder: 'twitch channel name (no #)',
    cancel: 'Cancel',
    add: 'Add',
    done: 'Done',

    language: 'Language',
    highlightKeywords: 'Highlight keywords (comma separated, your name will be added automatically)',
    highlightPlaceholder: 'kappa, lurk, ...',
    notifSound: 'Notification sound on mention',
    soundNone: 'None',
    soundBell: 'Bell',
    soundPing: 'Ping',
    test: 'Test',
    onlyLive: 'Only show channels that are currently live',
    showTimestamps: 'Show timestamps',
    autostart: 'Start ChatHub automatically with Windows',
    twitchLogin: 'Twitch Login (to send messages)',
    login: 'Login',
    logout: 'Logout',
    authStep1: '1. Go to',
    authStep2: '2. Enter code:',
    authWaiting: 'Waiting for authorization...',
    debug: 'Debug',
    configLabel: 'Config:',
    logLabel: 'Log:',
    forceSave: 'Force save',

    sendTo: 'Send to #{channel}',
    addChannelFirst: 'Add a channel first',
    watching: 'Watching #{channel}',
    loadedChannels: 'Loaded {count} channels from {path}',
    authExpired: 'Auth expired - please login again',
    loggedInAs: 'Logged in as @{name}',
    requestingCode: 'Requesting code...',
    waiting: 'Waiting...',
    earlierMessages: 'earlier messages',
    newMessages: 'new messages',
    chatCleared: 'Chat cleared',
    deletedPrefix: '[deleted]',

    joinedChannel: 'Joined #{channel}',
    connectedChannel: 'Connected to #{channel}',
    userJoined: '{name} joined',
    userParted: '{name} left',

    updates: 'Updates',
    versionLabel: 'Version:',
    checkForUpdates: 'Check for updates',
    installUpdate: 'Install update',
    upToDate: 'Up to date',
    updateAvailable: 'Update available: {version}',
    updateChecking: 'Checking…',
    updateFailed: 'Update failed: {error}',
    updateUnsupported: 'Auto-update is only available on Windows. Download manually from the release page.',
  },
  de: {
    emptyAdd: 'Klicke auf <strong>+</strong> um einen Twitch-Channel hinzuzufügen',
    emptyHint: 'Standardmäßig anonym im Read-Only-Modus. Über das Zahnrad einloggen um Nachrichten zu senden.',
    readOnly: 'nur-lesen',
    loginRequired: 'Login nötig um Nachrichten zu senden',
    ready: 'Bereit',
    notLoggedIn: 'Nicht eingeloggt',
    settings: 'Einstellungen',
    addChannel: 'Channel hinzufügen',
    channelPlaceholder: 'Twitch-Channelname (ohne #)',
    cancel: 'Abbrechen',
    add: 'Hinzufügen',
    done: 'Fertig',

    language: 'Sprache',
    highlightKeywords: 'Highlight-Stichwörter (kommagetrennt, dein Name wird automatisch hinzugefügt)',
    highlightPlaceholder: 'kappa, lurk, ...',
    notifSound: 'Benachrichtigungston bei Erwähnung',
    soundNone: 'Aus',
    soundBell: 'Glocke',
    soundPing: 'Ping',
    test: 'Testen',
    onlyLive: 'Nur Channels anzeigen die gerade live sind',
    showTimestamps: 'Zeitstempel anzeigen',
    autostart: 'ChatHub automatisch mit Windows starten',
    twitchLogin: 'Twitch-Login (zum Nachrichten senden)',
    login: 'Einloggen',
    logout: 'Ausloggen',
    authStep1: '1. Öffne',
    authStep2: '2. Code eingeben:',
    authWaiting: 'Warte auf Autorisierung...',
    debug: 'Debug',
    configLabel: 'Config:',
    logLabel: 'Log:',
    forceSave: 'Speichern erzwingen',

    sendTo: 'Senden an #{channel}',
    addChannelFirst: 'Erst einen Channel hinzufügen',
    watching: 'Schaue #{channel}',
    loadedChannels: '{count} Channels aus {path} geladen',
    authExpired: 'Auth abgelaufen - bitte neu einloggen',
    loggedInAs: 'Eingeloggt als @{name}',
    requestingCode: 'Fordere Code an...',
    waiting: 'Warte...',
    earlierMessages: 'ältere Nachrichten',
    newMessages: 'neue Nachrichten',
    chatCleared: 'Chat geleert',
    deletedPrefix: '[gelöscht]',

    joinedChannel: 'Beigetreten #{channel}',
    connectedChannel: 'Verbunden mit #{channel}',
    userJoined: '{name} ist beigetreten',
    userParted: '{name} ist gegangen',

    updates: 'Updates',
    versionLabel: 'Version:',
    checkForUpdates: 'Nach Updates suchen',
    installUpdate: 'Update installieren',
    upToDate: 'Aktuell',
    updateAvailable: 'Update verfügbar: {version}',
    updateChecking: 'Suche…',
    updateFailed: 'Update fehlgeschlagen: {error}',
    updateUnsupported: 'Auto-Update gibt es nur unter Windows. Bitte manuell von der Release-Seite herunterladen.',
  },
};

let currentLocale = 'en';

function t(key, vars) {
  const bundle = STRINGS[currentLocale] || STRINGS.en;
  let s = bundle[key];
  if (s == null) s = STRINGS.en[key] || key;
  if (vars) {
    for (const k in vars) s = s.split('{' + k + '}').join(vars[k]);
  }
  return s;
}

function applyI18n(root) {
  root = root || document;
  root.querySelectorAll('[data-i18n]').forEach(el => {
    el.textContent = t(el.dataset.i18n);
  });
  root.querySelectorAll('[data-i18n-html]').forEach(el => {
    el.innerHTML = t(el.dataset.i18nHtml);
  });
  root.querySelectorAll('[data-i18n-placeholder]').forEach(el => {
    el.setAttribute('placeholder', t(el.dataset.i18nPlaceholder));
  });
  root.querySelectorAll('[data-i18n-title]').forEach(el => {
    el.setAttribute('title', t(el.dataset.i18nTitle));
  });
}

function setLocale(loc) {
  if (loc !== 'de' && loc !== 'en') loc = 'en';
  currentLocale = loc;
  document.documentElement.lang = loc;
  applyI18n();
}

function localizeSystemMessage(text) {
  if (!text) return text;
  let m = /^Joined #(.+)$/.exec(text);
  if (m) return t('joinedChannel', { channel: m[1] });
  m = /^Connected to #(.+)$/.exec(text);
  if (m) return t('connectedChannel', { channel: m[1] });
  return text;
}

window.i18n = { t, applyI18n, setLocale, localizeSystemMessage, get locale() { return currentLocale; } };
