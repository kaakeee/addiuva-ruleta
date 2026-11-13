# Addiuva · Kiosco de Inscripción (DNI + Nombre) y Ruleta Digital 🎡

Aplicación todo-en-uno para el sorteo de fin de año de **Addiuva** (~100 personas), diseñada especialmente para una **notebook táctil (Lenovo Yoga) + salida HDMI**.

---

## 📦 Descarga directa para Windows (`.zip` con `.exe`)

El paquete listo para usar en Windows 10 / Windows 11 (64 bits) se encuentra en:

- **[`dist/AddiuvaRuleta-Windows-x64.zip`](dist/AddiuvaRuleta-Windows-x64.zip)** *(contiene `AddiuvaRuleta.exe`, `LEEME-WINDOWS.txt` y `ruleta-addiuva.html`)*
- También disponible en la sección **Releases** de este repositorio en GitHub.

### ¿Qué hace `AddiuvaRuleta.exe` al abrirlo en Windows?
1. **Pantalla completa bloqueada (Modo Kiosco):** Abre la aplicación en modo Kiosco a pantalla completa sin barras de navegación ni botones de cierre.
2. **Solo se desbloquea o cierra con contraseña:**
   - Contraseña por defecto: **`2026`** *(se puede cambiar desde el panel Admin)*.
   - Si alguien intenta cerrar la ventana con `Alt+F4` sin ingresar la contraseña de administrador en el botón **⏻ Salir**, el proceso guardián (*watchdog*) de `AddiuvaRuleta.exe` la vuelve a abrir instantáneamente en pantalla completa.
3. **Mantiene la PC y la pantalla siempre encendidas:** Invoca la API nativa de Windows (`kernel32.dll` → `SetThreadExecutionState`) + `Screen Wake Lock` para impedir que Windows apague el monitor o entre en suspensión durante el evento.
4. **Guardado automático en disco:** Cada inscripción y cada premio sorteado se guardan en tiempo real junto al `.exe` en:
   - `datos-sorteo-addiuva.json` (si la notebook se apaga o reinicia, al abrir `AddiuvaRuleta.exe` recupera todos los inscriptos).
   - `inscriptos-addiuva.csv` (planilla lista para abrir directamente en Excel).

---

## 🛡️ ¿Cómo evita que una persona se inscriba 2 veces?

En el **Modo 1 · Inscripción (Kiosco)** cada asistente se identifica en la PC con su **DNI** y su **Nombre y Apellido Completo** (usando el teclado táctil integrado en pantalla o el teclado físico). El sistema aplica **triple validación anti-duplicado**:

1. **Bloqueo por DNI único:** Limpia puntos, guiones, espacios y ceros a la izquierda. Si el mismo DNI intenta anotarse de nuevo, **no le genera otro número**: le muestra en pantalla el número que ya tenía asignado y suma +1 al contador de *Duplicados bloqueados*.
2. **Bloqueo por Nombre y Apellido Completo (incluso invertido):** Normaliza mayúsculas y tildes, y compara tanto el orden normal (`Sofía González`) como el orden invertido (`González Sofía`). Si alguien intenta anotarse por segunda vez cambiando el DNI pero con el mismo nombre y apellido, el sistema bloquea el registro.
3. **Paso de confirmación (“¿Están bien escritos tus datos?”):** Antes de asignar el número, muestra el DNI formateado y el nombre en grande para confirmar.
4. **Padrón oficial opcional:** Desde `3. Admin / Datos` se puede pegar la lista de empleados (`DNI, Nombre`) y activar *“Validar contra padrón pre-cargado”* para permitir únicamente DNIs de la empresa.

---

## 🎡 Cómo pasar de la Inscripción a la Ruleta

1. En la esquina superior derecha tocá **`🔒 Admin / Ruleta`**.
2. Ingresá la contraseña de administrador (**`2026`** por defecto).
3. Se desbloqueará la barra superior con:
   - **`1. Inscripción (Kiosco)`**: pantalla táctil para que la gente se anote.
   - **`2. Generar Ruleta`**: genera automáticamente la ruleta con todos los inscriptos habilitados, premios, sonido y lluvia de confeti.
   - **`3. Admin / Datos`**: buscador de inscriptos, carga de premios, cambio de contraseña y exportación a CSV/Excel.
   - **`🖥️ Ventana HDMI`**: abre una segunda ventana limpia solo con la ruleta para arrastrar al proyector HDMI (en modo *Extender pantalla* de Windows).
   - **`🔒 Bloquear Kiosco`**: vuelve a ocultar los menús para dejar la PC bloqueada frente al público.
   - **`⏻ Salir`**: cierra la aplicación de Windows pidiendo la contraseña.

---

## 🛠️ Compilar desde el código fuente

```bash
# Regenerar el HTML autónomo (dist/ruleta-addiuva.html)
python3 scripts/build_single.py

# Compilar el ejecutable nativo para Windows 64-bit (dist/AddiuvaRuleta.exe)
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H=windowsgui" -o dist/AddiuvaRuleta.exe .

# Empaquetar el .zip de distribución
zip -j -9 dist/AddiuvaRuleta-Windows-x64.zip dist/AddiuvaRuleta.exe dist/LEEME-WINDOWS.txt dist/ruleta-addiuva.html
```
