/* =========================================================
   Addiuva · Kiosco de Inscripción (DNI + Nombre) y Ruleta
   ---------------------------------------------------------
   - Identificación única por DNI + Nombre y Apellido completo
   - Triple blindaje anti-duplicado (DNI, Nombre exacto y
     Nombre con orden de palabras invertido)
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
  const DEFAULT_PRIZES = [
    'Kit Corporativo Addiuva',
    'Auriculares Inalámbricos',
    'Día Libre Extra',
    'Cena para Dos Personas',
    'Parlante Bluetooth',
    'Smartwatch',
    'Tablet 10"',
    'Smart TV 50"',
    'Estadía Fin de Semana'
  ];

  function uid() { return Math.random().toString(36).slice(2, 9); }

  function defaultState() {
    return {
      employees: [], // inscriptos reales [{ dni, nombre, normNombre, wordsKey, numero, checkin, premio, ausente }]
      roster: [],    // padrón opcional [{ dni, nombre }]
      prizes: DEFAULT_PRIZES.map((nombre) => ({ id: uid(), nombre, ganador: null })),
      current: 0,
      seq: 0,
      dupes: 0,
      config: {
        adminPass: '2026',
        removeWinners: true,
        showNames: true,
        rosterOnly: false,
        sound: true
      }
    };
  }

  /* ---------------- Normalización estricta de DNI y Nombre ---------------- */
  const cleanDni = (raw) => String(raw || '').replace(/\D/g, '').replace(/^0+/, '');
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
  const sortedWordsKey = (raw) => {
    const words = normName(raw).split(' ').filter(Boolean);
    return words.slice().sort().join('|');
  };
  const titleCaseName = (raw) => String(raw || '')
    .replace(/\s+/g, ' ')
    .trim()
    .split(' ')
    .filter(Boolean)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1).toLowerCase())
    .join(' ');

  /* ---------------- Persistencia (Local + Servidor .exe de Windows) ---------------- */
  let hasNativeServer = false;

  function loadLocal() {
    try {
      const s = JSON.parse(localStorage.getItem(KEY));
      if (s && Array.isArray(s.employees) && s.config) return s;
    } catch (_) { /* ignore */ }
    const s = defaultState();
    try { localStorage.setItem(KEY, JSON.stringify(s)); } catch (_) { /* ignore */ }
    return s;
  }

  let state = loadLocal();

  async function syncFromServer() {
    if (!/^https?:$/.test(location.protocol)) return;
    try {
      const res = await fetch('/api/state', { cache: 'no-store' });
      if (!res.ok) return;
      const remote = await res.json();
      if (remote && Array.isArray(remote.employees)) {
        hasNativeServer = true;
        state = remote;
        try { localStorage.setItem(KEY, JSON.stringify(state)); } catch (_) {}
        renderAll();
      }
    } catch (_) {
      // Ejecutando 100% en navegador sin el .exe
    }
  }

  function save() {
    try { localStorage.setItem(KEY, JSON.stringify(state)); } catch (_) { /* ignore */ }
    if (channel) channel.postMessage('sync');
    if (hasNativeServer) {
      fetch('/api/state', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(state)
      }).catch(() => {});
    }
    renderAll();
  }

  function onExternalChange() {
    state = loadLocal();
    renderAll();
  }
  window.addEventListener('storage', (e) => { if (e.key === KEY) onExternalChange(); });
  if (channel) channel.onmessage = onExternalChange;

  /* ---------------- Wake Lock: mantener PC y pantalla encendidas ---------------- */
  let wakeLock = null;
  async function requestWakeLock() {
    if (!('wakeLock' in navigator)) return;
    try {
      wakeLock = await navigator.wakeLock.request('screen');
      const pill = $('#wake-pill');
      if (pill) pill.textContent = '⚡ Pantalla activa (Sin suspensión)';
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
    // Bloquear F11, F5, F12, Ctrl+R, Ctrl+W, Ctrl+Shift+I, Alt+F4 (cuando el navegador lo permite)
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
  const currentPrize = () => state.prizes[state.current] || null;

  /* ---------------- Reglas Anti-Duplicado (DNI + Nombre Completo) ---------------- */
  function validateCandidate(rawDni, rawNombre) {
    const dni = cleanDni(rawDni);
    if (!dni || dni.length < 6 || dni.length > 10) {
      return { error: 'Ingresá un número de DNI válido (entre 6 y 10 números, sin puntos).' };
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

    const nName = normName(nombre);
    const wKey = sortedWordsKey(nombre);

    // 1. ¿Ya existe este DNI?
    const byDni = state.employees.find((e) => cleanDni(e.dni) === dni);
    if (byDni) {
      return { dupType: 'dni', existing: byDni };
    }

    // 2. ¿Ya existe exactamente este Nombre Completo (o con nombre/apellido invertidos) con otro DNI?
    const byName = state.employees.find((e) =>
      normName(e.nombre) === nName || sortedWordsKey(e.nombre) === wKey
    );
    if (byName) {
      return { dupType: 'name', existing: byName };
    }

    return { ok: true, dni, nombre, nName, wKey };
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

  function nextOpenPrize(from) {
    const n = state.prizes.length;
    for (let k = 1; k <= n; k++) {
      const i = (from + k) % n;
      if (!state.prizes[i].ganador) return i;
    }
    return from;
  }

  /* ---------------- Audio (WebAudio sintetizado) ---------------- */
  let audioCtx = null;
  let lastTick = 0;
  function ac() {
    if (!audioCtx) {
      const C = window.AudioContext || window.webkitAudioContext;
      if (C) audioCtx = new C();
    }
    if (audioCtx && audioCtx.state === 'suspended') audioCtx.resume();
    return audioCtx;
  }
  function blip(freq, dur, type = 'triangle', vol = 0.12, when = 0) {
    const ctx = ac();
    if (!ctx || !state.config.sound) return;
    const t = ctx.currentTime + when;
    const o = ctx.createOscillator();
    const g = ctx.createGain();
    o.type = type;
    o.frequency.setValueAtTime(freq, t);
    g.gain.setValueAtTime(vol, t);
    g.gain.exponentialRampToValueAtTime(0.0001, t + dur);
    o.connect(g).connect(ctx.destination);
    o.start(t);
    o.stop(t + dur + 0.02);
  }
  function tickSound() {
    const now = performance.now();
    if (now - lastTick < 45) return;
    lastTick = now;
    blip(1700, 0.035, 'square', 0.05);
    const p = $('#wheel-pointer');
    p.classList.remove('tick'); void p.offsetWidth; p.classList.add('tick');
  }
  function chimeOk() {
    blip(587.33, 0.14, 'triangle', 0.12, 0);
    blip(880, 0.25, 'triangle', 0.14, 0.11);
  }
  function chimeWarn() {
    blip(260, 0.18, 'sawtooth', 0.09, 0);
    blip(210, 0.22, 'sawtooth', 0.09, 0.15);
  }
  function fanfare() {
    [523.25, 659.25, 783.99, 1046.5].forEach((f, i) => blip(f, 0.22, 'triangle', 0.14, i * 0.12));
    [523.25, 659.25, 783.99, 1046.5].forEach((f) => blip(f, 1.1, 'sine', 0.07, 0.55));
  }

  /* ---------------- Ruleta Canvas ---------------- */
  const wheelCanvas = $('#wheel');
  const wctx = wheelCanvas.getContext('2d');
  const wheel = { rotation: 0, spinning: false, items: [], dirty: false };
  const SEG_COLORS = ['#4B39EC', '#8C46CD', '#E75CE0', '#FFB26B', '#2E1B73', '#B04FD6'];
  let overlayOpen = false;
  let pendingWinner = null;

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

    const seg = TAU / n;
    const useNames = state.config.showNames && n <= 80;
    const fontSize = Math.max(9, Math.min(R * (useNames ? 0.072 : 0.085), R * seg * (useNames ? 0.48 : 0.62)));

    for (let i = 0; i < n; i++) {
      const start = wheel.rotation + i * seg;
      const color = segColor(i, n);
      wctx.beginPath();
      wctx.moveTo(0, 0);
      wctx.arc(0, 0, R, start, start + seg);
      wctx.closePath();
      wctx.fillStyle = color;
      wctx.fill();
      if (n > 1) {
        wctx.strokeStyle = 'rgba(18,8,39,0.35)';
        wctx.lineWidth = Math.max(1, W / 700);
        wctx.stroke();
      }
      const emp = items[i];
      const label = useNames ? `${pad(emp.numero)} · ${shortName(emp.nombre)}` : pad(emp.numero);
      const mid = start + seg / 2;
      const flip = Math.cos(mid) < 0;
      wctx.save();
      wctx.rotate(flip ? mid + Math.PI : mid);
      wctx.textAlign = flip ? 'left' : 'right';
      wctx.textBaseline = 'middle';
      wctx.font = `700 ${fontSize}px "Segoe UI", system-ui, sans-serif`;
      wctx.fillStyle = color === '#FFB26B' ? '#1E1041' : '#FFFFFF';
      wctx.fillText(label, flip ? -R * 0.93 : R * 0.93, 0, R * 0.62);
      wctx.restore();
    }

    const shade = wctx.createRadialGradient(0, 0, R * 0.55, 0, 0, R);
    shade.addColorStop(0, 'rgba(0,0,0,0)');
    shade.addColorStop(1, 'rgba(10,4,30,0.28)');
    wctx.fillStyle = shade;
    wctx.beginPath(); wctx.arc(0, 0, R, 0, TAU); wctx.fill();
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

  function spin() {
    if (wheel.spinning || overlayOpen) return;
    const prize = currentPrize();
    if (!prize) { toast('Agregá premios desde el Panel Admin'); return; }
    if (prize.ganador) { toast('Ese premio ya fue sorteado · elegí otro con las flechas'); return; }
    if (wheel.dirty) syncWheel();
    const items = wheel.items.slice();
    const n = items.length;
    if (!n) { toast('No hay participantes habilitados en la ruleta'); return; }
    ac();

    const w = secureInt(n);
    const seg = TAU / n;
    const targetRel = (w + 0.15 + secureRandom() * 0.7) * seg;
    const targetRot = -Math.PI / 2 - targetRel;
    const start = wheel.rotation;
    const end = start + mod(targetRot - start, TAU) + (7 + secureInt(3)) * TAU;
    const duration = 7000 + secureRandom() * 1400;
    const t0 = performance.now();
    let lastIdx = indexAtPointer(start, n);

    wheel.spinning = true;
    $('#spin-btn').disabled = true;

    const ease = (t) => 1 - Math.pow(1 - t, 4);
    function frame(now) {
      const t = Math.min(1, (now - t0) / duration);
      wheel.rotation = start + (end - start) * ease(t);
      drawWheel();
      const idx = indexAtPointer(wheel.rotation, n);
      if (idx !== lastIdx) { lastIdx = idx; tickSound(); }
      if (t < 1) requestAnimationFrame(frame);
      else {
        wheel.rotation = mod(wheel.rotation, TAU);
        wheel.spinning = false;
        showWinner(items[w], prize);
      }
    }
    requestAnimationFrame(frame);
  }

  function showWinner(emp, prize) {
    pendingWinner = { dni: emp.dni, prizeId: prize.id };
    overlayOpen = true;
    $('#winner-prize').textContent = prize.nombre;
    $('#winner-number').textContent = `#${pad(emp.numero)}`;
    $('#winner-name').textContent = emp.nombre;
    $('#winner-area').textContent = `DNI ${maskDni(emp.dni)}`;
    $('#winner-overlay').classList.add('show');
    fanfare();
    confetti();
    setTimeout(() => $('#winner-confirm').focus(), 60);
  }

  function closeWinner() {
    overlayOpen = false;
    pendingWinner = null;
    $('#winner-overlay').classList.remove('show');
  }

  function confirmWinner() {
    if (!pendingWinner) return;
    const emp = state.employees.find((e) => cleanDni(e.dni) === cleanDni(pendingWinner.dni));
    const prize = state.prizes.find((p) => p.id === pendingWinner.prizeId);
    if (emp && prize) {
      prize.ganador = { dni: emp.dni, nombre: emp.nombre, numero: emp.numero, at: new Date().toISOString() };
      emp.premio = prize.nombre;
      state.current = nextOpenPrize(state.prizes.indexOf(prize));
    }
    closeWinner();
    save();
  }

  function absentWinner() {
    if (!pendingWinner) return;
    const emp = state.employees.find((e) => cleanDni(e.dni) === cleanDni(pendingWinner.dni));
    if (emp) emp.ausente = true;
    closeWinner();
    save();
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

    const recent = pres.slice().sort((a, b) => b.checkin.localeCompare(a.checkin)).slice(0, 8);
    $('#k-feed').innerHTML = recent.map((e) =>
      `<li><span class="num">#${pad(e.numero)}</span><span class="who">${esc(e.nombre)}</span><time>${fmtTime(e.checkin)}</time></li>`).join('');
    $('#k-feed-empty').hidden = recent.length > 0;
  }

  function renderPantalla() {
    const pres = present();
    const elig = eligible();
    $('#count-present').textContent = elig.length;
    $('#count-total').textContent = `/ ${pres.length} inscriptos`;
    $('#count-bar').style.width = pres.length ? `${(elig.length / pres.length) * 100}%` : '0';
    $('#stage-show-names').checked = !!state.config.showNames;

    const recent = pres.slice().sort((a, b) => b.checkin.localeCompare(a.checkin)).slice(0, 8);
    $('#feed').innerHTML = recent.map((e) =>
      `<li><span class="num">#${pad(e.numero)}</span><span class="who">${esc(e.nombre)}</span><time>${fmtTime(e.checkin)}</time></li>`).join('');
    $('#feed-empty').hidden = recent.length > 0;

    const p = currentPrize();
    const done = state.prizes.filter((x) => x.ganador).length;
    $('#current-prize').textContent = !state.prizes.length ? 'Sin premios cargados'
      : done === state.prizes.length ? '¡Sorteo finalizado! 🎉'
      : p ? p.nombre + (p.ganador ? ' (ya sorteado)' : '') : '—';
    $('#prizes-progress').textContent = `${done}/${state.prizes.length}`;
    $('#prize-list').innerHTML = state.prizes.map((x, i) => {
      const cls = [x.ganador ? 'done' : '', i === state.current ? 'current' : ''].join(' ');
      const sub = x.ganador ? `#${pad(x.ganador.numero)} · ${esc(x.ganador.nombre)}` : i === state.current ? 'Sorteando ahora' : 'Por sortear';
      return `<li class="prize-item ${cls}" data-idx="${i}"><span class="idx">${x.ganador ? '✓' : i + 1}</span><span class="pname">${esc(x.nombre)}</span><span class="pwinner">${sub}</span></li>`;
    }).join('');

    syncWheel();
  }

  let filter = 'all';
  function renderAdmin() {
    const pres = present();
    const elig = eligible();
    const done = state.prizes.filter((x) => x.ganador).length;
    $('#st-present').textContent = pres.length;
    $('#st-present-pct').textContent = state.config.rosterOnly && state.roster.length
      ? `Padrón activo (${state.roster.length} en lista)`
      : 'Únicos por DNI + Nombre';
    $('#st-wheel').textContent = elig.length;
    $('#st-prizes').textContent = `${done}/${state.prizes.length}`;
    $('#st-prizes-left').textContent = `${state.prizes.length - done} por sortear`;
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
      let status, action;
      if (e.premio) {
        status = `<span class="chip chip-gold">🏆 Ganó · ${esc(e.premio)}</span>`;
        action = '';
      } else if (e.ausente) {
        status = '<span class="chip chip-danger">Ausente al salir</span>';
        action = `<button class="btn btn-ghost btn-sm" data-act="restore" data-dni="${e.dni}">Reincorporar</button>`;
      } else {
        status = `<span class="chip chip-ok">En ruleta · ${fmtTime(e.checkin)}</span>`;
        action = `<button class="btn btn-ghost btn-sm" data-act="delete" data-dni="${e.dni}">Eliminar</button>`;
      }
      return `<tr><td class="num">#${pad(e.numero)}</td><td class="leg">${formatDni(e.dni)}</td><td><div class="nm">${esc(e.nombre)}</div></td><td>${status}</td><td class="act">${action}</td></tr>`;
    }).join('') : '<tr class="empty-row"><td colspan="5">Todavía no hay personas inscriptas</td></tr>';

    const cfg = state.config;
    if (document.activeElement !== $('#cfg-pass')) $('#cfg-pass').value = cfg.adminPass || '2026';
    $('#cfg-remove').checked = !!cfg.removeWinners;
    $('#cfg-names').checked = !!cfg.showNames;
    $('#cfg-roster-only').checked = !!cfg.rosterOnly;
    $('#cfg-sound').checked = !!cfg.sound;

    $('#prize-count').textContent = `${done}/${state.prizes.length} entregados`;
    $('#prize-edit').innerHTML = state.prizes.map((p) =>
      `<li><span>${esc(p.nombre)}</span>${p.ganador ? `<small>#${pad(p.ganador.numero)} ${esc(shortName(p.ganador.nombre))}</small>` : `<button class="x-btn" data-prize="${p.id}" aria-label="Quitar ${esc(p.nombre)}">✕</button>`}</li>`).join('');
  }

  function renderAll() {
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
  $('#k-nombre').addEventListener('focus', focusNameField);
  $('#k-nombre').addEventListener('input', hideKioskError);

  $('#toggle-osk').addEventListener('change', (e) => {
    $('#osk').hidden = !e.target.checked;
  });
  $('#osk-to-name').addEventListener('click', () => {
    focusNameField();
    $('#k-nombre').focus();
  });
  $('#osk-to-dni').addEventListener('click', () => {
    focusDniField();
    $('#k-dni').focus();
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
    else if (inp.value.length < 10) inp.value += k;
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
    if (check.dupType === 'name') {
      state.dupes = (state.dupes || 0) + 1;
      save();
      return showKioskError(
        `⛔ Ya existe una inscripción para «${check.existing.nombre}» (N.º #${pad(check.existing.numero)}, DNI ${maskDni(check.existing.dni)}). No se permite inscribirse dos veces con otro DNI.`
      );
    }

    pendingCandidate = { dni: check.dni, nombre: check.nombre };
    $('#k-conf-avatar').textContent = initials(check.nombre);
    $('#k-conf-name').textContent = check.nombre;
    $('#k-conf-dni').textContent = formatDni(check.dni);
    setKioskStep('confirm');
  });

  $('#k-back-edit').addEventListener('click', () => {
    setKioskStep('form');
  });

  $('#k-confirm-save').addEventListener('click', () => {
    if (!pendingCandidate) return resetKioskForm();
    const res = registerParticipant(pendingCandidate.dni, pendingCandidate.nombre);
    if (res.error) {
      setKioskStep('form');
      return showKioskError(res.error);
    }
    if (res.dup && res.dupType === 'name') {
      setKioskStep('form');
      return showKioskError(`⛔ «${res.emp.nombre}» ya está inscripto/a con el número #${pad(res.emp.numero)}.`);
    }
    showTicketScreen(res.emp, { isDup: !!res.dup });
  });

  $('#k-next-person').addEventListener('click', resetKioskForm);

  /* ---------------- Bloqueo Kiosco y Contraseña de Administrador ---------------- */
  let authCallback = null;

  function openAuthModal(subtitle, onSuccess) {
    authCallback = onSuccess;
    $('#auth-sub').textContent = subtitle || 'Ingresá la contraseña de Administrador para continuar.';
    $('#auth-input').value = '';
    $('#auth-error').classList.remove('show');
    $('#auth-overlay').classList.add('show');
    setTimeout(() => $('#auth-input').focus(), 50);
  }

  function closeAuthModal() {
    $('#auth-overlay').classList.remove('show');
    authCallback = null;
  }

  function setKioskLocked(locked) {
    kioskLocked = locked;
    document.body.classList.toggle('kiosk-locked', locked);
    if (locked) {
      // En modo bloqueado nos aseguramos de estar en pantalla completa si el navegador lo permite
      if (!document.fullscreenElement && document.documentElement.requestFullscreen) {
        document.documentElement.requestFullscreen().catch(() => {});
      }
    }
  }

  $('#btn-unlock').addEventListener('click', () => {
    openAuthModal('Desbloqueá el Kiosco para pasar a la Ruleta, administrar premios o salir a Windows.', () => {
      setKioskLocked(false);
      toast('🔓 Modo Administrador desbloqueado');
    });
  });

  $('#btn-lock-now').addEventListener('click', () => {
    // Si estaba en Admin, al bloquear vuelve a Inscripción (o deja en Ruleta si estaba en Ruleta)
    if (activeView() === 'view-admin') setView('kiosco');
    setKioskLocked(true);
    toast('🔒 Pantalla bloqueada en modo Kiosco');
  });

  $('#btn-quit-app').addEventListener('click', () => {
    openAuthModal('Confirmá la contraseña para cerrar el Kiosco y salir al escritorio de Windows.', async (enteredPass) => {
      if (hasNativeServer) {
        try {
          await fetch('/api/quit', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ password: enteredPass })
          });
        } catch (_) {}
      }
      if (document.fullscreenElement) {
        document.exitFullscreen?.().catch(() => {});
      }
      window.close();
      toast('Cerrando aplicación…');
    });
  });

  $('#btn-hdmi').addEventListener('click', async () => {
    if (hasNativeServer) {
      try {
        const r = await fetch('/api/open-hdmi', { method: 'POST' });
        if (r.ok) { toast('🖥️ Ventana de Ruleta abierta · arrastrala al proyector HDMI'); return; }
      } catch (_) {}
    }
    window.open(`${location.origin}${location.pathname}?hdmi=1#pantalla`, 'addiuva-hdmi', 'popup,width=1280,height=720');
    toast('🖥️ Ventana HDMI abierta · arrastrala al proyector y presioná F11');
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

  $('#auth-cancel').addEventListener('click', closeAuthModal);

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
    closeAuthModal();
    if (cb) cb(val);
  });

  /* ---------------- Navegación entre vistas ---------------- */
  function setView(name) {
    if (!['kiosco', 'pantalla', 'admin'].includes(name)) name = 'kiosco';
    $$('.tab').forEach((t) => t.setAttribute('aria-selected', String(t.dataset.view === name)));
    $$('.view').forEach((v) => v.classList.toggle('active', v.id === `view-${name}`));
    if (location.hash !== `#${name}`) history.replaceState(null, '', `#${name}`);
    if (name === 'pantalla') requestAnimationFrame(sizeWheel);
  }
  const activeView = () => ($('.view.active') || {}).id;

  $$('.tab').forEach((t) => t.addEventListener('click', () => setView(t.dataset.view)));
  $('#go-ruleta-btn').addEventListener('click', () => setView('pantalla'));
  window.addEventListener('resize', sizeWheel);

  /* ---------------- Eventos de la Ruleta ---------------- */
  $('#spin-btn').addEventListener('click', spin);
  $('#winner-confirm').addEventListener('click', confirmWinner);
  $('#winner-absent').addEventListener('click', absentWinner);
  $('#stage-show-names').addEventListener('change', (e) => {
    state.config.showNames = e.target.checked;
    save();
  });
  $('#prize-prev').addEventListener('click', () => {
    if (!state.prizes.length || wheel.spinning) return;
    state.current = mod(state.current - 1, state.prizes.length);
    save();
  });
  $('#prize-next').addEventListener('click', () => {
    if (!state.prizes.length || wheel.spinning) return;
    state.current = mod(state.current + 1, state.prizes.length);
    save();
  });
  $('#prize-list').addEventListener('click', (e) => {
    const li = e.target.closest('.prize-item');
    if (!li || wheel.spinning) return;
    state.current = Number(li.dataset.idx);
    save();
  });
  document.addEventListener('keydown', (e) => {
    if (activeView() !== 'view-pantalla') return;
    if (/INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName)) return;
    if ($('#auth-overlay').classList.contains('show')) return;
    if (overlayOpen) {
      if (e.key === 'Enter') { e.preventDefault(); confirmWinner(); }
      return;
    }
    if ([' ', 'Enter', 'PageDown', 'ArrowRight', 'ArrowDown'].includes(e.key)) {
      e.preventDefault();
      spin();
    }
    if (e.key === 'f' || e.key === 'F') {
      if (!document.fullscreenElement) document.documentElement.requestFullscreen?.().catch(() => {});
      else if (!kioskLocked) document.exitFullscreen?.();
    }
  });

  /* ---------------- Eventos del Panel Admin ---------------- */
  $('#search').addEventListener('input', renderAdmin);
  $$('.seg button').forEach((b) => b.addEventListener('click', () => {
    filter = b.dataset.filter;
    $$('.seg button').forEach((x) => x.classList.toggle('on', x === b));
    renderAdmin();
  }));

  $('#emp-body').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-act]');
    if (!b) return;
    const dni = b.dataset.dni;
    const idx = state.employees.findIndex((x) => cleanDni(x.dni) === cleanDni(dni));
    if (idx < 0) return;
    const emp = state.employees[idx];
    if (b.dataset.act === 'delete') {
      if (!confirm(`¿Eliminar la inscripción de ${emp.nombre} (DNI ${formatDni(emp.dni)})?`)) return;
      state.employees.splice(idx, 1);
      save();
      toast('Inscripción eliminada');
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
  $('#cfg-sound').addEventListener('change', (e) => { state.config.sound = e.target.checked; save(); });

  $('#prize-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const v = $('#prize-new').value.trim();
    if (!v) return;
    state.prizes.push({ id: uid(), nombre: v, ganador: null });
    $('#prize-new').value = '';
    save();
  });
  $('#prize-edit').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-prize]');
    if (!b) return;
    const i = state.prizes.findIndex((p) => p.id === b.dataset.prize);
    if (i < 0) return;
    state.prizes.splice(i, 1);
    if (state.current >= state.prizes.length) state.current = Math.max(0, state.prizes.length - 1);
    save();
  });

  $('#import-btn').addEventListener('click', () => {
    const lines = $('#import-text').value.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
    const seen = new Set();
    const list = [];
    for (const line of lines) {
      const [rawDni, rawNombre] = line.split(/[,;\t]/).map((x) => (x || '').trim());
      const d = cleanDni(rawDni);
      const n = titleCaseName(rawNombre);
      if (!d || d.length < 6 || !n || seen.has(d)) continue;
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
    if (!confirm('¿Borrar todos los inscriptos y ganadores para empezar de cero?')) return;
    const keepPass = state.config.adminPass;
    state = defaultState();
    state.config.adminPass = keepPass;
    wheel.rotation = 0;
    save();
    toast('Datos reiniciados a cero');
  });

  $('#export-csv').addEventListener('click', () => {
    const head = ['numero', 'dni', 'nombre_completo', 'hora_inscripcion', 'premio_ganado'];
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
  if (new URLSearchParams(location.search).get('hdmi') === '1') {
    document.body.classList.add('hdmi-clean');
    setView('pantalla');
  } else {
    const initial = location.hash.slice(1) || 'kiosco';
    setView(initial);
  }
  focusDniField();
  renderAll();
  syncFromServer();
})();
