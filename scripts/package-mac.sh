#!/usr/bin/env bash
# Gói binary đã build thành PortManager.app (bundle tối giản, ad-hoc signed).
set -euo pipefail
cd "$(dirname "$0")/.."

VER=$(sed -n '1p' VERSION | tr -d '[:space:]')
APP=build/PortManager.app

[ -f build/port-manager ] || { echo "Thiếu build/port-manager — chạy make build"; exit 1; }

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS"
cp build/port-manager "$APP/Contents/MacOS/port-manager"

# Icon app: assets/appicon.png (vẽ bằng `make icons`) → AppIcon.icns qua
# iconset chuẩn; sips + iconutil có sẵn trên macOS nên không cần tool ngoài.
if [ -f assets/appicon.png ]; then
    ISET=build/AppIcon.iconset
    rm -rf "$ISET"
    mkdir -p "$ISET"
    for s in 16 32 128 256 512; do
        sips -z "$s" "$s" assets/appicon.png --out "$ISET/icon_${s}x${s}.png" >/dev/null
        d=$((s * 2))
        sips -z "$d" "$d" assets/appicon.png --out "$ISET/icon_${s}x${s}@2x.png" >/dev/null
    done
    mkdir -p "$APP/Contents/Resources"
    iconutil -c icns "$ISET" -o "$APP/Contents/Resources/AppIcon.icns"
    rm -rf "$ISET"
fi

cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key><string>PortManager</string>
    <key>CFBundleDisplayName</key><string>SMATEC Port Manager</string>
    <key>CFBundleIdentifier</key><string>vn.smatec.port-manager</string>
    <key>CFBundleExecutable</key><string>port-manager</string>
    <key>CFBundleIconFile</key><string>AppIcon</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>${VER}</string>
    <key>CFBundleVersion</key><string>${VER}</string>
    <key>LSMinimumSystemVersion</key><string>12.0</string>
    <key>NSHighResolutionCapable</key><true/>
    <key>NSHumanReadableCopyright</key><string>Công cụ nội bộ — workspace smatec</string>
</dict>
</plist>
EOF

echo "APPL????" > "$APP/Contents/PkgInfo"
codesign --force --deep --sign - "$APP" >/dev/null 2>&1 || true
echo "Đã gói $APP (v${VER})"
