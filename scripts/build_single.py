#!/usr/bin/env python3
"""Genera una versión de un solo archivo del prototipo (dist/ruleta-addiuva.html).

Incrusta CSS, JS, la librería de QR y el logo (base64) para poder
abrirlo con doble clic, mandarlo por mail o usarlo sin internet.
"""
import base64
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
SRC = ROOT / "prototipo"
OUT = ROOT / "dist" / "ruleta-addiuva.html"

html = (SRC / "index.html").read_text(encoding="utf-8")
css = (SRC / "styles.css").read_text(encoding="utf-8")
app = (SRC / "app.js").read_text(encoding="utf-8")
qr = (SRC / "vendor" / "qrcode.js").read_text(encoding="utf-8")
logo = base64.b64encode((SRC / "assets" / "logo-addiuva-blanco.png").read_bytes()).decode()

html = html.replace('<link rel="stylesheet" href="styles.css" />', f"<style>\n{css}\n</style>")
html = html.replace('<script src="vendor/qrcode.js"></script>', f"<script>\n{qr}\n</script>")
html = html.replace('<script src="app.js"></script>', f"<script>\n{app}\n</script>")
html = html.replace('src="assets/logo-addiuva-blanco.png"', f'src="data:image/png;base64,{logo}"')

OUT.parent.mkdir(parents=True, exist_ok=True)
OUT.write_text(html, encoding="utf-8")
print(f"OK -> {OUT} ({OUT.stat().st_size // 1024} KB)")
