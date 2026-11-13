/* =========================================================
   Addiuva · Ruleta del Sorteo de Fin de Año — Prototipo
   ---------------------------------------------------------
   - Sin backend: los datos viven en localStorage del navegador.
   - Las pestañas abiertas en el MISMO navegador se sincronizan
     en vivo (BroadcastChannel + evento "storage"), así se puede
     simular: pestaña 1 = pantalla gigante, pestaña 2 = celular,
     pestaña 3 = recepción.
   - En producción esto se reemplaza por una base de datos en la
     nube con tiempo real (ver docs/PLAN.md).
   ========================================================= */
(() => {
  'use strict';

  const KEY = 'addiuva-ruleta-v1';
  const TAU = Math.PI * 2;
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => Array.from(el.querySelectorAll(s));
  const channel = 'BroadcastChannel' in window ? new BroadcastChannel('addiuva-ruleta') : null;

  /* ---------------- Datos de demo ---------------- */
  const FIRST = ['Sofía', 'Martina', 'Valentina', 'Camila', 'Lucía', 'Julieta', 'Florencia', 'Agustina', 'Paula', 'Carolina',
    'María', 'Ana', 'Laura', 'Daniela', 'Victoria', 'Micaela', 'Rocío', 'Belén', 'Natalia', 'Gabriela',
    'Juan', 'Santiago', 'Mateo', 'Matías', 'Nicolás', 'Tomás', 'Lucas', 'Martín', 'Facundo', 'Joaquín',
    'Diego', 'Federico', 'Pablo', 'Gonzalo', 'Sebastián', 'Alejandro', 'Ramiro', 'Ignacio', 'Franco', 'Andrés'];
  const LAST = ['González', 'Rodríguez', 'Gómez', 'Fernández', 'López', 'Díaz', 'Martínez', 'Pérez', 'García', 'Sánchez',
    'Romero', 'Sosa', 'Álvarez', 'Torres', 'Ruiz', 'Ramírez', 'Flores', 'Acosta', 'Benítez', 'Medina',
    'Herrera', 'Suárez', 'Aguirre', 'Giménez', 'Molina', 'Castro', 'Ortiz', 'Silva', 'Núñez', 'Luna',
    'Juárez', 'Cabrera', 'Ríos', 'Morales', 'Godoy', 'Peralta', 'Vega', 'Méndez', 'Ferreyra', 'Rojas'];
  const AREAS = ['Operaciones', 'Tecnología', 'Contact Center', 'Comercial', 'Finanzas', 'RR.HH.', 'Logística', 'Calidad', 'Marketing', 'Dirección'];
  const DEFAULT_PRIZES = ['Kit Addiuva', 'Auriculares inalámbricos', 'Día libre extra', 'Cena para dos', 'Parlante Bluetooth',
    'Smartwatch', 'Tablet', 'Smart TV 50"', 'Fin de semana en hotel'];

  function mulberry32(a) {
    return function () {
      a |= 0; a = (a + 0x6D2B79F5) | 0;
      let t = Math.imul(a ^ (a >>> 15), 1 | a);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }

  function seedEmployees() {
    const rnd = mulberry32(2026);
    const pick = (arr) => arr[Math.floor(rnd() * arr.length)];
    const used = new Set();
    const list = [];
    for (let i = 0; i < 100; i++) {
      let nombre;
      do { nombre = `${pick(FIRST)} ${pick(LAST)}`; } while (used.has(nombre));
      used.add(nombre);
      list.push(blankEmployee(String(1001 + i), nombre, pick(AREAS)));
    }
    return list;
  }

  function blankEmployee(legajo, nombre, area) {
    return { legajo, nombre, area, numero: null, checkin: null, origen: null, premio: null, ausente: false };
  }

  function uid() { return Math.random().toString(36).slice(2, 9); }

  function defaultState() {
    return {
      employees: seedEmployees(),
      prizes: DEFAULT_PRIZES.map((nombre) => ({ id: uid(), nombre, ganador: null })),
      current: 0,
      seq: 0,
      dupes: 0,
      config: { pin: '2026', removeWinners: true, showNames: false, sound: true },
    };
  }

  /* ---------------- Persistencia + sync entre pestañas ---------------- */
  function load() {
    try {
      const s = JSON.parse(localStorage.getItem(KEY));
      if (s && Array.isArray(s.employees)) return s;
    } catch (_) { /* ignore */ }
    const s = defaultState();
    try { localStorage.setItem(KEY, JSON.stringify(s)); } catch (_) { /* ignore */ }
    return s;
  }

  let state = load();

  function save() {
    try { localStorage.setItem(KEY, JSON.stringify(state)); } catch (_) { /* ignore */ }
    if (channel) channel.postMessage('sync');
    renderAll();
  }

  function onExternalChange() {
    state = load();
    renderAll();
  }
  window.addEventListener('storage', (e) => { if (e.key === KEY) onExternalChange(); });
  if (channel) channel.onmessage = onExternalChange;

  /* ---------------- Utilidades ---------------- */
  const pad = (n) => String(n).padStart(3, '0');
  const fmtTime = (iso) => new Date(iso).toLocaleTimeString('es-AR', { hour: '2-digit', minute: '2-digit', hour12: false });
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const initials = (n) => n.split(/\s+/).map((p) => p[0]).slice(0, 2).join('').toUpperCase();
  const shortName = (n) => { const p = n.split(/\s+/); return p.length > 1 ? `${p[0]} ${p[p.length - 1][0]}.` : n; };
  const mod = (a, n) => ((a % n) + n) % n;
  const secureRandom = () => crypto.getRandomValues(new Uint32Array(1))[0] / 4294967296;
  const secureInt = (n) => Math.floor(secureRandom() * n);

  function toast(msg) {
    const el = document.createElement('div');
    el.className = 'toast';
    el.textContent = msg;
    $('#toasts').appendChild(el);
    setTimeout(() => { el.style.transition = 'opacity .3s'; el.style.opacity = '0'; }, 2400);
    setTimeout(() => el.remove(), 2800);
  }

  const findEmp = (legajo) => state.employees.find((e) => e.legajo === String(legajo).trim());
  const present = () => state.employees.filter((e) => e.checkin);
  const eligible = () => present()
    .filter((e) => !e.ausente && !(state.config.removeWinners && e.premio))
    .sort((a, b) => a.numero - b.numero);
  const currentPrize = () => state.prizes[state.current] || null;

  /* ---------------- Reglas de negocio ---------------- */
  // Un legajo = un número. Si ya hizo check-in, devuelve el mismo número (nunca duplica).
  function checkIn(legajo, origen, { persist = true, countDupe = false } = {}) {
    const emp = findEmp(legajo);
    if (!emp) return { error: 'notfound' };
    if (emp.checkin) {
      if (countDupe) { state.dupes++; if (persist) save(); }
      return { dup: true, emp };
    }
    state.seq += 1;
    emp.numero = state.seq;
    emp.checkin = new Date().toISOString();
    emp.origen = origen;
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

  /* ---------------- Audio (WebAudio, sin archivos) ---------------- */
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
  function fanfare() {
    [523.25, 659.25, 783.99, 1046.5].forEach((f, i) => blip(f, 0.22, 'triangle', 0.14, i * 0.12));
    [523.25, 659.25, 783.99, 1046.5].forEach((f) => blip(f, 1.1, 'sine', 0.07, 0.55));
  }

  /* ---------------- Ruleta ---------------- */
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
    if (n > 1 && i === n - 1 && idx === 0) idx = 2; // evita dos segmentos iguales pegados
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
    const useNames = state.config.showNames && n <= 60;
    const fontSize = Math.max(9, Math.min(R * (useNames ? 0.075 : 0.085), R * seg * (useNames ? 0.5 : 0.62)));

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
      // En la mitad izquierda se gira el texto 180° para que nunca quede de cabeza
      const mid = start + seg / 2;
      const flip = Math.cos(mid) < 0;
      wctx.save();
      wctx.rotate(flip ? mid + Math.PI : mid);
      wctx.textAlign = flip ? 'left' : 'right';
      wctx.textBaseline = 'middle';
      wctx.font = `700 ${fontSize}px Poppins, "Segoe UI", system-ui, sans-serif`;
      wctx.fillStyle = color === '#FFB26B' ? '#1E1041' : '#FFFFFF';
      wctx.fillText(label, flip ? -R * 0.93 : R * 0.93, 0, R * 0.62);
      wctx.restore();
    }

    // profundidad: sombra interior + brillo superior
    const shade = wctx.createRadialGradient(0, 0, R * 0.55, 0, 0, R);
    shade.addColorStop(0, 'rgba(0,0,0,0)');
    shade.addColorStop(1, 'rgba(10,4,30,0.28)');
    wctx.fillStyle = shade;
    wctx.beginPath(); wctx.arc(0, 0, R, 0, TAU); wctx.fill();
    const shine = wctx.createLinearGradient(0, -R, 0, R);
    shine.addColorStop(0, 'rgba(255,255,255,0.12)');
    shine.addColorStop(0.5, 'rgba(255,255,255,0)');
    wctx.fillStyle = shine;
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
    if (!prize) { toast('Agregá premios desde Recepción'); return; }
    if (prize.ganador) { toast('Ese premio ya tiene ganador · elegí otro con las flechas'); return; }
    if (wheel.dirty) syncWheel();
    const items = wheel.items.slice();
    const n = items.length;
    if (!n) { toast('Todavía no hay participantes en la ruleta'); return; }
    ac();

    // El ganador se elige con aleatoriedad criptográfica; la animación solo lo "muestra".
    const w = secureInt(n);
    const seg = TAU / n;
    const targetRel = (w + 0.15 + secureRandom() * 0.7) * seg;
    const targetRot = -Math.PI / 2 - targetRel;
    const start = wheel.rotation;
    const end = start + mod(targetRot - start, TAU) + (7 + secureInt(3)) * TAU;
    const duration = 7200 + secureRandom() * 1600;
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
    pendingWinner = { legajo: emp.legajo, prizeId: prize.id };
    overlayOpen = true;
    $('#winner-prize').textContent = prize.nombre;
    $('#winner-number').textContent = `#${pad(emp.numero)}`;
    $('#winner-name').textContent = emp.nombre;
    $('#winner-area').textContent = emp.area;
    $('#winner-overlay').classList.add('show');
    fanfare();
    confetti();
    setTimeout(() => $('#winner-confirm').focus(), 50);
  }

  function closeWinner() {
    overlayOpen = false;
    pendingWinner = null;
    $('#winner-overlay').classList.remove('show');
  }

  function confirmWinner() {
    if (!pendingWinner) return;
    const emp = findEmp(pendingWinner.legajo);
    const prize = state.prizes.find((p) => p.id === pendingWinner.prizeId);
    if (emp && prize) {
      prize.ganador = { legajo: emp.legajo, nombre: emp.nombre, numero: emp.numero, at: new Date().toISOString() };
      emp.premio = prize.nombre;
      state.current = nextOpenPrize(state.prizes.indexOf(prize));
    }
    closeWinner();
    save();
  }

  function absentWinner() {
    if (!pendingWinner) return;
    const emp = findEmp(pendingWinner.legajo);
    if (emp) emp.ausente = true;
    closeWinner();
    save();
    toast(`${emp ? emp.nombre : 'Participante'} quedó fuera · girando de nuevo…`);
    setTimeout(spin, 900);
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
          r: Math.random() * TAU, vr: (Math.random() - 0.5) * 0.3, c: colors[i % colors.length], life: 0 });
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
      if (now - t0 < 4500) requestAnimationFrame(step);
      else { ctx.clearRect(0, 0, cv.width, cv.height); cv.hidden = true; }
    })(t0);
  }

  /* ---------------- QR ---------------- */
  function checkinUrl() {
    if (/^https?:/.test(location.protocol)) return `${location.origin}${location.pathname}#checkin`;
    return 'https://sorteo.addiuva.com/checkin';
  }
  function drawQR() {
    const cv = $('#qr-canvas');
    const url = checkinUrl();
    $('#qr-url').textContent = url.replace(/^https?:\/\//, '');
    if (typeof window.qrcode !== 'function') return;
    const qr = window.qrcode(0, 'M');
    qr.addData(url);
    qr.make();
    const n = qr.getModuleCount();
    const size = 480;
    cv.width = size; cv.height = size;
    const ctx = cv.getContext('2d');
    const cell = size / n;
    ctx.fillStyle = '#fff'; ctx.fillRect(0, 0, size, size);
    ctx.fillStyle = '#1E1041';
    for (let r = 0; r < n; r++) for (let c = 0; c < n; c++) {
      if (qr.isDark(r, c)) ctx.fillRect(Math.floor(c * cell), Math.floor(r * cell), Math.ceil(cell), Math.ceil(cell));
    }
  }

  /* ---------------- Render: pantalla gigante ---------------- */
  function renderPantalla() {
    const total = state.employees.length;
    const pres = present();
    $('#count-present').textContent = pres.length;
    $('#count-total').textContent = `/ ${total}`;
    $('#count-bar').style.width = total ? `${(pres.length / total) * 100}%` : '0';
    $('#pin-display').textContent = state.config.pin;

    const recent = pres.slice().sort((a, b) => b.checkin.localeCompare(a.checkin)).slice(0, 7);
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

  /* ---------------- Render: check-in móvil ---------------- */
  const DEMO_LEGAJOS = ['1001', '1007', '1013', '1022', '1035', '1041', '1056', '1063', '1070', '1084', '1092', '1100'];
  function renderCheckin() {
    $('#demo-pin').textContent = state.config.pin;
    $('#demo-legajos').innerHTML = DEMO_LEGAJOS.filter(findEmp).map((l) => {
      const e = findEmp(l);
      return `<button type="button" data-legajo="${l}" class="${e.checkin ? 'used' : ''}" title="${esc(e.nombre)}">${l}${e.checkin ? ' ✓' : ''}</button>`;
    }).join('');
  }

  let mEmp = null;
  function mStep(n) { $$('.m-step').forEach((s) => s.classList.toggle('active', s.id === `m-step-${n}`)); }
  function mError(msg) { const el = $('#m-error'); el.textContent = msg; el.classList.remove('show'); void el.offsetWidth; el.classList.add('show'); }
  function mTicket(emp, dup) {
    $('#m-ticket-num').textContent = `#${pad(emp.numero)}`;
    $('#m-ticket-name').textContent = emp.nombre;
    $('#m-ticket-meta').textContent = `${emp.area} · llegó ${fmtTime(emp.checkin)}`;
    const n = $('#m-ticket-notice');
    if (emp.premio) {
      n.className = 'notice notice-ok';
      n.textContent = `🎉 ¡Ganaste: ${emp.premio}! Acercate al escenario.`;
    } else if (dup) {
      n.className = 'notice notice-warn';
      n.textContent = `Ya te habías registrado a las ${fmtTime(emp.checkin)}. Tu número no cambia: cada persona participa una sola vez.`;
    } else {
      n.className = 'notice notice-ok';
      n.textContent = '¡Listo! Ya estás en la ruleta. Guardá esta pantalla por si sale tu número.';
    }
    mStep(3);
  }

  /* ---------------- Render: recepción ---------------- */
  let filter = 'all';
  let flashLegajo = null;
  function renderAdmin() {
    const total = state.employees.length;
    const pres = present();
    const done = state.prizes.filter((x) => x.ganador).length;
    $('#st-present').textContent = pres.length;
    $('#st-present-pct').textContent = `${total ? Math.round((pres.length / total) * 100) : 0}% de ${total} invitados`;
    $('#st-wheel').textContent = eligible().length;
    $('#st-prizes').textContent = `${done}/${state.prizes.length}`;
    $('#st-prizes-left').textContent = `${state.prizes.length - done} por sortear`;
    $('#st-dupes').textContent = state.dupes;

    const q = $('#search').value.trim().toLowerCase();
    const rows = state.employees.filter((e) => {
      if (filter === 'present' && !e.checkin) return false;
      if (filter === 'absent' && e.checkin) return false;
      if (filter === 'winners' && !e.premio) return false;
      if (!q) return true;
      return e.nombre.toLowerCase().includes(q) || e.legajo.includes(q) || (e.numero && pad(e.numero).includes(q)) || e.area.toLowerCase().includes(q);
    }).sort((a, b) => (a.numero || 9999) - (b.numero || 9999) || a.legajo.localeCompare(b.legajo));

    $('#table-count').textContent = `${rows.length} de ${total}`;
    $('#emp-body').innerHTML = rows.length ? rows.map((e) => {
      let status, action;
      if (e.premio) { status = `<span class="chip chip-gold">Ganó · ${esc(e.premio)}</span>`; action = ''; }
      else if (e.ausente) { status = '<span class="chip chip-danger">No estaba al salir sorteado</span>'; action = `<button class="btn btn-ghost btn-sm" data-act="restore" data-legajo="${e.legajo}">Volver a la ruleta</button>`; }
      else if (e.checkin) { status = `<span class="chip chip-ok">Presente · ${fmtTime(e.checkin)}${e.origen === 'recepcion' ? ' · recepción' : ''}</span>`; action = `<button class="btn btn-ghost btn-sm" data-act="undo" data-legajo="${e.legajo}">Deshacer</button>`; }
      else { status = '<span class="chip chip-muted">Sin llegar</span>'; action = `<button class="btn btn-primary btn-sm" data-act="checkin" data-legajo="${e.legajo}">Check-in</button>`; }
      return `<tr class="${e.legajo === flashLegajo ? 'flash' : ''}"><td class="num">${e.numero ? '#' + pad(e.numero) : '—'}</td><td class="leg">${e.legajo}</td><td><div class="nm">${esc(e.nombre)}</div><div class="ar">${esc(e.area)}</div></td><td>${status}</td><td class="act">${action}</td></tr>`;
    }).join('') : '<tr class="empty-row"><td colspan="5">Sin resultados</td></tr>';
    flashLegajo = null;

    const cfg = state.config;
    if (document.activeElement !== $('#cfg-pin')) $('#cfg-pin').value = cfg.pin;
    $('#cfg-remove').checked = cfg.removeWinners;
    $('#cfg-names').checked = cfg.showNames;
    $('#cfg-sound').checked = cfg.sound;

    $('#prize-count').textContent = `${done}/${state.prizes.length} entregados`;
    $('#prize-edit').innerHTML = state.prizes.map((p) =>
      `<li><span>${esc(p.nombre)}</span>${p.ganador ? `<small>#${pad(p.ganador.numero)} ${esc(shortName(p.ganador.nombre))}</small>` : `<button class="x-btn" data-prize="${p.id}" aria-label="Quitar ${esc(p.nombre)}">✕</button>`}</li>`).join('');
  }

  function renderAll() {
    renderPantalla();
    renderCheckin();
    renderAdmin();
  }

  /* ---------------- Navegación ---------------- */
  function setView(name) {
    if (!['pantalla', 'checkin', 'admin'].includes(name)) name = 'pantalla';
    $$('.tab').forEach((t) => t.setAttribute('aria-selected', String(t.dataset.view === name)));
    $$('.view').forEach((v) => v.classList.toggle('active', v.id === `view-${name}`));
    if (location.hash !== `#${name}`) history.replaceState(null, '', `#${name}`);
    if (name === 'pantalla') requestAnimationFrame(sizeWheel);
  }
  const activeView = () => ($('.view.active') || {}).id;

  /* ---------------- Eventos ---------------- */
  $$('.tab').forEach((t) => t.addEventListener('click', () => setView(t.dataset.view)));
  window.addEventListener('hashchange', () => setView(location.hash.slice(1)));
  window.addEventListener('resize', sizeWheel);

  // Pantalla
  $('#spin-btn').addEventListener('click', spin);
  $('#winner-confirm').addEventListener('click', confirmWinner);
  $('#winner-absent').addEventListener('click', absentWinner);
  $('#qr-card').addEventListener('click', () => setView('checkin'));
  $('#prize-prev').addEventListener('click', () => { if (!state.prizes.length || wheel.spinning) return; state.current = mod(state.current - 1, state.prizes.length); save(); });
  $('#prize-next').addEventListener('click', () => { if (!state.prizes.length || wheel.spinning) return; state.current = mod(state.current + 1, state.prizes.length); save(); });
  $('#prize-list').addEventListener('click', (e) => {
    const li = e.target.closest('.prize-item');
    if (!li || wheel.spinning) return;
    state.current = Number(li.dataset.idx); save();
  });
  document.addEventListener('keydown', (e) => {
    if (activeView() !== 'view-pantalla') return;
    if (/INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName)) return;
    if (overlayOpen) {
      if (e.key === 'Enter') { e.preventDefault(); confirmWinner(); }
      return;
    }
    if ([' ', 'Enter', 'PageDown', 'ArrowRight', 'ArrowDown'].includes(e.key)) { e.preventDefault(); spin(); }
    if (e.key === 'f' || e.key === 'F') {
      if (!document.fullscreenElement) document.documentElement.requestFullscreen?.().catch(() => {});
      else document.exitFullscreen?.();
    }
  });

  // Check-in móvil
  $('#m-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const legajo = $('#m-legajo').value.trim();
    const pin = $('#m-pin').value.trim();
    if (!legajo) return mError('Ingresá tu número de legajo.');
    if (pin !== state.config.pin) return mError('PIN incorrecto. Lo ves en la pantalla grande del salón.');
    const emp = findEmp(legajo);
    if (!emp) return mError('No encontramos ese legajo. Acercate a recepción.');
    if (emp.checkin) { checkIn(legajo, 'qr', { countDupe: true }); return mTicket(findEmp(legajo), true); }
    mEmp = emp;
    $('#m-avatar').textContent = initials(emp.nombre);
    $('#m-who-name').textContent = emp.nombre;
    $('#m-who-area').textContent = `${emp.area} · Legajo ${emp.legajo}`;
    $('#m-error').classList.remove('show');
    mStep(2);
  });
  $('#m-confirm').addEventListener('click', () => {
    if (!mEmp) return mStep(1);
    const res = checkIn(mEmp.legajo, 'qr', { countDupe: true });
    if (res.error) { mStep(1); return mError('No se pudo registrar. Probá de nuevo.'); }
    mTicket(findEmp(mEmp.legajo), !!res.dup);
  });
  $('#m-back').addEventListener('click', () => { mEmp = null; mStep(1); });
  $('#m-reset').addEventListener('click', () => {
    mEmp = null; $('#m-legajo').value = ''; $('#m-pin').value = ''; $('#m-error').classList.remove('show'); mStep(1);
  });
  $('#demo-legajos').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-legajo]');
    if (!b) return;
    mEmp = null; mStep(1);
    $('#m-legajo').value = b.dataset.legajo;
    $('#m-pin').value = state.config.pin;
    $('#m-error').classList.remove('show');
    $('#m-submit').focus();
  });
  const tickClock = () => { $('#phone-clock').textContent = new Date().toLocaleTimeString('es-AR', { hour: '2-digit', minute: '2-digit', hour12: false }); };
  tickClock(); setInterval(tickClock, 20000);

  // Recepción
  $('#search').addEventListener('input', renderAdmin);
  $$('.seg button').forEach((b) => b.addEventListener('click', () => {
    filter = b.dataset.filter;
    $$('.seg button').forEach((x) => x.classList.toggle('on', x === b));
    renderAdmin();
  }));
  $('#emp-body').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-act]');
    if (!b) return;
    const emp = findEmp(b.dataset.legajo);
    if (!emp) return;
    if (b.dataset.act === 'checkin') {
      flashLegajo = emp.legajo;
      const r = checkIn(emp.legajo, 'recepcion');
      if (r.emp) toast(`${emp.nombre} → número #${pad(r.emp.numero)}`);
    } else if (b.dataset.act === 'undo') {
      if (!confirm(`¿Deshacer el check-in de ${emp.nombre}? Saldrá de la ruleta.`)) return;
      emp.checkin = null; emp.numero = null; emp.origen = null; save();
    } else if (b.dataset.act === 'restore') {
      emp.ausente = false; save(); toast(`${emp.nombre} volvió a la ruleta`);
    }
  });

  function simulate(n, stagger) {
    const pool = state.employees.filter((e) => !e.checkin).map((e) => e.legajo);
    for (let i = pool.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [pool[i], pool[j]] = [pool[j], pool[i]]; }
    const pick = pool.slice(0, n);
    if (!pick.length) return toast('Ya están todos presentes');
    if (stagger) {
      pick.forEach((l, i) => setTimeout(() => checkIn(l, 'qr'), i * 350));
    } else {
      pick.forEach((l) => checkIn(l, 'qr', { persist: false }));
      save();
    }
    toast(`Simulando ${pick.length} llegadas…`);
  }
  $('#sim-10').addEventListener('click', () => simulate(10, true));
  $('#sim-all').addEventListener('click', () => simulate(state.employees.length, false));

  $('#cfg-pin').addEventListener('input', (e) => {
    const v = e.target.value.replace(/\D/g, '').slice(0, 4);
    e.target.value = v;
    if (v.length === 4) { state.config.pin = v; save(); }
  });
  $('#cfg-pin-random').addEventListener('click', () => {
    state.config.pin = String(1000 + secureInt(9000)); save(); toast(`Nuevo PIN: ${state.config.pin}`);
  });
  $('#cfg-remove').addEventListener('change', (e) => { state.config.removeWinners = e.target.checked; save(); });
  $('#cfg-names').addEventListener('change', (e) => { state.config.showNames = e.target.checked; save(); });
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
      const [legajo, nombre, area] = line.split(/[,;\t]/).map((x) => (x || '').trim());
      if (!legajo || !nombre || /legajo/i.test(legajo)) continue;
      if (seen.has(legajo)) continue; // legajo duplicado en la planilla → se ignora
      seen.add(legajo);
      list.push(blankEmployee(legajo, nombre, area || '—'));
    }
    if (!list.length) return toast('No se encontraron filas válidas');
    if (!confirm(`Se reemplazará la lista por ${list.length} colaboradores y se reiniciarán los check-ins. ¿Continuar?`)) return;
    state.employees = list;
    state.seq = 0; state.dupes = 0; state.current = 0;
    state.prizes.forEach((p) => { p.ganador = null; });
    $('#import-text').value = '';
    save();
    toast(`Lista importada: ${list.length} personas`);
  });
  $('#reset-btn').addEventListener('click', () => {
    if (!confirm('¿Reiniciar la demo? Se borran check-ins, ganadores y cambios.')) return;
    state = defaultState(); wheel.rotation = 0; save(); toast('Demo reiniciada');
  });
  $('#export-csv').addEventListener('click', () => {
    const head = ['legajo', 'nombre', 'area', 'numero', 'hora_checkin', 'origen', 'premio'];
    const rows = state.employees.map((e) => [e.legajo, e.nombre, e.area, e.numero || '', e.checkin ? fmtTime(e.checkin) : '', e.origen || '', e.premio || '']);
    const csv = [head, ...rows].map((r) => r.map((v) => `"${String(v).replace(/"/g, '""')}"`).join(',')).join('\n');
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob(['\ufeff' + csv], { type: 'text/csv;charset=utf-8' }));
    a.download = 'addiuva-sorteo.csv';
    document.body.appendChild(a); a.click(); a.remove();
  });

  // Logo: si no carga la imagen, se muestra el nombre en texto
  $$('.logo-img').forEach((img) => {
    const fallback = () => {
      const s = document.createElement('span');
      s.textContent = 'addiuva';
      s.style.cssText = 'font-weight:700;letter-spacing:-0.02em;font-size:22px;color:#fff;';
      if (img.closest('.wheel-hub')) s.style.fontSize = 'clamp(12px,1.6vw,22px)';
      img.replaceWith(s);
    };
    if (img.complete && img.naturalWidth === 0) fallback();
    else img.addEventListener('error', fallback);
  });

  /* ---------------- Init ---------------- */
  drawQR();
  renderAll();
  setView(location.hash.slice(1) || 'pantalla');
  if (document.fonts && document.fonts.ready) document.fonts.ready.then(drawWheel);
})();
