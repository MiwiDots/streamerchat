// Tiny i18n for StreamerChat. No deps. EN + DE.

const STRINGS = {
  en: {
    emoteSearch: 'Search emotes…',
    emoteScopeChannel: 'Channel',
    emoteScopeGlobal: 'Global',
    emotePickerEmpty: 'No emotes available here.',
    emoteNoMatch: 'No matching emotes.',
    tabPreviewLoading: 'Loading…',
    powerUpGigantify: 'Gigantified emote',
    powerUpEffect: 'Message effect',
    moderationTitle: 'Moderation',
    timeoutPresetsLabel: 'Timeout buttons in the user card (max 8, e.g. 30s, 10m, 1h, 1d, 7d):',
    timeoutPresetsSaved: 'Saved.',
    warn: '⚠ Warn',
    warnSend: 'Send warning',
    warnReasonPlaceholder: 'Reason (shown to the user, required)',
    automodTab: 'AutoMod',
    automodTitle: 'Held by AutoMod',
    automodEmpty: 'Nothing waiting for review.',
    automodHeld: 'AutoMod held a message from {user}',
    automodBlockedTerm: 'blocked term',
    automodBlockedLink: 'blocked link',
    automodLevel: 'level {n}',
    automodAllow: 'Approve',
    automodDeny: 'Deny',
    automodApproved: 'approved',
    automodDenied: 'denied',
    automodExpired: 'expired',
    replyingTo: 'Replying to @{user}',
    reply: 'Reply',
    searchPlaceholder: 'Search…  from:user  has:link  is:first-msg  !from:bot',
    searchCount: '{n} found',
    firstMessage: 'First message in this channel',
    returningChatter: 'Returning chatter',
    watchStreak: 'Watch streak: {n} streams in a row',
    watchStreakNoCount: 'Shared a watch streak',
    highlightedMessage: 'Highlighted message',
    rewardMessage: 'Channel points reward',
    redeemed: '{user} redeemed {reward}',
    redeemedCost: '{n} points',
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
    statsTab: 'Stats',
    statsTitle: 'Most active chatters',
    statsResetTitle: 'Start counting from zero',
    statsEmpty: 'No chat messages yet.',
    statsSummary: 'Messages: {msgs} · Chatters: {chatters}',
    statsSince: 'Counting since {time}',
    statsBotsHidden: 'Bot messages hidden: {n}',
    statsSettingsTitle: 'Chat stats',
    statsIgnoredLabel: 'Hidden from the stats (bots), comma separated. Empty = default list:',
    statsIgnoredSaved: 'Saved.',
    usersTab: 'Users',
    activityTab: 'Activity',
    addAccount: 'Add Account',
    autostart: 'Start StreamerChat automatically with Windows',
    autostartBlocked: 'Windows has switched autostart off (Task Manager → Startup apps). Tick the box to turn it back on.',
    autostartError: 'Autostart could not be changed: {err}',
    fontSize: 'Chat font size',
    fontSizeHint: 'Tip: Ctrl + / Ctrl - / Ctrl 0 also work.',

    // Sound pane
    chatSound: 'Chat sound',
    chatSoundEnable: 'Play a sound on every chat message',
    pickWav: 'Pick .wav…',
    builtInBeep: 'Use built-in beep',
    none: '(none)',
    builtInBeepLabel: '(built-in beep)',
    volume: 'Volume',
    test: 'Test',

    // Twitch pane
    twitchLogin: 'Twitch Login',
    notLoggedIn: 'Not logged in',
    loggedInAs: 'Logged in as @{name}',
    login: 'Login',
    logout: 'Logout',
    authStep1: '1. Go to',
    authStep2: '2. Enter code:',
    authWaiting: 'Waiting for authorization…',

    // YouTube pane
    ytEnable: 'Enable YouTube live-chat overlay',
    ytStatus: 'Reading needs no login. App polls /live every 30s and attaches automatically when you go live.',
    ytLoginTitle: 'YouTube login (to send messages)',
    ytConnect: 'Connect YouTube',
    ytDisconnect: 'Disconnect',
    ytConnected: 'Connected',
    ytNotConnected: 'Not connected',
    ytLoginOpening: 'Login window opening — log in there…',
    ytLoginHint: 'Opens a Microsoft Edge window for the Google login (Windows only). The session is stored in the OS keyring, never in the config file. Switch the send target with F2.',
    ytSendFailed: 'YouTube: message not sent — {err}',
    save: 'Save',
    ytSaved: 'Saved. Polling {handle}…',
    ytDisabled: 'Disabled.',
    ytFailed: 'Failed: {err}',

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
    emoteSearch: 'Emotes suchen…',
    emoteScopeChannel: 'Channel',
    emoteScopeGlobal: 'Global',
    emotePickerEmpty: 'Hier sind keine Emotes verfügbar.',
    emoteNoMatch: 'Keine passenden Emotes.',
    tabPreviewLoading: 'Lädt…',
    powerUpGigantify: 'Gigantisches Emote',
    powerUpEffect: 'Nachrichteneffekt',
    moderationTitle: 'Moderation',
    timeoutPresetsLabel: 'Timeout-Knöpfe in der User-Card (max. 8, z. B. 30s, 10m, 1h, 1d, 7d):',
    timeoutPresetsSaved: 'Gespeichert.',
    warn: '⚠ Verwarnen',
    warnSend: 'Verwarnung senden',
    warnReasonPlaceholder: 'Grund (sieht der User, Pflicht)',
    automodTab: 'AutoMod',
    automodTitle: 'Von AutoMod zurückgehalten',
    automodEmpty: 'Nichts zu prüfen.',
    automodHeld: 'AutoMod hält eine Nachricht von {user} zurück',
    automodBlockedTerm: 'gesperrter Begriff',
    automodBlockedLink: 'gesperrter Link',
    automodLevel: 'Stufe {n}',
    automodAllow: 'Freigeben',
    automodDeny: 'Ablehnen',
    automodApproved: 'freigegeben',
    automodDenied: 'abgelehnt',
    automodExpired: 'abgelaufen',
    replyingTo: 'Antwort an @{user}',
    reply: 'Antworten',
    searchPlaceholder: 'Suchen…  from:user  has:link  is:first-msg  !from:bot',
    searchCount: '{n} Treffer',
    firstMessage: 'Erste Nachricht im Channel',
    returningChatter: 'Wiederkehrender Zuschauer',
    watchStreak: 'Watch-Streak: {n} Streams in Folge',
    watchStreakNoCount: 'Watch-Streak geteilt',
    highlightedMessage: 'Hervorgehobene Nachricht',
    rewardMessage: 'Kanalpunkte-Belohnung',
    redeemed: '{user} hat {reward} eingelöst',
    redeemedCost: '{n} Punkte',
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
    statsTab: 'Stats',
    statsTitle: 'Aktivste Chatter',
    statsResetTitle: 'Zählung neu starten',
    statsEmpty: 'Noch keine Chat-Nachrichten.',
    statsSummary: 'Nachrichten: {msgs} · Chatter: {chatters}',
    statsSince: 'Gezählt seit {time}',
    statsBotsHidden: 'Bot-Nachrichten ausgeblendet: {n}',
    statsSettingsTitle: 'Chat-Statistik',
    statsIgnoredLabel: 'In der Statistik ausgeblendet (Bots), mit Komma getrennt. Leer = Standardliste:',
    statsIgnoredSaved: 'Gespeichert.',
    usersTab: 'Chatter',
    activityTab: 'Aktivität',
    accounts: 'Accounts',
    accountsHint: 'Jedes Profil hat eigenen Twitch-Login + YouTube-Channel. Wechseln über das Dropdown in der Titelleiste.',
    addAccount: 'Account hinzufügen',
    autostart: 'StreamerChat automatisch mit Windows starten',
    autostartBlocked: 'Windows hat den Autostart abgeschaltet (Task-Manager → Autostart). Haken setzen schaltet ihn wieder ein.',
    autostartError: 'Autostart konnte nicht geändert werden: {err}',
    fontSize: 'Schriftgröße im Chat',
    fontSizeHint: 'Tipp: Strg + / Strg - / Strg 0 funktionieren auch.',

    chatSound: 'Chat-Ton',
    chatSoundEnable: 'Bei jeder Chat-Nachricht einen Ton abspielen',
    pickWav: '.wav auswählen…',
    builtInBeep: 'Eingebauten Ton nutzen',
    none: '(keine)',
    builtInBeepLabel: '(eingebauter Ton)',
    volume: 'Lautstärke',
    test: 'Testen',

    twitchLogin: 'Twitch-Login',
    notLoggedIn: 'Nicht eingeloggt',
    loggedInAs: 'Eingeloggt als @{name}',
    login: 'Einloggen',
    logout: 'Ausloggen',
    authStep1: '1. Öffne',
    authStep2: '2. Code eingeben:',
    authWaiting: 'Warte auf Autorisierung…',

    ytEnable: 'YouTube-Live-Chat-Overlay aktivieren',
    ytStatus: 'Lesen braucht keinen Login. App pollt /live alle 30s und verbindet sich automatisch sobald du live gehst.',
    ytLoginTitle: 'YouTube-Login (zum Schreiben)',
    ytConnect: 'YouTube verbinden',
    ytDisconnect: 'Trennen',
    ytConnected: 'Verbunden',
    ytNotConnected: 'Nicht verbunden',
    ytLoginOpening: 'Login-Fenster öffnet sich — dort einloggen…',
    ytLoginHint: 'Öffnet ein Microsoft-Edge-Fenster für den Google-Login (nur Windows). Die Session liegt im Schlüsselbund des Systems, nie in der Config-Datei. Sendeziel mit F2 umschalten.',
    ytSendFailed: 'YouTube: Nachricht nicht gesendet — {err}',
    save: 'Speichern',
    ytSaved: 'Gespeichert. Pollt {handle}…',
    ytDisabled: 'Deaktiviert.',
    ytFailed: 'Fehlgeschlagen: {err}',

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

// Elements whose text is set at runtime (login status, sound file label,
// YouTube save status) carry no data-i18n; app.js registers a callback here
// to re-render them after a language change.
const localeListeners = [];
function onLocaleChange(fn) { localeListeners.push(fn); }

function setLocale(loc) {
  if (loc !== 'de' && loc !== 'en') loc = 'en';
  currentLocale = loc;
  document.documentElement.lang = loc;
  applyI18n();
  for (const fn of localeListeners) {
    try { fn(loc); } catch (e) { console.error('locale listener', e); }
  }
}

window.i18n = { t, applyI18n, setLocale, onLocaleChange, get locale() { return currentLocale; } };
