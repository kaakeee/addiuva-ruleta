/* =========================================================
   Addiuva · Kiosco de Inscripción (DNI + Nombre) y Ruleta
   ---------------------------------------------------------
   - Identificación por DNI (entre 2 y 12 números) + Nombre y Apellido
   - Bloqueo anti-duplicado únicamente por DNI
   - La misma App genera la Ruleta del sorteo
   - Bloqueo Kiosco a pantalla completa con contraseña +
     Screen Wake Lock (mantiene la PC encendida)
   - Sincronización dual: Backend nativo (.exe en Windows) +
     localStorage / BroadcastChannel en navegador
   ========================================================= */
(() => {
  'use strict';

  const KEY = 'addiuva-ruleta-v2';
  const TAU = Math.PI * 2;
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => Array.from(el.querySelectorAll(s));
  const channel = 'BroadcastChannel' in window ? new BroadcastChannel('addiuva-ruleta-v2') : null;

  /* ---------------- Datos iniciales y nombres de prueba ---------------- */
  const FIRST = ['Sofía', 'Martina', 'Valentina', 'Camila', 'Lucía', 'Julieta', 'Florencia', 'Agustina', 'Paula', 'Carolina',
    'María', 'Ana', 'Laura', 'Daniela', 'Victoria', 'Micaela', 'Rocío', 'Belén', 'Natalia', 'Gabriela',
    'Juan', 'Santiago', 'Mateo', 'Matías', 'Nicolás', 'Tomás', 'Lucas', 'Martín', 'Facundo', 'Joaquín',
    'Diego', 'Federico', 'Pablo', 'Gonzalo', 'Sebastián', 'Alejandro', 'Ramiro', 'Ignacio', 'Franco', 'Andrés'];
  const LAST = ['González', 'Rodríguez', 'Gómez', 'Fernández', 'López', 'Díaz', 'Martínez', 'Pérez', 'García', 'Sánchez',
    'Romero', 'Sosa', 'Álvarez', 'Torres', 'Ruiz', 'Ramírez', 'Flores', 'Acosta', 'Benítez', 'Medina',
    'Herrera', 'Suárez', 'Aguirre', 'Giménez', 'Molina', 'Castro', 'Ortiz', 'Silva', 'Núñez', 'Luna'];
  function uid() { return Math.random().toString(36).slice(2, 9); }

  function defaultState() {
    return {
      employees: [], // inscriptos reales [{ dni, nombre, numero, checkin, premio, ausente }]
      roster: [],    // padrón opcional [{ dni, nombre }]
      prizes: [],
      draws: [],     // ganadores confirmados [{ id, round, dni, nombre, numero, at }]
      seq: 0,
      dupes: 0,
      config: {
        adminPass: '2026',
        removeWinners: true,
        showNames: true,
        rosterOnly: false,
        sound: true,
        lockExplorer: true
      }
    };
  }

  function normalizeState(s) {
    if (!s || !Array.isArray(s.employees) || !s.config) return defaultState();
    s.prizes = [];
    if (!Array.isArray(s.draws)) s.draws = [];
    if (typeof s.config.lockExplorer !== 'boolean') s.config.lockExplorer = true;
    return s;
  }

  /* ---------------- Normalización estricta de DNI y Nombre ---------------- */
  const cleanDni = (raw) => String(raw || '').replace(/\D/g, '');
  const formatDni = (dni) => {
    const d = cleanDni(dni);
    return d.replace(/\B(?=(\d{3})+(?!\d))/g, '.');
  };
  const maskDni = (dni) => {
    const d = cleanDni(dni);
    if (d.length <= 3) return d;
    return '•••.' + d.slice(-3);
  };
  const stripAccents = (s) => String(s || '').normalize('NFD').replace(/[\u0300-\u036f]/g, '');
  const normName = (raw) => stripAccents(raw)
    .toLowerCase()
    .replace(/[^a-z\s]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
  const titleCaseName = (raw) => String(raw || '')
    .replace(/\s+/g, ' ')
    .trim()
    .split(' ')
    .filter(Boolean)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1).toLowerCase())
    .join(' ');

  /* ---------------- Persistencia (Local + Servidor .exe de Windows) ---------------- */
  let hasNativeServer = false;
  let liveSupported = null;
  let lastStateVersion = 0;
  let lastLiveId = 0;
  let liveBootstrapped = false;
  let liveSeq = 0;
  let liveBusy = false;
  let syncBusy = false;
  let localSaveSeq = 0;
  let inflightSaves = 0;
  let initialServerSyncPromise = null;
  let pendingAllowEmpty = false;
  const seenLocalLiveIds = new Set();

  function loadLocal() {
    try {
      const s = JSON.parse(localStorage.getItem(KEY));
      if (s && Array.isArray(s.employees) && s.config) {
        const hadLegacyPrizes = Array.isArray(s.prizes) && s.prizes.length > 0;
        const missingDraws = !Array.isArray(s.draws);
        const missingLockExplorer = typeof s.config.lockExplorer !== 'boolean';
        const norm = normalizeState(s);
        if (hadLegacyPrizes || missingDraws || missingLockExplorer) {
          try { localStorage.setItem(KEY, JSON.stringify(norm)); } catch (_) { /* ignore */ }
        }
        return norm;
      }
    } catch (_) { /* ignore */ }
    const s = defaultState();
    try { localStorage.setItem(KEY, JSON.stringify(s)); } catch (_) { /* ignore */ }
    return s;
  }

  let state = loadLocal();

  let lastStateJson = JSON.stringify(state);

  function makeLiveId() {
    liveSeq = (liveSeq + 1) % 1000;
    const id = Date.now() * 1000 + liveSeq;
    seenLocalLiveIds.add(id);
    if (seenLocalLiveIds.size > 60) {
      const oldest = seenLocalLiveIds.values().next().value;
      seenLocalLiveIds.delete(oldest);
    }
    return id;
  }

  function broadcastLive(payload) {
    const ev = { ...payload, id: makeLiveId(), ts: Date.now(), stateVersion: lastStateVersion };
    if (channel) channel.postMessage({ type: 'live', ev });
    if (hasNativeServer && liveSupported !== false) {
      fetch('/api/live', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(ev)
      }).then((r) => {
        if (r.ok) liveSupported = true;
        else if (r.status === 404 || r.status === 405) liveSupported = false;
      }).catch(() => {});
    }
  }

  let shellStatus = {
    supported: false,
    lockExplorer: true,
    explorerRunning: true,
    closedByApp: false,
  };
  let shellApiAvailable = false;
  let shellBusy = false;
  let shellBootCheckScheduled = false;

  function renderShellBox() {
    const cfgLock = $('#cfg-lock-explorer');
    const statusRow = $('#shell-status-row');
    const statusChip = $('#shell-status-chip');
    const toggleBtn = $('#shell-toggle-btn');
    const standaloneNote = $('#shell-standalone-note');
    if (!cfgLock || !statusRow || !statusChip || !toggleBtn || !standaloneNote) return;

    cfgLock.checked = state.config.lockExplorer !== false;
    const canControl = hasNativeServer && !!shellStatus.supported;
    cfgLock.disabled = !canControl || shellBusy;
    statusRow.hidden = !canControl;
    standaloneNote.hidden = canControl;

    if (canControl) {
      const running = !!shellStatus.explorerRunning;
      statusChip.textContent = running ? 'Escritorio activo' : 'Escritorio cerrado';
      statusChip.className = `chip ${running ? 'chip-ok' : 'chip-gold'}`;
      toggleBtn.textContent = running ? 'Cerrar explorer.exe ahora' : 'Restaurar escritorio ahora';
      toggleBtn.disabled = shellBusy;
    }
  }

  async function refreshShellStatus() {
    if (!hasNativeServer || !shellApiAvailable || !/^https?:$/.test(location.protocol)) {
      shellStatus.supported = false;
      renderShellBox();
      return shellStatus;
    }
    try {
      const res = await fetch('/api/shell', { cache: 'no-store' });
      if (res.ok) {
        const data = await res.json();
        if (data && typeof data.supported === 'boolean') {
          shellStatus = data;
          renderShellBox();
          if (shellStatus.supported && shellStatus.explorerRunning &&
              state.config.lockExplorer !== false && !shellBootCheckScheduled) {
            shellBootCheckScheduled = true;
            setTimeout(() => { refreshShellStatus(); }, 1800);
          }
        }
      }
    } catch (_) {}
    return shellStatus;
  }

  async function applyShellAction(action) {
    if (!hasNativeServer || !shellStatus.supported || shellBusy) return;
    shellBusy = true;
    renderShellBox();
    try {
      const res = await fetch('/api/shell', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ action })
      });
      if (res.ok) {
        const data = await res.json();
        if (data && typeof data.supported === 'boolean') {
          shellStatus = data;
        }
        if (action === 'close') {
          toast(!shellStatus.explorerRunning
            ? '🔒 Escritorio de Windows (explorer.exe) cerrado'
            : '⚠️ No se pudo cerrar explorer.exe automáticamente');
        } else {
          toast(shellStatus.explorerRunning
            ? '🖥️ Escritorio de Windows restaurado'
            : '⚠️ No se pudo restaurar explorer.exe automáticamente');
        }
      } else {
        toast('⚠️ No se pudo actualizar el estado de explorer.exe');
      }
    } catch (_) {
      toast('⚠️ Error de comunicación con el Kiosco');
    } finally {
      shellBusy = false;
      renderShellBox();
    }
  }

  async function syncFromServer() {
    if (!/^https?:$/.test(location.protocol) || syncBusy) return false;
    syncBusy = true;
    const saveSeqAtStart = localSaveSeq;
    let runLiveBootstrap = false;
    let shouldRefreshShell = false;
    try {
      const res = await fetch('/api/state', { cache: 'no-store' });
      if (!res.ok) return false;
      const verHdr = Number(res.headers.get('X-State-Version') || 0);
      const shellHdr = res.headers.get('X-Shell-Supported');
      const remote = await res.json();
      if (remote && Array.isArray(remote.employees)) {
        const normalized = normalizeState(remote);
        const firstConnect = !hasNativeServer;
        hasNativeServer = true;
        shellApiAvailable = shellHdr === 'true';
        if (firstConnect && shellApiAvailable) shouldRefreshShell = true;
        if (verHdr > 0) lastStateVersion = Math.max(lastStateVersion, verHdr);
        if (firstConnect && liveSupported === null) runLiveBootstrap = true;
        if (inflightSaves > 0 || saveSeqAtStart !== localSaveSeq) {
          // Si el usuario llegó a tocar algo antes de completar la carga inicial del disco,
          // conservar y fusionar todos los inscriptos ya guardados en servidor.
          if (firstConnect && (normalized.employees.length > 0 || normalized.roster.length > 0 || normalized.draws.length > 0)) {
            const merged = normalizeState(JSON.parse(JSON.stringify(normalized)));
            const seenDnis = new Set(merged.employees.map((e) => cleanDni(e.dni)));
            for (const le of (state.employees || [])) {
              const ld = cleanDni(le.dni);
              if (!ld || seenDnis.has(ld)) continue;
              seenDnis.add(ld);
              let nextNum = Number(le.numero) || 0;
              if (!nextNum || merged.employees.some((x) => x.numero === nextNum)) {
                nextNum = merged.employees.reduce((m, x) => Math.max(m, x.numero || 0), 0) + 1;
              }
              merged.seq = Math.max(merged.seq || 0, nextNum);
              merged.employees.push({ ...le, numero: nextNum });
            }
            if ((state.roster || []).length && !merged.roster.length) merged.roster = state.roster;
            state = merged;
            lastStateJson = JSON.stringify(state);
            try { localStorage.setItem(KEY, lastStateJson); } catch (_) {}
            renderAll();
          }
          return true;
        }
        const remoteJson = JSON.stringify(normalized);
        if (remoteJson !== lastStateJson && !wheel.spinning && !overlayOpen) {
          lastStateJson = remoteJson;
          state = normalized;
          try { localStorage.setItem(KEY, remoteJson); } catch (_) {}
          renderAll();
        } else if (firstConnect) {
          renderShellBox();
        }
        return true;
      }
    } catch (_) {
      // Ejecutando 100% en navegador sin el .exe
    } finally {
      syncBusy = false;
      if (runLiveBootstrap) pollLive();
      if (shouldRefreshShell) refreshShellStatus();
    }
    return false;
  }

  async function pollLive() {
    if (!hasNativeServer || liveSupported === false || liveBusy) return;
    liveBusy = true;
    try {
      const res = await fetch('/api/live', { cache: 'no-store' });
      if (!res.ok) { liveSupported = false; return; }
      const data = await res.json();
      if (!data || typeof data.stateVersion !== 'number') { liveSupported = false; return; }
      liveSupported = true;
      const wasBootstrapped = liveBootstrapped;
      liveBootstrapped = true;
      if (lastStateVersion === 0 && !wasBootstrapped) {
        lastStateVersion = data.stateVersion;
      } else if (data.stateVersion !== lastStateVersion && inflightSaves === 0) {
        if (!wheel.spinning && !overlayOpen) {
          const synced = await syncFromServer();
          if (synced) lastStateVersion = data.stateVersion;
        } else {
          wheel.dirty = true;
        }
      }
      const ev = data.live;
      if (ev && ev.id && ev.id !== lastLiveId) {
        lastLiveId = ev.id;
        if (!wasBootstrapped && (!ev.ts || Math.abs(Date.now() - ev.ts) > 2500)) {
          return;
        }
        if (!seenLocalLiveIds.has(ev.id)) {
          await handleLiveEvent(ev);
        }
      }
    } catch (_) {
      // Ignorar fallo transitorio de sondeo
    } finally {
      liveBootstrapped = true;
      liveBusy = false;
    }
  }

  function save(opts = {}) {
    const allowEmpty = Boolean(opts && opts.allowEmpty);
    const json = JSON.stringify(state);
    lastStateJson = json;
    localSaveSeq++;
    const mySeq = localSaveSeq;
    if (allowEmpty) pendingAllowEmpty = true;
    try { localStorage.setItem(KEY, json); } catch (_) { /* ignore */ }
    if (channel) channel.postMessage({ type: 'sync' });
    if (/^https?:$/.test(location.protocol)) {
      inflightSaves++;
      Promise.resolve(initialServerSyncPromise)
        .then(() => {
          if (!hasNativeServer || localSaveSeq !== mySeq) return null;
          const sendAllowEmpty = pendingAllowEmpty;
          pendingAllowEmpty = false;
          const url = sendAllowEmpty ? '/api/state?allowEmpty=1' : '/api/state';
          const body = JSON.stringify(state);
          return fetch(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body
          });
        })
        .then(async (res) => {
          if (!res) return;
          const verHdr = Number(res.headers.get('X-State-Version') || 0);
          if (verHdr > 0) lastStateVersion = Math.max(lastStateVersion, verHdr);
          if (res.status === 409) {
            await syncFromServer();
          }
        })
        .catch(() => {})
        .finally(() => { inflightSaves = Math.max(0, inflightSaves - 1); });
    }
    renderAll();
  }

  function onExternalChange() {
    if (wheel.spinning || overlayOpen) { wheel.dirty = true; return; }
    state = loadLocal();
    lastStateJson = JSON.stringify(state);
    renderAll();
  }
  window.addEventListener('storage', (e) => { if (e.key === KEY) onExternalChange(); });
  if (channel) {
    channel.onmessage = (e) => {
      const msg = e.data;
      if (msg && msg.type === 'live' && msg.ev) {
        if (!seenLocalLiveIds.has(msg.ev.id)) handleLiveEvent(msg.ev);
      } else {
        onExternalChange();
      }
    };
  }

  /* ---------------- Wake Lock: mantener PC y pantalla encendidas ---------------- */
  let wakeLock = null;
  async function requestWakeLock() {
    if (!('wakeLock' in navigator)) return;
    try {
      wakeLock = await navigator.wakeLock.request('screen');
      wakeLock.addEventListener('release', () => { wakeLock = null; });
    } catch (_) { /* ignore */ }
  }
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') requestWakeLock();
  });
  window.addEventListener('focus', requestWakeLock);
  requestWakeLock();

  /* ---------------- Bloqueo de atajos de salida en modo Kiosco ---------------- */
  let kioskLocked = true;
  document.addEventListener('contextmenu', (e) => {
    if (kioskLocked) e.preventDefault();
  });
  document.addEventListener('keydown', (e) => {
    const k = e.key;
    // En modo desbloqueado se permite F11 para pantalla completa
    if (k === 'F11' && !kioskLocked) return;
    // Bloquear F11 (cuando está bloqueado), F5, F12, Ctrl+R, Ctrl+W, Ctrl+Shift+I, Alt+F4
    if (k === 'F11' || k === 'F5' || k === 'F12' ||
        (e.ctrlKey && ['r', 'R', 'w', 'W', 'u', 'U', 'p', 'P'].includes(k)) ||
        (e.ctrlKey && e.shiftKey && ['i', 'I', 'j', 'J', 'c', 'C'].includes(k)) ||
        (e.altKey && (k === 'F4' || k === 'ArrowLeft' || k === 'ArrowRight'))) {
      e.preventDefault();
      e.stopPropagation();
      return false;
    }
  }, true);

  /* ---------------- Utilidades ---------------- */
  const pad = (n) => String(n).padStart(3, '0');
  const fmtTime = (iso) => new Date(iso).toLocaleTimeString('es-AR', { hour: '2-digit', minute: '2-digit', hour12: false });
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const initials = (n) => String(n || '').split(/\s+/).filter(Boolean).map((p) => p[0]).slice(0, 2).join('').toUpperCase() || 'AD';
  const shortName = (n) => {
    const p = String(n || '').split(/\s+/).filter(Boolean);
    return p.length > 1 ? `${p[0]} ${p[p.length - 1][0]}.` : (p[0] || '');
  };
  const mod = (a, n) => ((a % n) + n) % n;
  const secureRandom = () => crypto.getRandomValues(new Uint32Array(1))[0] / 4294967296;
  const secureInt = (n) => Math.floor(secureRandom() * n);

  function toast(msg) {
    const el = document.createElement('div');
    el.className = 'toast';
    el.textContent = msg;
    $('#toasts').appendChild(el);
    setTimeout(() => { el.style.transition = 'opacity .3s'; el.style.opacity = '0'; }, 2600);
    setTimeout(() => el.remove(), 3000);
  }

  const present = () => state.employees.filter((e) => e.checkin);
  const eligible = () => present()
    .filter((e) => !e.ausente && !(state.config.removeWinners && e.premio))
    .sort((a, b) => a.numero - b.numero);
  const nextRoundNum = (draws = state.draws || []) =>
    draws.reduce((m, d) => Math.max(m, Number(d.round) || 0), 0) + 1;

  /* ---------------- Reglas Anti-Duplicado (solo por DNI, 2 a 12 dígitos) ---------------- */
  function validateCandidate(rawDni, rawNombre) {
    const dni = cleanDni(rawDni);
    if (!dni || dni.length < 2 || dni.length > 12) {
      return { error: 'Ingresá un número de DNI válido (entre 2 y 12 números, sin puntos).' };
    }
    const nombre = titleCaseName(rawNombre);
    const words = normName(nombre).split(' ').filter((w) => w.length >= 2);
    if (words.length < 2) {
      return { error: 'Ingresá tu Nombre y Apellido completos (mínimo nombre y apellido).' };
    }

    // Si está activo el modo Padrón Oficial, verificar que el DNI figure en el padrón
    if (state.config.rosterOnly && state.roster && state.roster.length > 0) {
      const inRoster = state.roster.find((r) => cleanDni(r.dni) === dni);
      if (!inRoster) {
        return { error: `El DNI ${formatDni(dni)} no figura en el padrón habilitado del evento.` };
      }
    }

    // Bloqueo anti-duplicado únicamente por DNI
    const byDni = state.employees.find((e) => cleanDni(e.dni) === dni);
    if (byDni) {
      return { dupType: 'dni', existing: byDni };
    }

    return { ok: true, dni, nombre };
  }

  function registerParticipant(rawDni, rawNombre, { persist = true, countDupe = true } = {}) {
    const check = validateCandidate(rawDni, rawNombre);
    if (check.error) return { error: check.error };
    if (check.dupType) {
      if (countDupe) {
        state.dupes = (state.dupes || 0) + 1;
        if (persist) save();
      }
      return { dup: true, dupType: check.dupType, emp: check.existing };
    }
    state.seq += 1;
    const emp = {
      dni: check.dni,
      nombre: check.nombre,
      numero: state.seq,
      checkin: new Date().toISOString(),
      premio: null,
      ausente: false
    };
    state.employees.push(emp);
    if (persist) save();
    return { emp };
  }

  /* ---------------- Audio sin tildes ni retraso (WebAudio pre-calentado) ---------------- */
  let audioCtx = null;
  let audioBus = null;
  let tickBuffer = null;
  let keepAliveOsc = null;
  let keepAliveGain = null;
  let lastTickAudioTime = -1;
  let lastPointerTickMs = 0;
  let pointerTickFlip = false;
  const wheelPointerEl = $('#wheel-pointer');

  function buildTickBuffer(ctx) {
    const dur = 0.024;
    const len = Math.max(1, Math.ceil(ctx.sampleRate * dur));
    const buf = ctx.createBuffer(1, len, ctx.sampleRate);
    const ch = buf.getChannelData(0);
    const attack = Math.max(1, Math.floor(ctx.sampleRate * 0.0012));
    for (let i = 0; i < len; i++) {
      const t = i / ctx.sampleRate;
      const env = (i < attack ? i / attack : 1) * Math.exp(-t * 205);
      const fadeOut = i > len - 32 ? (len - i) / 32 : 1;
      const freq = 420 + 780 * Math.exp(-t * 170);
      const body = Math.sin(TAU * freq * t);
      const click = Math.sin(TAU * 2350 * t) * Math.exp(-t * 420) * 0.45;
      ch[i] = (body * 0.72 + click) * env * fadeOut * 0.55;
    }
    return buf;
  }

  function syncAudioWarmth() {
    if (!audioCtx || !keepAliveGain) return;
    try {
      const active = !!(state.config.sound && document.visibilityState !== 'hidden');
      keepAliveGain.gain.setValueAtTime(active ? 0.00001 : 0, audioCtx.currentTime);
    } catch (_) {}
  }

  function ac() {
    if (!audioCtx) {
      const C = window.AudioContext || window.webkitAudioContext;
      if (C) {
        try {
          audioCtx = new C({ latencyHint: 'interactive' });
        } catch (_) {
          audioCtx = new C();
        }
        try {
          const comp = audioCtx.createDynamicsCompressor();
          comp.threshold.setValueAtTime(-14, audioCtx.currentTime);
          comp.knee.setValueAtTime(12, audioCtx.currentTime);
          comp.ratio.setValueAtTime(6, audioCtx.currentTime);
          comp.attack.setValueAtTime(0.002, audioCtx.currentTime);
          comp.release.setValueAtTime(0.12, audioCtx.currentTime);
          audioBus = audioCtx.createGain();
          audioBus.gain.setValueAtTime(0.92, audioCtx.currentTime);
          audioBus.connect(comp).connect(audioCtx.destination);
          tickBuffer = buildTickBuffer(audioCtx);

          // Mantiene la salida de audio de Windows activa (0 dB efectivos) para evitar retardo de activación
          keepAliveOsc = audioCtx.createOscillator();
          keepAliveOsc.frequency.setValueAtTime(1, audioCtx.currentTime);
          keepAliveGain = audioCtx.createGain();
          keepAliveGain.gain.setValueAtTime(state.config.sound ? 0.00001 : 0, audioCtx.currentTime);
          keepAliveOsc.connect(keepAliveGain).connect(audioCtx.destination);
          keepAliveOsc.start();
        } catch (_) {}
      }
    }
    if (audioCtx && audioCtx.state === 'suspended') {
      audioCtx.resume().then(syncAudioWarmth).catch(() => {});
    }
    syncAudioWarmth();
    return audioCtx;
  }

  function warmAudio() {
    if (!state.config.sound) return;
    ac();
  }
  ['pointerdown', 'touchstart', 'keydown', 'click'].forEach((evName) => {
    window.addEventListener(evName, warmAudio, { capture: true, passive: true });
  });
  document.addEventListener('visibilitychange', () => {
    if (!audioCtx) return;
    if (document.visibilityState === 'visible' && state.config.sound) ac();
    else syncAudioWarmth();
  });

  function scheduleAudio(fn) {
    if (!state.config.sound) return;
    const ctx = ac();
    if (!ctx) return;
    if (ctx.state === 'running') {
      fn(ctx, ctx.currentTime + 0.012);
    } else {
      ctx.resume().then(() => {
        syncAudioWarmth();
        if (state.config.sound && ctx.state === 'running') {
          fn(ctx, ctx.currentTime + 0.012);
        }
      }).catch(() => {});
    }
  }

  function playTone(ctx, baseTime, freq, dur, type = 'triangle', vol = 0.12, when = 0) {
    const t = baseTime + when;
    const o = ctx.createOscillator();
    const g = ctx.createGain();
    const safeDur = Math.max(0.015, dur);
    o.type = type;
    o.frequency.setValueAtTime(freq, t);
    g.gain.setValueAtTime(0.0001, t);
    g.gain.linearRampToValueAtTime(vol, t + 0.004);
    g.gain.exponentialRampToValueAtTime(0.0001, t + safeDur);
    o.connect(g).connect(audioBus || ctx.destination);
    o.onended = () => { try { o.disconnect(); g.disconnect(); } catch (_) {} };
    o.start(t);
    o.stop(t + safeDur + 0.02);
  }

  function blip(freq, dur, type = 'triangle', vol = 0.12, when = 0) {
    scheduleAudio((ctx, baseT) => playTone(ctx, baseT, freq, dur, type, vol, when));
  }

  function tickSound({ silent = false } = {}) {
    if (!silent && state.config.sound) {
      const ctx = ac();
      if (ctx && ctx.state === 'running' && tickBuffer) {
        const nowAudio = ctx.currentTime;
        const target = nowAudio + 0.012;
        const minNext = (lastTickAudioTime > 0 && lastTickAudioTime < nowAudio + 0.1)
          ? (lastTickAudioTime + 0.042)
          : 0;
        const playAt = Math.max(target, minNext);
        if (playAt - nowAudio <= 0.04) {
          lastTickAudioTime = playAt;
          const src = ctx.createBufferSource();
          const g = ctx.createGain();
          src.buffer = tickBuffer;
          src.playbackRate.setValueAtTime(0.97 + Math.random() * 0.06, playAt);
          g.gain.setValueAtTime(0.32, playAt);
          src.connect(g).connect(audioBus || ctx.destination);
          src.onended = () => { try { src.disconnect(); g.disconnect(); } catch (_) {} };
          src.start(playAt);
        }
      }
    }
    if (wheelPointerEl) {
      const nowMs = performance.now();
      if (nowMs - lastPointerTickMs >= 42) {
        lastPointerTickMs = nowMs;
        pointerTickFlip = !pointerTickFlip;
        wheelPointerEl.classList.remove(pointerTickFlip ? 'tick-b' : 'tick-a');
        wheelPointerEl.classList.add(pointerTickFlip ? 'tick-a' : 'tick-b');
      }
    }
  }

  function chimeOk() {
    scheduleAudio((ctx, baseT) => {
      playTone(ctx, baseT, 587.33, 0.14, 'triangle', 0.12, 0);
      playTone(ctx, baseT, 880, 0.25, 'triangle', 0.14, 0.11);
    });
  }
  function chimeWarn() {
    scheduleAudio((ctx, baseT) => {
      playTone(ctx, baseT, 260, 0.18, 'sawtooth', 0.085, 0);
      playTone(ctx, baseT, 210, 0.22, 'sawtooth', 0.085, 0.15);
    });
  }
  function fanfare() {
    scheduleAudio((ctx, baseT) => {
      [523.25, 659.25, 783.99, 1046.5].forEach((f, i) => playTone(ctx, baseT, f, 0.22, 'triangle', 0.14, i * 0.12));
      [523.25, 659.25, 783.99, 1046.5].forEach((f) => playTone(ctx, baseT, f, 1.05, 'sine', 0.07, 0.52));
    });
  }
  function suspenseChime() {
    scheduleAudio((ctx, baseT) => {
      [329.63, 392.0, 493.88, 659.25].forEach((f, i) => playTone(ctx, baseT, f, 0.2, 'triangle', 0.11, i * 0.11));
    });
  }

  /* ---------------- Ruleta Canvas ---------------- */
  const wheelCanvas = $('#wheel');
  const wctx = wheelCanvas.getContext('2d');
  const wheel = { rotation: 0, spinning: false, items: [], dirty: false };
  const SEG_COLORS = ['#4B39EC', '#8C46CD', '#E75CE0', '#FFB26B', '#2E1B73', '#B04FD6'];
  let wheelSlicesCanvas = null;
  let wheelSlicesKey = '';
  let overlayOpen = false;
  let pendingWinner = null;
  let winnerRevealed = false;
  let revealLockUntil = 0;
  let pendingRemoteReveal = false;

  function sizeWheel() {
    const rect = wheelCanvas.getBoundingClientRect();
    if (!rect.width) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const px = Math.round(rect.width * dpr);
    if (wheelCanvas.width !== px) { wheelCanvas.width = px; wheelCanvas.height = px; }
    drawWheel();
  }

  function segColor(i, n) {
    let idx = i % SEG_COLORS.length;
    if (n > 1 && i === n - 1 && idx === 0) idx = 2;
    return SEG_COLORS[idx];
  }

  function getSlicesCanvas(W, n) {
    const key = `${W}:${n}`;
    if (wheelSlicesCanvas && wheelSlicesKey === key) return wheelSlicesCanvas;
    const cv = wheelSlicesCanvas || document.createElement('canvas');
    cv.width = W;
    cv.height = W;
    const c = cv.getContext('2d');
    const R = W / 2;
    const seg = TAU / n;
    c.clearRect(0, 0, W, W);
    c.save();
    c.translate(R, R);
    for (let i = 0; i < n; i++) {
      const start = i * seg;
      c.beginPath();
      c.moveTo(0, 0);
      c.arc(0, 0, R, start, start + seg);
      c.closePath();
      c.fillStyle = segColor(i, n);
      c.fill();
      if (n > 1) {
        c.strokeStyle = 'rgba(18,8,39,0.35)';
        c.lineWidth = Math.max(1, W / 700);
        c.stroke();
      }
    }
    const shade = c.createRadialGradient(0, 0, R * 0.55, 0, 0, R);
    shade.addColorStop(0, 'rgba(0,0,0,0)');
    shade.addColorStop(1, 'rgba(10,4,30,0.28)');
    c.fillStyle = shade;
    c.beginPath(); c.arc(0, 0, R, 0, TAU); c.fill();
    c.restore();
    wheelSlicesCanvas = cv;
    wheelSlicesKey = key;
    return cv;
  }

  function drawWheel() {
    const W = wheelCanvas.width;
    if (!W) return;
    const R = W / 2;
    const items = wheel.items;
    const n = items.length;
    wctx.clearRect(0, 0, W, W);
    wctx.save();
    wctx.translate(R, R);

    $('#wheel-empty').hidden = n > 0;

    if (n === 0) {
      const g = wctx.createRadialGradient(0, 0, R * 0.1, 0, 0, R);
      g.addColorStop(0, '#2a1862');
      g.addColorStop(1, '#170b38');
      wctx.fillStyle = g;
      wctx.beginPath(); wctx.arc(0, 0, R, 0, TAU); wctx.fill();
      wctx.setLineDash([R * 0.02, R * 0.03]);
      wctx.strokeStyle = 'rgba(255,255,255,0.15)';
      wctx.lineWidth = Math.max(1, W / 500);
      [0.45, 0.7, 0.92].forEach((k) => { wctx.beginPath(); wctx.arc(0, 0, R * k, 0, TAU); wctx.stroke(); });
      wctx.restore();
      return;
    }

    const slices = getSlicesCanvas(W, n);
    wctx.save();
    wctx.rotate(wheel.rotation);
    wctx.drawImage(slices, -R, -R, W, W);
    wctx.restore();

    const seg = TAU / n;
    const useNames = state.config.showNames && n <= 80;
    const fontSize = Math.max(9, Math.min(R * (useNames ? 0.068 : 0.082), R * seg * (useNames ? 0.45 : 0.58)));
    wctx.font = `700 ${fontSize}px "Segoe UI", system-ui, sans-serif`;
    wctx.textBaseline = 'middle';

    for (let i = 0; i < n; i++) {
      const emp = items[i];
      const color = segColor(i, n);
      const label = useNames ? `${pad(emp.numero)} · ${shortName(emp.nombre)}` : pad(emp.numero);
      const mid = wheel.rotation + i * seg + seg / 2;
      const flip = Math.cos(mid) < 0;
      wctx.save();
      wctx.rotate(flip ? mid + Math.PI : mid);
      wctx.textAlign = flip ? 'left' : 'right';
      wctx.fillStyle = color === '#FFB26B' ? '#1E1041' : '#FFFFFF';
      wctx.fillText(label, flip ? -R * 0.94 : R * 0.94, 0, R * 0.47);
      wctx.restore();
    }

    wctx.restore();
  }

  function syncWheel() {
    if (wheel.spinning || overlayOpen) { wheel.dirty = true; return; }
    wheel.items = eligible();
    wheel.dirty = false;
    drawWheel();
    $('#spin-btn').disabled = false;
  }

  function indexAtPointer(rot, n) {
    const seg = TAU / n;
    return Math.floor(mod(-Math.PI / 2 - rot, TAU) / seg) % n;
  }

  function animateWheelSpin({ items, winnerEmp, startRot, endRot, duration, silent = false }) {
    const n = items.length;
    if (!n || !winnerEmp) return;
    if (!silent) ac();
    lastTickAudioTime = -1;

    const t0 = performance.now();
    let lastIdx = indexAtPointer(startRot, n);

    pendingRemoteReveal = false;
    wheel.items = items;
    wheel.rotation = startRot;
    wheel.spinning = true;
    $('#spin-btn').disabled = true;

    const ease = (t) => 1 - Math.pow(1 - t, 4);
    function frame(now) {
      if (!wheel.spinning) return;
      const t = Math.min(1, (now - t0) / duration);
      wheel.rotation = startRot + (endRot - startRot) * ease(t);
      drawWheel();
      const idx = indexAtPointer(wheel.rotation, n);
      if (idx !== lastIdx) { lastIdx = idx; tickSound({ silent }); }
      if (t < 1) requestAnimationFrame(frame);
      else {
        wheel.rotation = mod(wheel.rotation, TAU);
        wheel.spinning = false;
        const doAutoReveal = pendingRemoteReveal;
        pendingRemoteReveal = false;
        showWinner(winnerEmp, { silent });
        if (doAutoReveal) revealWinner({ remote: true });
      }
    }
    requestAnimationFrame(frame);
  }

  function spin() {
    if (wheel.spinning || overlayOpen) return;
    if (wheel.dirty) syncWheel();
    const items = wheel.items.slice();
    const n = items.length;
    if (!n) { toast('No hay participantes habilitados en la ruleta'); return; }

    const w = secureInt(n);
    const seg = TAU / n;
    const targetRel = (w + 0.15 + secureRandom() * 0.7) * seg;
    const targetRot = -Math.PI / 2 - targetRel;
    const startRot = wheel.rotation;
    const endRot = startRot + mod(targetRot - startRot, TAU) + (7 + secureInt(3)) * TAU;
    const duration = 7000 + secureRandom() * 1400;

    broadcastLive({
      action: 'spin',
      dni: items[w].dni,
      startRot,
      endRot,
      duration
    });

    animateWheelSpin({
      items,
      winnerEmp: items[w],
      startRot,
      endRot,
      duration,
      silent: false
    });
  }

  async function handleLiveEvent(ev) {
    if (!ev || !ev.action) return;
    if (ev.action === 'reveal') {
      if (wheel.spinning) { pendingRemoteReveal = true; return; }
      if (overlayOpen && !winnerRevealed) revealWinner({ remote: true });
      return;
    }
    if (ev.action === 'close') {
      pendingRemoteReveal = false;
      wheel.spinning = false;
      if (overlayOpen) closeWinner();
      await syncFromServer();
      syncWheel();
      return;
    }
    if (ev.action === 'spin') {
      if (wheel.spinning) return;
      if (overlayOpen) closeWinner();
      const needsSync = !wheel.items.some((e) => cleanDni(e.dni) === cleanDni(ev.dni)) ||
        (ev.stateVersion && ev.stateVersion !== lastStateVersion);
      if (needsSync && hasNativeServer) {
        await syncFromServer();
      }
      let items = eligible();
      let wIdx = items.findIndex((e) => cleanDni(e.dni) === cleanDni(ev.dni));
      if (wIdx < 0 && hasNativeServer) {
        await new Promise((r) => setTimeout(r, 100));
        await syncFromServer();
        items = eligible();
        wIdx = items.findIndex((e) => cleanDni(e.dni) === cleanDni(ev.dni));
      }
      if (wIdx < 0) return;

      const n = items.length;
      let startRot = typeof ev.startRot === 'number' ? ev.startRot : wheel.rotation;
      let endRot = typeof ev.endRot === 'number' ? ev.endRot : 0;
      const duration = typeof ev.duration === 'number' && ev.duration > 1000 ? ev.duration : 7400;
      if (!(endRot > startRot && indexAtPointer(endRot, n) === wIdx)) {
        const seg = TAU / n;
        const targetRot = -Math.PI / 2 - (wIdx + 0.5) * seg;
        startRot = wheel.rotation;
        endRot = startRot + mod(targetRot - startRot, TAU) + 7 * TAU;
      }

      animateWheelSpin({
        items,
        winnerEmp: items[wIdx],
        startRot,
        endRot,
        duration,
        silent: true
      });
    }
  }

  function showWinner(emp, { silent = false } = {}) {
    const mysteryMode = !state.config.showNames;
    const hintEl = $('#winner-reveal-hint');
    pendingWinner = { dni: emp.dni, nombre: emp.nombre, numero: emp.numero };
    overlayOpen = true;
    $('#winner-number').textContent = `#${pad(emp.numero)}`;

    if (mysteryMode) {
      winnerRevealed = false;
      $('#winner-card').classList.add('is-mystery');
      $('#winner-card').classList.remove('is-revealed');
      $('#winner-eyebrow').textContent = '🔮 ¡NÚMERO GANADOR!';
      $('#mystery-question').textContent = `¿Quién tiene el número #${pad(emp.numero)}?`;
      $('#winner-mystery-box').hidden = false;
      $('#winner-name').textContent = '';
      $('#winner-area').textContent = '';
      $('#winner-name').hidden = true;
      $('#winner-area').hidden = true;
      $('#winner-reveal').hidden = false;
      hintEl.innerHTML = 'Presioná <kbd>Espacio</kbd>, <kbd>Enter</kbd> o tocá el botón para revelar el ganador';
      hintEl.hidden = false;
      $('#winner-confirm').hidden = true;
      $('#winner-absent').hidden = true;
      $('#winner-overlay').classList.add('show');
      if (!silent) suspenseChime();
      $('#winner-reveal').focus({ preventScroll: true });
    } else {
      winnerRevealed = true;
      $('#winner-card').classList.remove('is-mystery');
      $('#winner-card').classList.add('is-revealed');
      $('#winner-eyebrow').textContent = '¡TENEMOS GANADOR!';
      $('#winner-mystery-box').hidden = true;
      $('#winner-name').textContent = emp.nombre;
      $('#winner-area').textContent = `DNI ${maskDni(emp.dni)}`;
      $('#winner-name').hidden = false;
      $('#winner-area').hidden = false;
      $('#winner-reveal').hidden = true;
      hintEl.innerHTML = '<kbd>Enter</kbd> o <kbd>Espacio</kbd> confirmar · <kbd>N</kbd> o <kbd>← / →</kbd> ausente';
      hintEl.hidden = false;
      $('#winner-confirm').hidden = false;
      $('#winner-absent').hidden = false;
      $('#winner-overlay').classList.add('show');
      if (!silent) fanfare();
      confetti();
      $('#winner-confirm').focus({ preventScroll: true });
    }
  }

  function revealWinner(opts) {
    const remote = Boolean(opts && opts.remote);
    if (!pendingWinner || winnerRevealed) return;
    const hintEl = $('#winner-reveal-hint');
    winnerRevealed = true;
    revealLockUntil = performance.now() + 260;
    $('#winner-card').classList.remove('is-mystery');
    $('#winner-card').classList.add('is-revealed');
    $('#winner-eyebrow').textContent = '🎉 ¡TENEMOS GANADOR!';
    $('#winner-mystery-box').hidden = true;
    $('#winner-name').textContent = pendingWinner.nombre;
    $('#winner-area').textContent = `DNI ${maskDni(pendingWinner.dni)}`;
    $('#winner-name').hidden = false;
    $('#winner-area').hidden = false;
    $('#winner-reveal').hidden = true;
    hintEl.innerHTML = '<kbd>Enter</kbd> o <kbd>Espacio</kbd> confirmar · <kbd>N</kbd> o <kbd>← / →</kbd> ausente';
    hintEl.hidden = false;
    $('#winner-confirm').hidden = false;
    $('#winner-absent').hidden = false;
    if (!remote) {
      broadcastLive({ action: 'reveal' });
      fanfare();
    }
    confetti();
    $('#winner-confirm').focus({ preventScroll: true });
  }

  function closeWinner() {
    overlayOpen = false;
    pendingWinner = null;
    winnerRevealed = false;
    $('#winner-overlay').classList.remove('show');
    $('#winner-card').classList.remove('is-mystery', 'is-revealed');
    $('#winner-name').textContent = '';
    $('#winner-area').textContent = '';
    $('#spin-btn').disabled = false;
    if (activeView() === 'view-pantalla') {
      $('#spin-btn').focus({ preventScroll: true });
    } else {
      document.activeElement?.blur?.();
    }
  }

  function confirmWinner() {
    if (!pendingWinner || !winnerRevealed || performance.now() < revealLockUntil) return;
    const emp = state.employees.find((e) => cleanDni(e.dni) === cleanDni(pendingWinner.dni));
    if (emp) {
      if (!Array.isArray(state.draws)) state.draws = [];
      const round = nextRoundNum(state.draws);
      emp.premio = `Ganador #${round}`;
      state.draws.push({
        id: uid(),
        round,
        dni: emp.dni,
        nombre: emp.nombre,
        numero: emp.numero,
        at: new Date().toISOString()
      });
    }
    closeWinner();
    save();
    broadcastLive({ action: 'close' });
  }

  function absentWinner() {
    if (!pendingWinner || !winnerRevealed || performance.now() < revealLockUntil) return;
    const emp = state.employees.find((e) => cleanDni(e.dni) === cleanDni(pendingWinner.dni));
    if (emp) emp.ausente = true;
    closeWinner();
    save();
    broadcastLive({ action: 'close' });
    toast(`${emp ? emp.nombre : 'Participante'} marcado ausente · girando de nuevo…`);
    setTimeout(spin, 850);
  }

  /* ---------------- Confeti ---------------- */
  function confetti() {
    const cv = $('#confetti');
    cv.hidden = false;
    const ctx = cv.getContext('2d');
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    cv.width = innerWidth * dpr; cv.height = innerHeight * dpr;
    const colors = ['#4B39EC', '#8C46CD', '#E75CE0', '#FFCC6E', '#FFB26B', '#FFFFFF'];
    const parts = [];
    const burst = (x, dir) => {
      for (let i = 0; i < 110; i++) {
        const a = (-Math.PI / 2) + dir * (0.25 + Math.random() * 0.6);
        const sp = (12 + Math.random() * 16) * dpr;
        parts.push({ x, y: cv.height, vx: Math.cos(a) * sp, vy: Math.sin(a) * sp, w: (6 + Math.random() * 6) * dpr, h: (10 + Math.random() * 8) * dpr,
          r: Math.random() * TAU, vr: (Math.random() - 0.5) * 0.3, c: colors[i % colors.length] });
      }
    };
    burst(cv.width * 0.08, 1);
    burst(cv.width * 0.92, -1);
    const t0 = performance.now();
    (function step(now) {
      ctx.clearRect(0, 0, cv.width, cv.height);
      parts.forEach((p) => {
        p.vy += 0.38 * dpr; p.vx *= 0.99; p.vy *= 0.99;
        p.x += p.vx; p.y += p.vy; p.r += p.vr;
        ctx.save(); ctx.translate(p.x, p.y); ctx.rotate(p.r);
        ctx.fillStyle = p.c; ctx.fillRect(-p.w / 2, -p.h / 2, p.w, p.h * Math.abs(Math.cos(p.r * 2)));
        ctx.restore();
      });
      if (now - t0 < 4300) requestAnimationFrame(step);
      else { ctx.clearRect(0, 0, cv.width, cv.height); cv.hidden = true; }
    })(t0);
  }

  /* ---------------- Renderizado de las 3 vistas ---------------- */
  function renderKiosco() {
    const pres = present();
    $('#top-count').textContent = pres.length;
    $('#k-count-present').textContent = pres.length;
    const goal = Math.max(100, pres.length);
    $('#k-count-bar').style.width = `${Math.min(100, (pres.length / goal) * 100)}%`;
  }

  function renderPantalla() {
    const pres = present();
    const elig = eligible();
    $('#count-present').textContent = elig.length;
    $('#count-total').textContent = `/ ${pres.length} inscriptos`;
    $('#count-bar').style.width = pres.length ? `${(elig.length / pres.length) * 100}%` : '0';

    const draws = state.draws || [];
    $('#current-round').textContent = `Ronda #${nextRoundNum(draws)}`;
    $('#winners-count').textContent = String(draws.length);
    const ordered = draws.slice().reverse();
    $('#winner-list').innerHTML = ordered.map((d, idx) => {
      const latestCls = idx === 0 ? 'latest' : '';
      const roundNum = d.round || (draws.length - idx);
      const timeStr = d.at ? `Confirmado · ${fmtTime(d.at)}` : 'Ganador confirmado';
      return `<li class="prize-item winner-item done ${latestCls}"><span class="idx">#${roundNum}</span><span class="pname">#${pad(d.numero)} · ${esc(d.nombre)}</span><span class="pwinner">${timeStr}</span></li>`;
    }).join('');

    syncWheel();
  }

  let filter = 'all';
  function renderAdmin() {
    const pres = present();
    const elig = eligible();
    const draws = state.draws || [];
    $('#st-present').textContent = pres.length;
    $('#st-present-pct').textContent = state.config.rosterOnly && state.roster.length
      ? `Padrón activo (${state.roster.length} en lista)`
      : 'Únicos por DNI';
    $('#st-wheel').textContent = elig.length;
    $('#st-winners').textContent = String(draws.length);
    $('#st-dupes').textContent = state.dupes || 0;

    const q = $('#search').value.trim().toLowerCase();
    const rows = state.employees.filter((e) => {
      if (filter === 'present' && (e.ausente || (state.config.removeWinners && e.premio))) return false;
      if (filter === 'winners' && !e.premio) return false;
      if (!q) return true;
      return e.nombre.toLowerCase().includes(q) || e.dni.includes(q) || (e.numero && pad(e.numero).includes(q));
    }).sort((a, b) => (b.numero || 0) - (a.numero || 0));

    $('#table-count').textContent = `${rows.length} de ${pres.length}`;
    $('#emp-body').innerHTML = rows.length ? rows.map((e) => {
      let status;
      const acts = [];
      if (e.premio) {
        status = `<span class="chip chip-gold">🏆 ${esc(e.premio)}</span>`;
      } else if (e.ausente) {
        status = '<span class="chip chip-danger">Ausente al salir</span>';
        acts.push(`<button class="btn btn-ghost btn-sm" data-act="restore" data-dni="${e.dni}">Reincorporar</button>`);
      } else {
        status = `<span class="chip chip-ok">En ruleta · ${fmtTime(e.checkin)}</span>`;
      }
      acts.push(`<button class="btn btn-danger btn-sm" data-act="delete" data-dni="${e.dni}">Eliminar</button>`);
      const action = `<div class="row-acts">${acts.join('')}</div>`;
      return `<tr><td class="num">#${pad(e.numero)}</td><td class="leg">${formatDni(e.dni)}</td><td><div class="nm">${esc(e.nombre)}</div></td><td>${status}</td><td class="act">${action}</td></tr>`;
    }).join('') : '<tr class="empty-row"><td colspan="5">Todavía no hay personas inscriptas</td></tr>';

    const cfg = state.config;
    if (document.activeElement !== $('#cfg-pass')) $('#cfg-pass').value = cfg.adminPass || '2026';
    $('#cfg-remove').checked = !!cfg.removeWinners;
    $('#cfg-names').checked = !!cfg.showNames;
    $('#cfg-roster-only').checked = !!cfg.rosterOnly;
    $('#cfg-sound').checked = !!cfg.sound;
    renderShellBox();
  }

  function renderAll() {
    syncAudioWarmth();
    renderKiosco();
    renderPantalla();
    renderAdmin();
  }

  /* ---------------- Flujo Kiosco Paso 1 → Paso 2 → Paso 3 ---------------- */
  let pendingCandidate = null;
  let countdownTimer = null;

  function setKioskStep(stepId) {
    if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null; }
    $$('.k-step').forEach((s) => s.classList.toggle('active', s.id === `k-step-${stepId}`));
    if (activeView() === 'view-kiosco') {
      if (stepId === 'confirm') $('#k-confirm-save').focus({ preventScroll: true });
      else if (stepId === 'ticket') $('#k-next-person').focus({ preventScroll: true });
    }
  }

  function showKioskError(msg) {
    const el = $('#k-error');
    el.textContent = msg;
    el.classList.remove('show');
    void el.offsetWidth;
    el.classList.add('show');
    chimeWarn();
  }

  function hideKioskError() {
    $('#k-error').classList.remove('show');
  }

  function focusDniField() {
    $('#k-dni').classList.add('focused');
    $('#k-nombre').classList.remove('focused');
    $('#osk-num').hidden = false;
    $('#osk-alpha').hidden = true;
  }

  function focusNameField() {
    $('#k-nombre').classList.add('focused');
    $('#k-dni').classList.remove('focused');
    $('#osk-num').hidden = true;
    $('#osk-alpha').hidden = false;
  }

  function resetKioskForm() {
    pendingCandidate = null;
    $('#k-dni').value = '';
    $('#k-nombre').value = '';
    hideKioskError();
    setKioskStep('form');
    focusDniField();
    if (activeView() === 'view-kiosco') {
      $('#k-dni').focus({ preventScroll: true });
    }
  }

  function showTicketScreen(emp, { isDup = false } = {}) {
    const card = $('#k-ticket-card');
    card.classList.toggle('is-dup', isDup);
    $('#k-ticket-eyebrow').textContent = isDup
      ? '⚠️ DNI YA REGISTRADO · TU NÚMERO ES'
      : '¡INSCRIPCIÓN CONFIRMADA! TU NÚMERO ES';
    $('#k-ticket-num').textContent = `#${pad(emp.numero)}`;
    $('#k-ticket-name').textContent = emp.nombre;
    $('#k-ticket-meta').textContent = `DNI ${formatDni(emp.dni)} · Registrado a las ${fmtTime(emp.checkin)}`;

    const notice = $('#k-ticket-notice');
    if (isDup) {
      notice.className = 'notice notice-warn';
      notice.textContent = `Ya te habías inscripto a las ${fmtTime(emp.checkin)}. El sistema bloquea registros duplicados: conservás tu mismo número #${pad(emp.numero)}.`;
      chimeWarn();
    } else {
      notice.className = 'notice notice-ok';
      notice.textContent = '🎉 ¡Listo! Ya estás dentro de la ruleta de Addiuva. Recordá tu número para el sorteo.';
      chimeOk();
    }

    setKioskStep('ticket');
    let secs = 8;
    $('#k-countdown').textContent = secs;
    countdownTimer = setInterval(() => {
      secs -= 1;
      $('#k-countdown').textContent = secs;
      if (secs <= 0) {
        clearInterval(countdownTimer);
        countdownTimer = null;
        resetKioskForm();
      }
    }, 1000);
  }

  // Autocompletar nombre si el DNI está en el padrón opcional
  $('#k-dni').addEventListener('input', () => {
    $('#k-dni').value = cleanDni($('#k-dni').value);
    hideKioskError();
    if (state.roster && state.roster.length) {
      const hit = state.roster.find((r) => cleanDni(r.dni) === $('#k-dni').value);
      if (hit && !$('#k-nombre').value.trim()) {
        $('#k-nombre').value = hit.nombre;
      }
    }
  });
  $('#k-dni').addEventListener('focus', focusDniField);
  $('#k-dni').addEventListener('click', focusDniField);
  $('#k-nombre').addEventListener('focus', focusNameField);
  $('#k-nombre').addEventListener('click', focusNameField);
  $('#k-nombre').addEventListener('input', hideKioskError);

  function applyOskMode(enabled) {
    $('#osk').hidden = !enabled;
    const dniInp = $('#k-dni');
    const nameInp = $('#k-nombre');
    if (enabled) {
      dniInp.setAttribute('readonly', '');
      nameInp.setAttribute('readonly', '');
      dniInp.setAttribute('inputmode', 'none');
      nameInp.setAttribute('inputmode', 'none');
    } else {
      dniInp.removeAttribute('readonly');
      nameInp.removeAttribute('readonly');
      dniInp.setAttribute('inputmode', 'numeric');
      nameInp.setAttribute('inputmode', 'text');
    }
  }
  applyOskMode($('#toggle-osk').checked);
  // Sacar las teclas táctiles del orden Tab para que el teclado Bluetooth navegue sin vueltas
  $$('#osk button, #auth-numpad button').forEach((b) => { b.tabIndex = -1; });

  $('#osk').addEventListener('mousedown', (e) => {
    if (e.target.closest('button')) e.preventDefault();
  });

  $('#toggle-osk').addEventListener('change', (e) => {
    applyOskMode(e.target.checked);
  });
  $('#osk-to-name').addEventListener('click', () => {
    focusNameField();
    $('#k-nombre').focus({ preventScroll: true });
  });
  $('#osk-to-dni').addEventListener('click', () => {
    focusDniField();
    $('#k-dni').focus({ preventScroll: true });
  });

  // Soporte de teclado físico incluso cuando los campos están en readonly (sin atrapar Tab ni botones)
  document.addEventListener('keydown', (e) => {
    if (activeView() !== 'view-kiosco') return;
    if ($('#auth-overlay').classList.contains('show')) return;
    if ($('#confirm-overlay').classList.contains('show')) return;
    if (e.ctrlKey || e.altKey || e.metaKey) return;

    const ae = document.activeElement;
    const k = e.key;

    if ($('#k-step-confirm').classList.contains('active')) {
      const stale = ae && ae !== document.body && ae.closest('.k-step:not(.active)');
      const onStepBtn = ae === $('#k-back-edit') || ae === $('#k-confirm-save') || !ae || ae === document.body || !!stale;
      if (!onStepBtn) return;
      if (k === 'Escape') { e.preventDefault(); $('#k-back-edit').click(); return; }
      if (k === 'ArrowLeft' || k === 'ArrowRight') {
        e.preventDefault();
        const next = ae === $('#k-back-edit') ? $('#k-confirm-save') : $('#k-back-edit');
        next.focus({ preventScroll: true });
        return;
      }
      if (k === 'Enter' && ae !== $('#k-back-edit')) {
        e.preventDefault();
        $('#k-confirm-save').click();
      }
      return;
    }

    if ($('#k-step-ticket').classList.contains('active')) {
      if (k === 'Escape' || ((k === 'Enter' || k === ' ') && (!ae || ae === document.body))) {
        e.preventDefault();
        resetKioskForm();
      }
      return;
    }

    if (!$('#k-step-form').classList.contains('active')) return;
    const stale = ae && ae !== document.body && ae.closest('.k-step:not(.active)');
    const onKioskField = ae === $('#k-dni') || ae === $('#k-nombre') || !ae || ae === document.body || ae === document.documentElement || !!stale;
    if (!onKioskField) return;

    const dniFocused = ae === $('#k-nombre')
      ? false
      : (ae === $('#k-dni') ? true : $('#k-dni').classList.contains('focused'));

    if (k === 'Enter') {
      e.preventDefault();
      if (dniFocused && !$('#k-nombre').value.trim()) {
        focusNameField();
        $('#k-nombre').focus({ preventScroll: true });
      } else {
        $('#k-form').requestSubmit();
      }
      return;
    }

    if (!$('#toggle-osk').checked) return;

    if (dniFocused) {
      const inp = $('#k-dni');
      if (/^[0-9]$/.test(k)) {
        e.preventDefault();
        if (inp.value.length < 12) { inp.value += k; inp.dispatchEvent(new Event('input')); }
      } else if (k === 'Backspace') {
        e.preventDefault();
        inp.value = inp.value.slice(0, -1);
        inp.dispatchEvent(new Event('input'));
      }
    } else {
      const inp = $('#k-nombre');
      if (k === 'Backspace') {
        e.preventDefault();
        inp.value = inp.value.slice(0, -1);
        hideKioskError();
      } else if (k.length === 1 && /^[a-zA-ZáéíóúüÁÉÍÓÚÜñÑ ]$/.test(k)) {
        e.preventDefault();
        if (inp.value.length < 55) {
          inp.value = (inp.value + k).replace(/\s{2,}/g, ' ');
          hideKioskError();
        }
      }
    }
  });

  // Eventos del teclado numérico táctil
  $('#osk-num').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-key]');
    if (!b) return;
    ac();
    const k = b.dataset.key;
    const inp = $('#k-dni');
    if (k === 'CLEAR') inp.value = '';
    else if (k === 'BACK') inp.value = inp.value.slice(0, -1);
    else if (inp.value.length < 12) inp.value += k;
    inp.dispatchEvent(new Event('input'));
  });

  // Eventos del teclado QWERTY táctil
  $('#osk-alpha').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-key]');
    if (!b) return;
    ac();
    const k = b.dataset.key;
    const inp = $('#k-nombre');
    if (k === 'CLEAR') { inp.value = ''; hideKioskError(); return; }
    if (k === 'BACK') { inp.value = inp.value.slice(0, -1); hideKioskError(); return; }
    if (k === 'SUBMIT') { $('#k-form').requestSubmit(); return; }
    if (inp.value.length < 55) {
      // Capitalizar después de un espacio o al inicio
      const prev = inp.value;
      const ch = (!prev || prev.endsWith(' ')) ? k.toUpperCase() : k.toLowerCase();
      inp.value = (prev + ch).replace(/\s{2,}/g, ' ');
      hideKioskError();
    }
  });

  $('#k-clear').addEventListener('click', resetKioskForm);

  $('#k-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const rawDni = $('#k-dni').value;
    const rawNombre = $('#k-nombre').value;
    const check = validateCandidate(rawDni, rawNombre);
    if (check.error) return showKioskError(check.error);

    if (check.dupType === 'dni') {
      state.dupes = (state.dupes || 0) + 1;
      save();
      return showTicketScreen(check.existing, { isDup: true });
    }

    pendingCandidate = { dni: check.dni, nombre: check.nombre };
    $('#k-conf-avatar').textContent = initials(check.nombre);
    $('#k-conf-name').textContent = check.nombre;
    $('#k-conf-dni').textContent = formatDni(check.dni);
    setKioskStep('confirm');
  });

  $('#k-back-edit').addEventListener('click', () => {
    setKioskStep('form');
    focusDniField();
    requestAnimationFrame(() => $('#k-dni').focus({ preventScroll: true }));
  });

  $('#k-confirm-save').addEventListener('click', () => {
    if (!pendingCandidate) return resetKioskForm();
    const res = registerParticipant(pendingCandidate.dni, pendingCandidate.nombre);
    if (res.error) {
      setKioskStep('form');
      return showKioskError(res.error);
    }
    showTicketScreen(res.emp, { isDup: !!res.dup });
  });

  $('#k-next-person').addEventListener('click', resetKioskForm);

  /* ---------------- Bloqueo Kiosco y Contraseña de Administrador ---------------- */
  let authCallback = null;
  let authReturnFocus = null;

  function openAuthModal(subtitle, onSuccess, submitLabel = 'Desbloquear') {
    authReturnFocus = document.activeElement;
    authCallback = onSuccess;
    $('#auth-sub').textContent = subtitle || 'Ingresá la contraseña de Administrador para continuar.';
    $('#auth-submit').textContent = submitLabel || 'Desbloquear';
    $('#auth-input').value = '';
    $('#auth-error').classList.remove('show');
    $('#auth-overlay').classList.add('show');
    $('#auth-input').focus({ preventScroll: true });
  }

  function closeAuthModal({ restore = true } = {}) {
    $('#auth-overlay').classList.remove('show');
    $('#auth-submit').textContent = 'Desbloquear';
    authCallback = null;
    const rf = authReturnFocus;
    authReturnFocus = null;
    if (restore && rf && typeof rf.focus === 'function' && document.contains(rf)) {
      rf.focus({ preventScroll: true });
    }
  }

  // Soporte de teclado físico también en el modal de contraseña (aunque tenga readonly para evitar teclado táctil de Windows)
  document.addEventListener('keydown', (e) => {
    if (!$('#auth-overlay').classList.contains('show')) return;
    if (e.ctrlKey || e.altKey || e.metaKey) return;
    const inp = $('#auth-input');
    const ae = document.activeElement;
    const k = e.key;
    if (k === 'Escape') { e.preventDefault(); e.stopImmediatePropagation(); closeAuthModal(); return; }
    if (k === 'Tab') {
      e.preventDefault();
      e.stopImmediatePropagation();
      const order = [$('#auth-input'), $('#auth-cancel'), $('#auth-submit')];
      const idx = Math.max(0, order.indexOf(ae));
      const next = order[(idx + (e.shiftKey ? order.length - 1 : 1)) % order.length];
      next.focus({ preventScroll: true });
      return;
    }
    if (k === 'Enter') {
      e.preventDefault();
      e.stopImmediatePropagation();
      if (ae === $('#auth-cancel')) closeAuthModal();
      else $('#auth-form').requestSubmit();
      return;
    }
    if (k === ' ' && (ae === $('#auth-cancel') || ae === $('#auth-submit'))) {
      e.stopImmediatePropagation();
      return;
    }
    if (k === 'Backspace') { e.preventDefault(); e.stopImmediatePropagation(); inp.value = inp.value.slice(0, -1); $('#auth-error').classList.remove('show'); return; }
    if (k.length === 1 && inp.value.length < 20) {
      e.preventDefault();
      e.stopImmediatePropagation();
      inp.value += k;
      $('#auth-error').classList.remove('show');
    }
  });

  /* ---------------- Modal in-app de confirmación ---------------- */
  let confirmCallback = null;
  let confirmReturnFocus = null;
  function openConfirmModal(title, msg, yesLabel, onConfirm) {
    confirmReturnFocus = document.activeElement;
    confirmCallback = onConfirm;
    $('#confirm-title').textContent = title;
    $('#confirm-msg').textContent = msg;
    $('#confirm-yes').textContent = yesLabel || 'Confirmar';
    $('#confirm-overlay').classList.add('show');
    $('#confirm-no').focus({ preventScroll: true });
  }
  function closeConfirmModal({ restore = true } = {}) {
    $('#confirm-overlay').classList.remove('show');
    confirmCallback = null;
    const rf = confirmReturnFocus;
    confirmReturnFocus = null;
    if (restore && rf && typeof rf.focus === 'function' && document.contains(rf)) {
      rf.focus({ preventScroll: true });
    }
  }
  $('#confirm-no').addEventListener('click', () => closeConfirmModal());
  $('#confirm-yes').addEventListener('click', () => {
    const cb = confirmCallback;
    closeConfirmModal({ restore: false });
    if (cb) cb();
  });
  document.addEventListener('keydown', (e) => {
    if (!$('#confirm-overlay').classList.contains('show')) return;
    if (e.key === 'Escape') { e.preventDefault(); e.stopImmediatePropagation(); closeConfirmModal(); return; }
    if (['Tab', 'ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(e.key)) {
      e.preventDefault();
      e.stopImmediatePropagation();
      const next = document.activeElement === $('#confirm-yes') ? $('#confirm-no') : $('#confirm-yes');
      next.focus({ preventScroll: true });
      return;
    }
    if (e.key === 'Enter' || e.key === ' ') {
      e.stopImmediatePropagation();
    }
  });

  function setKioskLocked(locked) {
    kioskLocked = locked;
    document.body.classList.toggle('kiosk-locked', locked);
    if (locked) {
      if (!document.fullscreenElement && document.documentElement.requestFullscreen) {
        document.documentElement.requestFullscreen().catch(() => {});
      }
    }
  }

  function lockKioskNow() {
    if (activeView() === 'view-admin') setView('kiosco');
    setKioskLocked(true);
    toast('🔒 Pantalla bloqueada en modo Kiosco');
    if (activeView() === 'view-pantalla') {
      $('#spin-btn').focus({ preventScroll: true });
    } else {
      focusWorkspaceTarget('kiosco');
    }
  }

  function openUnlockFlow() {
    openAuthModal('Desbloqueá el Kiosco para pasar a la Ruleta, administrar participantes o salir a Windows.', () => {
      setKioskLocked(false);
      toast('🔓 Modo Administrador desbloqueado');
      if (activeView() === 'view-pantalla') {
        $('#spin-btn').focus({ preventScroll: true });
      } else {
        $('#tab-kiosco').focus({ preventScroll: true });
      }
    });
  }

  function toggleKioskLock() {
    if (kioskLocked) openUnlockFlow();
    else lockKioskNow();
  }

  $('#btn-unlock').addEventListener('click', openUnlockFlow);
  $('#btn-lock-now').addEventListener('click', lockKioskNow);

  $('#btn-quit-app').addEventListener('click', () => {
    openAuthModal('Confirmá la contraseña para cerrar el Kiosco y salir al escritorio de Windows.', async (enteredPass) => {
      const isHttp = /^https?:$/.test(location.protocol);
      if (hasNativeServer || isHttp) {
        try {
          const res = await fetch('/api/quit', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ password: enteredPass })
          });
          if (res.status === 403) {
            toast('⚠️ Contraseña incorrecta en el servidor.');
            return;
          }
          if (res.ok) {
            toast('Cerrando aplicación y restaurando escritorio…');
            if (document.fullscreenElement) {
              document.exitFullscreen?.().catch(() => {});
            }
            window.close();
            return;
          }
        } catch (_) {}
      }
      if (document.fullscreenElement) {
        document.exitFullscreen?.().catch(() => {});
      }
      window.close();
      setTimeout(() => {
        toast('Para cerrar esta pestaña del navegador presioná Ctrl + W o Alt + F4.');
      }, 250);
    }, '⏻ Confirmar y Salir');
  });

  $('#auth-numpad').addEventListener('mousedown', (e) => {
    if (e.target.closest('button')) e.preventDefault();
  });
  $('#auth-numpad').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-akey]');
    if (!b) return;
    const k = b.dataset.akey;
    const inp = $('#auth-input');
    if (k === 'CLEAR') inp.value = '';
    else if (k === 'BACK') inp.value = inp.value.slice(0, -1);
    else if (inp.value.length < 20) inp.value += k;
    $('#auth-error').classList.remove('show');
  });

  $('#auth-cancel').addEventListener('click', () => closeAuthModal());

  $('#auth-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const val = $('#auth-input').value.trim();
    const expected = String(state.config.adminPass || '2026').trim();
    if (val !== expected) {
      const err = $('#auth-error');
      err.textContent = 'Contraseña incorrecta.';
      err.classList.remove('show'); void err.offsetWidth; err.classList.add('show');
      chimeWarn();
      return;
    }
    const cb = authCallback;
    closeAuthModal({ restore: false });
    if (cb) cb(val);
  });

  /* ---------------- Utilidades de teclado y navegación entre vistas ---------------- */
  const isAltShortcut = (e) => e.altKey && !e.ctrlKey && !e.metaKey &&
    !(e.getModifierState && e.getModifierState('AltGraph'));
  const isTextInputEl = (el) => !!el && !el.readOnly &&
    (/^(TEXTAREA|SELECT)$/.test(el.tagName) ||
      (el.tagName === 'INPUT' && !['checkbox', 'radio', 'button', 'submit'].includes(el.type)));

  function focusWorkspaceTarget(viewName) {
    if (viewName === 'pantalla') {
      $('#spin-btn').focus({ preventScroll: true });
    } else if (viewName === 'admin') {
      $('#search').focus({ preventScroll: true });
    } else if ($('#k-step-confirm').classList.contains('active')) {
      $('#k-confirm-save').focus({ preventScroll: true });
    } else if ($('#k-step-ticket').classList.contains('active')) {
      $('#k-next-person').focus({ preventScroll: true });
    } else {
      focusDniField();
      $('#k-dni').focus({ preventScroll: true });
    }
  }

  function setView(name) {
    if (!['kiosco', 'pantalla', 'admin'].includes(name)) name = 'kiosco';
    $$('.tab').forEach((t) => t.setAttribute('aria-selected', String(t.dataset.view === name)));
    $$('.view').forEach((v) => v.classList.toggle('active', v.id === `view-${name}`));
    document.body.classList.toggle('in-admin', name === 'admin');
    if (location.hash !== `#${name}`) history.replaceState(null, '', `#${name}`);
    if (name === 'pantalla') requestAnimationFrame(sizeWheel);
    if (name === 'admin') refreshShellStatus();
  }
  const activeView = () => ($('.view.active') || {}).id;

  const topTabs = $$('.tab');
  topTabs.forEach((t) => {
    t.addEventListener('click', () => setView(t.dataset.view));
    t.addEventListener('keydown', (e) => {
      if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
      e.preventDefault();
      const idx = topTabs.indexOf(t);
      const next = topTabs[(idx + (e.key === 'ArrowRight' ? 1 : topTabs.length - 1)) % topTabs.length];
      setView(next.dataset.view);
      next.focus({ preventScroll: true });
    });
  });
  $('#go-ruleta-btn').addEventListener('click', () => {
    setView('pantalla');
    focusWorkspaceTarget('pantalla');
  });
  window.addEventListener('resize', sizeWheel);
  if ('ResizeObserver' in window) {
    new ResizeObserver(() => sizeWheel()).observe($('#wheel-wrap'));
  }
  let pollTick = 0;
  setInterval(() => {
    if (!hasNativeServer) return;
    pollTick++;
    if (liveSupported !== false) pollLive();
    else if (pollTick % 5 === 0) syncFromServer();
  }, 220);

  // Activar switches/checkboxes con Enter además de Espacio
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter' || e.ctrlKey || e.altKey || e.metaKey) return;
    if ($('#auth-overlay').classList.contains('show') || $('#confirm-overlay').classList.contains('show') || overlayOpen) return;
    const ae = document.activeElement;
    if (ae && ae.tagName === 'INPUT' && ae.type === 'checkbox') {
      e.preventDefault();
      e.stopImmediatePropagation();
      ae.click();
    }
  });

  // Atajos globales para operar 100% con teclado Bluetooth (incluido bloquear/desbloquear)
  document.addEventListener('keydown', (e) => {
    if ($('#confirm-overlay').classList.contains('show')) return;

    const kLow = (e.key || '').toLowerCase();
    const isLockHotkey =
      e.key === 'F9' ||
      (isAltShortcut(e) && (kLow === 'b' || kLow === 'l' || e.code === 'KeyB' || e.code === 'KeyL')) ||
      (e.ctrlKey && e.shiftKey && !e.altKey && !e.metaKey && (kLow === 'l' || e.code === 'KeyL'));

    if (isLockHotkey) {
      e.preventDefault();
      e.stopImmediatePropagation();
      if ($('#auth-overlay').classList.contains('show')) closeAuthModal();
      else toggleKioskLock();
      return;
    }

    if (kioskLocked || overlayOpen || $('#auth-overlay').classList.contains('show')) return;
    if (!isAltShortcut(e)) return;

    if (e.key === '1' || e.code === 'Digit1') {
      e.preventDefault();
      setView('kiosco');
      focusWorkspaceTarget('kiosco');
    } else if (e.key === '2' || e.code === 'Digit2') {
      e.preventDefault();
      setView('pantalla');
      focusWorkspaceTarget('pantalla');
    } else if (e.key === '3' || e.code === 'Digit3') {
      e.preventDefault();
      setView('admin');
      focusWorkspaceTarget('admin');
    } else if (kLow === 'q' || e.code === 'KeyQ') {
      e.preventDefault();
      $('#btn-quit-app').click();
    }
  });

  /* ---------------- Eventos de la Ruleta ---------------- */
  $('#spin-btn').addEventListener('click', spin);
  $('#winner-reveal').addEventListener('click', () => revealWinner());
  $('#winner-confirm').addEventListener('click', confirmWinner);
  $('#winner-absent').addEventListener('click', absentWinner);
  document.addEventListener('keydown', (e) => {
    if (activeView() !== 'view-pantalla') return;
    if ($('#auth-overlay').classList.contains('show') || $('#confirm-overlay').classList.contains('show')) return;
    const ae = document.activeElement;
    if (isTextInputEl(ae)) return;

    if (overlayOpen) {
      if (!winnerRevealed) {
        if (e.key === 'Tab') {
          e.preventDefault();
          $('#winner-reveal').focus({ preventScroll: true });
          return;
        }
        if ([' ', 'Enter', 'PageDown', 'ArrowRight', 'ArrowDown'].includes(e.key) &&
            !e.ctrlKey && !e.altKey && !e.metaKey) {
          e.preventDefault();
          e.stopPropagation();
          revealWinner();
        }
        return;
      }
      if (['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Tab'].includes(e.key)) {
        e.preventDefault();
        const nextBtn = ae === $('#winner-absent') ? $('#winner-confirm') : $('#winner-absent');
        nextBtn.focus({ preventScroll: true });
        return;
      }
      if (!e.ctrlKey && !e.altKey && !e.metaKey &&
          ['n', 'N', 'x', 'X', 'Delete', 'Backspace'].includes(e.key)) {
        e.preventDefault();
        e.stopPropagation();
        absentWinner();
        return;
      }
      if (!e.ctrlKey && !e.altKey && !e.metaKey && [' ', 'Enter', 'PageDown'].includes(e.key)) {
        e.preventDefault();
        e.stopPropagation();
        if (ae === $('#winner-absent')) absentWinner();
        else confirmWinner();
      }
      return;
    }

    if (e.ctrlKey || e.altKey || e.metaKey) return;
    const kLow = (e.key || '').toLowerCase();
    if (kLow === 'f') {
      if (!document.fullscreenElement) document.documentElement.requestFullscreen?.().catch(() => {});
      else if (!kioskLocked) document.exitFullscreen?.();
      return;
    }
    if (e.key === 'Escape') {
      if (ae !== $('#spin-btn')) {
        e.preventDefault();
        $('#spin-btn').focus({ preventScroll: true });
      }
      return;
    }
    if (e.key === 'PageDown') {
      e.preventDefault();
      e.stopPropagation();
      spin();
      return;
    }
    // Solo interceptar Espacio / Enter / flechas cuando el foco está en el botón GIRAR o libre,
    // para que Tab -> 🔒 Bloquear Kiosco funcione con Enter/Espacio
    const spinFocusedOrIdle = !ae || ae === document.body || ae === document.documentElement || ae === $('#spin-btn');
    if (spinFocusedOrIdle && [' ', 'Enter', 'ArrowRight', 'ArrowDown'].includes(e.key)) {
      e.preventDefault();
      e.stopPropagation();
      spin();
    }
  });

  /* ---------------- Eventos del Panel Admin ---------------- */
  $('#search').addEventListener('input', renderAdmin);
  const segBtns = $$('.seg button');
  segBtns.forEach((b) => {
    b.addEventListener('click', () => {
      filter = b.dataset.filter;
      segBtns.forEach((x) => x.classList.toggle('on', x === b));
      renderAdmin();
    });
    b.addEventListener('keydown', (e) => {
      if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
      e.preventDefault();
      const idx = segBtns.indexOf(b);
      const next = segBtns[(idx + (e.key === 'ArrowRight' ? 1 : segBtns.length - 1)) % segBtns.length];
      next.focus({ preventScroll: true });
      next.click();
    });
  });

  $('#emp-body').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-act]');
    if (!b) return;
    const dni = b.dataset.dni;
    const idx = state.employees.findIndex((x) => cleanDni(x.dni) === cleanDni(dni));
    if (idx < 0) return;
    const emp = state.employees[idx];
    if (b.dataset.act === 'delete') {
      const extra = emp.premio ? ' También se quitará de la lista de ganadores.' : '';
      openConfirmModal(
        '¿Eliminar participante?',
        `Vas a eliminar a ${emp.nombre} (DNI ${formatDni(emp.dni)}).${extra}`,
        'Sí, eliminar',
        () => {
          state.draws = (state.draws || []).filter((d) => cleanDni(d.dni) !== cleanDni(emp.dni));
          state.employees.splice(idx, 1);
          save({ allowEmpty: state.employees.length === 0 });
          toast(`🗑️ ${emp.nombre} eliminado`);
        }
      );
    } else if (b.dataset.act === 'restore') {
      emp.ausente = false;
      save();
      toast(`${emp.nombre} volvió a la ruleta`);
    }
  });

  function simulateParticipants(count) {
    let added = 0;
    let attempts = 0;
    while (added < count && attempts < count * 5) {
      attempts++;
      const f = FIRST[Math.floor(Math.random() * FIRST.length)];
      const l = LAST[Math.floor(Math.random() * LAST.length)];
      const dni = String(24000000 + Math.floor(Math.random() * 22000000));
      const res = registerParticipant(dni, `${f} ${l}`, { persist: false, countDupe: false });
      if (res.emp && !res.dup) added++;
    }
    save();
    toast(`✓ Se agregaron ${added} inscriptos de prueba`);
  }
  $('#sim-10').addEventListener('click', () => simulateParticipants(10));
  $('#sim-50').addEventListener('click', () => simulateParticipants(50));

  $('#cfg-pass').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      $('#cfg-pass-save').click();
    }
  });
  $('#cfg-pass-save').addEventListener('click', () => {
    const v = $('#cfg-pass').value.trim();
    if (v.length < 3) return toast('La contraseña debe tener al menos 3 caracteres');
    state.config.adminPass = v;
    save();
    toast(`Contraseña actualizada: ${v}`);
  });
  $('#cfg-remove').addEventListener('change', (e) => { state.config.removeWinners = e.target.checked; save(); });
  $('#cfg-names').addEventListener('change', (e) => { state.config.showNames = e.target.checked; save(); });
  $('#cfg-roster-only').addEventListener('change', (e) => { state.config.rosterOnly = e.target.checked; save(); });
  $('#cfg-sound').addEventListener('change', (e) => {
    state.config.sound = e.target.checked;
    syncAudioWarmth();
    save();
  });
  $('#cfg-lock-explorer').addEventListener('change', async (e) => {
    const enabled = !!e.target.checked;
    state.config.lockExplorer = enabled;
    save();
    if (!hasNativeServer || !shellStatus.supported) return;
    await applyShellAction(enabled ? 'close' : 'restore');
  });
  $('#shell-toggle-btn').addEventListener('click', async () => {
    if (!hasNativeServer || !shellStatus.supported || shellBusy) return;
    const action = shellStatus.explorerRunning ? 'close' : 'restore';
    await applyShellAction(action);
  });

  $('#import-btn').addEventListener('click', () => {
    const lines = $('#import-text').value.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
    const seen = new Set();
    const list = [];
    for (const line of lines) {
      const [rawDni, rawNombre] = line.split(/[,;\t]/).map((x) => (x || '').trim());
      const d = cleanDni(rawDni);
      const n = titleCaseName(rawNombre);
      if (!d || d.length < 2 || d.length > 12 || !n || seen.has(d)) continue;
      seen.add(d);
      list.push({ dni: d, nombre: n });
    }
    if (!list.length) return toast('Pegá líneas con formato: DNI, Nombre y Apellido');
    state.roster = list;
    state.config.rosterOnly = true;
    $('#import-text').value = '';
    save();
    toast(`Padrón cargado: ${list.length} personas habilitadas`);
  });

  $('#reset-btn').addEventListener('click', () => {
    openConfirmModal(
      '¿Borrar todo y empezar de cero?',
      'Se eliminarán todos los participantes inscriptos y se reiniciará la lista de ganadores.',
      'Sí, borrar todo',
      () => {
        const keepPass = state.config.adminPass;
        state = defaultState();
        state.config.adminPass = keepPass;
        wheel.rotation = 0;
        save({ allowEmpty: true });
        toast('Datos reiniciados a cero');
      }
    );
  });

  $('#export-csv').addEventListener('click', () => {
    const head = ['numero', 'dni', 'nombre_completo', 'hora_inscripcion', 'resultado'];
    const rows = state.employees.map((e) => [
      e.numero ? pad(e.numero) : '',
      e.dni,
      e.nombre,
      e.checkin ? fmtTime(e.checkin) : '',
      e.premio || ''
    ]);
    const csv = [head, ...rows].map((r) => r.map((v) => `"${String(v).replace(/"/g, '""')}"`).join(',')).join('\n');
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob(['\ufeff' + csv], { type: 'text/csv;charset=utf-8' }));
    a.download = 'addiuva-inscriptos-y-ganadores.csv';
    document.body.appendChild(a);
    a.click();
    a.remove();
  });

  // Fallback de logo
  $$('.logo-img').forEach((img) => {
    const fallback = () => {
      const s = document.createElement('span');
      s.textContent = 'addiuva';
      s.style.cssText = 'font-weight:800;letter-spacing:-0.02em;font-size:22px;color:#fff;';
      img.replaceWith(s);
    };
    if (img.complete && img.naturalWidth === 0) fallback();
    else img.addEventListener('error', fallback);
  });

  /* ---------------- Inicialización ---------------- */
  const initial = location.hash.slice(1) || 'kiosco';
  setView(initial);
  focusWorkspaceTarget(initial);
  renderAll();
  initialServerSyncPromise = syncFromServer();
})();
