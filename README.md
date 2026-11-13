# Addiuva · Ruleta del Sorteo de Fin de Año 🎡

Ruleta digital estilo *Wheel of Names* para el sorteo de fin de año de Addiuva (unas 100 personas), con **check-in por QR** al llegar y protección contra participaciones duplicadas.

> Estado: **prototipo funcional (fase 0)**. Corre 100 % en el navegador, sin servidor. Ver el plan completo en [`docs/PLAN.md`](docs/PLAN.md).

## Cómo verlo

**Opción 1: doble clic**
1. Descargá el repo (botón verde **Code → Download ZIP**) y descomprimilo.
2. Abrí `dist/ruleta-addiuva.html` en Chrome o Edge. Es un único archivo y funciona sin internet.

**Opción 2: servidor local** (para probar el QR desde un celular en la misma red Wi-Fi)
```bash
cd prototipo
python3 -m http.server 8080
# En la laptop abrí http://<IP-de-la-laptop>:8080 (ej. http://192.168.0.15:8080)
# El QR de la pantalla apunta a esa misma dirección: escanealo con el celular.
```

## Las 3 pantallas

| Pantalla | Para qué | Dispositivo |
|---|---|---|
| **Pantalla gigante** | Ruleta, QR de check-in, PIN del evento, contador en vivo y premios | Laptop conectada por HDMI al proyector o TV |
| **Check-in (celular)** | Cada persona carga su legajo y el PIN, confirma “¿Sos vos?” y recibe su número | Celular de cada asistente |
| **Recepción / Admin** | Check-in manual, premios, configuración, importación de Excel y exportación a CSV | Tablet o laptop de RR.HH. |

**Tip:** abrí el archivo en **dos pestañas** (una en *Pantalla gigante* y otra en *Check-in*). Lo que registres en una aparece al instante en la otra.

### Atajos en la pantalla gigante
- `Espacio`, `Enter`, `→` o `PageDown`: girar (compatible con clickers de presentación)
- `F`: pantalla completa
- `Enter` con el ganador en pantalla: confirmar entrega

## Datos de demo
- PIN del evento: **2026** (se cambia en Recepción)
- Legajos válidos: **1001 a 1100** (100 colaboradores ficticios)
- Para cargar la lista real, pegá en Recepción las filas `legajo, nombre, área` copiadas desde Excel.

## ¿Dónde se guardan los datos?
En el prototipo, en el **`localStorage` del navegador** (solo en esa computadora). En producción se reemplaza por una base de datos en la nube (Supabase recomendado) con tiempo real y una restricción única por legajo. Detalle en [`docs/PLAN.md`](docs/PLAN.md).

## Estructura
```
prototipo/
  index.html       # estructura de las 3 vistas
  styles.css       # sistema de diseño (paleta Addiuva)
  app.js           # lógica: ruleta, check-in, recepción y sync entre pestañas
  vendor/qrcode.js # generador de QR (MIT, Kazuhiko Arase)
  assets/          # logo
dist/ruleta-addiuva.html  # versión de un solo archivo (generada)
scripts/build_single.py   # genera dist/
docs/PLAN.md              # plan: web vs app, hardware, arquitectura, fases
```

Regenerar la versión de un solo archivo: `python3 scripts/build_single.py`
