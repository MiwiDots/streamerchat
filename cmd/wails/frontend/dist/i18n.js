// Tiny i18n for StreamerChat. No deps. EN + DE.

const STRINGS = {
  en: {
    settings: 'Settings',
    general: 'General',
    sound: 'Sound',
    twitch: 'Twitch',
    youtube: 'YouTube',
    updates: 'Updates',
    about: 'About',
    done: 'Done',

    // General pane
    display: 'Display',
    language: 'Language',
    showTimestamps: 'Show timestamps',
    showJoinPart: 'Show join/part messages (lurkers entering/leaving)',
    accounts: 'Accounts',
    accountsHint: 'Each profile holds its own Twitch login + YouTube channel. Switch from the dropdown in the titlebar.',
    addAccount: 'Add Account',
    autostart: 'Start StreamerChat automatically with Windows',
    fontSize: 'Chat font size',
    fontSizeHint: 'Tip: Ctrl + / Ctrl - / Ctrl 0 also work.',

    // Sound pane
    chatSound: 'Chat sound',
    chatSoundEnable: 'Play a sound on every chat message',
    pickWav: 'Pick .wav…',
    builtInBeep: 'Use built-in beep',
    none: '(none)',
    volume: 'Volume',
    test: 'Test',

    // Twitch pane
    twitchLogin: 'Twitch Login',
    notLoggedIn: 'Not logged in',
    login: 'Login',
    logout: 'Logout',
    authStep1: '1. Go to',
    authStep2: '2. Enter code:',
    authWaiting: 'Waiting for authorization…',

    // YouTube pane
    ytEnable: 'Enable YouTube live-chat overlay',
    ytStatus: 'Read-only, no login needed. App polls /live every 30s and attaches automatically when you go live.',
    save: 'Save',

    // Updates pane
    updateChannel: 'Update channel:',
    checkForUpdates: 'Check for updates',
    installUpdate: 'Install update',

    // About pane
    version: 'Version',

    // Titlebar / status
    appTitle: 'StreamerChat',
    sendTargetHint: 'Press F2 to cycle send target  |  Click user for mod actions',
    typeMessage: 'Type a message... (F2 to switch target, Esc to close modal)',
    usersCount: 'Users ({count})',
  },
  de: {
    settings: 'Einstellungen',
    general: 'Allgemein',
    sound: 'Ton',
    twitch: 'Twitch',
    youtube: 'YouTube',
    updates: 'Updates',
    about: 'Über',
    done: 'Fertig',

    display: 'Anzeige',
    language: 'Sprache',
    showTimestamps: 'Zeitstempel anzeigen',
    showJoinPart: 'Join/Part-Nachrichten anzeigen (Lurker, die kommen/gehen)',
    accounts: 'Accounts',
    accountsHint: 'Jedes Profil hat eigenen Twitch-Login + YouTube-Channel. Wechseln über das Dropdown in der Titelleiste.',
    addAccount: 'Account hinzufügen',
    autostart: 'StreamerChat automatisch mit Windows starten',
    fontSize: 'Schriftgröße im Chat',
    fontSizeHint: 'Tipp: Strg + / Strg - / Strg 0 funktionieren auch.',

    chatSound: 'Chat-Ton',
    chatSoundEnable: 'Bei jeder Chat-Nachricht einen Ton abspielen',
    pickWav: '.wav auswählen…',
    builtInBeep: 'Eingebauten Ton nutzen',
    none: '(keine)',
    volume: 'Lautstärke',
    test: 'Testen',

    twitchLogin: 'Twitch-Login',
    notLoggedIn: 'Nicht eingeloggt',
    login: 'Einloggen',
    logout: 'Ausloggen',
    authStep1: '1. Öffne',
    authStep2: '2. Code eingeben:',
    authWaiting: 'Warte auf Autorisierung…',

    ytEnable: 'YouTube-Live-Chat-Overlay aktivieren',
    ytStatus: 'Nur lesen, kein Login nötig. App pollt /live alle 30s und verbindet sich automatisch sobald du live gehst.',
    save: 'Speichern',

    updateChannel: 'Update-Kanal:',
    checkForUpdates: 'Nach Updates suchen',
    installUpdate: 'Update installieren',

    version: 'Version',

    appTitle: 'StreamerChat',
    sendTargetHint: 'F2 wechselt das Sendeziel  |  Klick auf User für Mod-Aktionen',
    typeMessage: 'Nachricht eingeben... (F2 wechselt Ziel, Esc schließt Modal)',
    usersCount: 'User ({count})',
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

window.i18n = { t, applyI18n, setLocale, get locale() { return currentLocale; } };
