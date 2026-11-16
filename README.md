# Addiuva · Kiosco de Inscripción (DNI + Nombre) y Ruleta Digital 🎡

Aplicación todo-en-uno para el sorteo de fin de año de **Addiuva** (~100 personas), diseñada especialmente para una **notebook táctil (Lenovo Yoga) + salida HDMI**.

---

## 📦 Descarga directa para Windows (`.exe` único autónomo)

**`AddiuvaRuleta.exe` es un único archivo ejecutable** (ya trae incrustada toda la aplicación, estilos, teclado táctil y ruleta; no necesita otros archivos para funcionar):

- 🚀 **Descarga directa (`.exe` único):** **[`dist/AddiuvaRuleta.exe`](dist/AddiuvaRuleta.exe)** *(solo descargar y hacer doble clic)*
- 📦 **Alternativa comprimida (`.zip`):** **[`dist/AddiuvaRuleta-Windows-x64.zip`](dist/AddiuvaRuleta-Windows-x64.zip)** *(contiene únicamente `AddiuvaRuleta.exe`, útil si el navegador bloquea descargar `.exe` sueltos)*
- También disponible en la sección **Releases** de este repositorio en GitHub.

### ¿Qué hace `AddiuvaRuleta.exe` al abrirlo en Windows?
1. **Pantalla completa bloqueada (Modo Kiosco):** Abre la aplicación en modo Kiosco a pantalla completa sin barras de navegación ni botones de cierre.
2. **Solo se desbloquea o cierra con contraseña:**
   - Contraseña por defecto: **`2026`** *(se puede cambiar desde el panel Admin)*.
   - Si alguien intenta cerrar la ventana con `Alt+F4` sin ingresar la contraseña de administrador en el botón **⏻ Salir**, el proceso guardián (*watchdog*) de `AddiuvaRuleta.exe` la vuelve a abrir instantáneamente en pantalla completa.
   - Si intentás abrir `AddiuvaRuleta.exe` una segunda vez estando ya en ejecución, evita duplicar procesos.
3. **Mantiene la PC y la pantalla siempre encendidas:** Invoca la API nativa de Windows (`kernel32.dll` → `SetThreadExecutionState`) + `Screen Wake Lock` para impedir que Windows apague el monitor o entre en suspensión durante el evento.
4. **Guardado automático en disco:** Cada inscripción y cada ronda ganadora se guardan en tiempo real junto al `.exe` en:
   - `datos-sorteo-addiuva.json` (si la notebook se apaga o reinicia, al abrir `AddiuvaRuleta.exe` recupera todos los inscriptos).
   - `inscriptos-addiuva.csv` (planilla lista para abrir directamente en Excel).
5. **Sin carteles ni traducciones automáticas:** Desactiva el diálogo de traducción de Edge/Chrome (*"Translate page from Spanish?"*), el asistente inicial y las barras emergentes sobre el kiosco.

---

## 🛡️ ¿Cómo evita que una persona se inscriba 2 veces?

En el **Modo 1 · Inscripción (Kiosco)** cada asistente se identifica en la PC con su **DNI** *(entre 2 y 12 números)* y su **Nombre y Apellido Completo** (usando el teclado táctil integrado en pantalla sin disparar el teclado de Windows, o el teclado físico). El sistema aplica **validación anti-duplicado por DNI**:

1. **Bloqueo por DNI único (2 a 12 números):** Limpia puntos, guiones o espacios. Si el mismo DNI intenta anotarse de nuevo, **no le genera otro número**: le muestra en pantalla el número que ya tenía asignado y suma +1 al contador de *Duplicados bloqueados*. Dos personas con el mismo nombre y distinto DNI sí pueden inscribirse.
2. **Paso de confirmación (“¿Están bien escritos tus datos?”):** Antes de asignar el número, muestra el DNI formateado y el nombre en grande para confirmar.
3. **Padrón oficial opcional:** Desde `3. Admin / Datos` se puede pegar la lista de empleados (`DNI, Nombre`) y activar *“Validar contra padrón pre-cargado”* para permitir únicamente DNIs de la empresa.

---

## 🎡 Cómo pasar de la Inscripción a la Ruleta

1. Conectá la notebook a la TV o proyector (por ejemplo con **`Win + P`** → *Duplicar* o *Solo segunda pantalla*).
2. En la esquina superior derecha de la app tocá **`🔒 Admin / Ruleta`** (o presioná **`F9`**) e ingresá la contraseña (**`2026`** por defecto).
3. Se desbloqueará la barra superior con:
   - **`1. Inscripción (Kiosco)`**: pantalla táctil para que la gente se anote.
   - **`2. Generar Ruleta`**: genera automáticamente la ruleta con todos los inscriptos habilitados, rondas ilimitadas en vivo (para que RRHH anuncie cada sorteo por micrófono), sonido y lluvia de confeti.
   - **`3. Admin / Datos`**: buscador de inscriptos, eliminación de participantes/ganadores, configuración (*Mostrar nombres en la ruleta* / Modo Números con nombre oculto hasta descubrirlo, cambio de contraseña, padrón, sonidos) y exportación a CSV/Excel.
   - **`🔒 Bloquear Kiosco`**: vuelve a ocultar los menús para dejar la pantalla bloqueada frente al público (también desde la vista Ruleta).
   - **`⏻ Salir`**: cierra la aplicación de Windows pidiendo la contraseña.

### ⌨️ Control total solo con teclado Bluetooth (sin touch ni mouse)
- **`F9`** o **`Alt+B`** / **`Alt+L`**: Bloquear o desbloquear la pantalla al instante.
- **`Alt+1` / `Alt+2` / `Alt+3`**: Cambiar entre *Inscripción*, *Ruleta* y *Admin / Datos*.
- **`Espacio`** o **`Enter`**: Girar ruleta → Descubrir nombre (Modo Números) → Confirmar ganador.
- **`N`** *(o **`←` / `→`** + **`Enter`**)*: Marcar ganador como ausente y girar de nuevo.
- **`Tab` / `Shift+Tab`**: Moverse entre todos los botones y controles (sin quedar atrapado en teclados táctiles).
- **`F`** / **`Alt+Q`**: Pantalla completa / Salir a Windows.

---

## 🛠️ Compilar desde el código fuente

```bash
# Regenerar el HTML autónomo (dist/ruleta-addiuva.html)
python3 scripts/build_single.py

# Compilar el ejecutable único nativo para Windows 64-bit (dist/AddiuvaRuleta.exe)
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H=windowsgui" -o dist/AddiuvaRuleta.exe .

# Empaquetar el .zip de respaldo con el .exe único
zip -j -9 dist/AddiuvaRuleta-Windows-x64.zip dist/AddiuvaRuleta.exe
```
