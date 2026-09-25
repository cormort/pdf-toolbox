#!/usr/bin/env bash
# 產出 dist/PdfToolbox/（Windows x64 可攜版）與 dist/PdfToolbox.zip。需要 go、7z、curl（zip 可有可無）。
set -euo pipefail
cd "$(dirname "$0")"
# Windows 裝 7-Zip 預設不會加進 PATH
command -v 7z >/dev/null || [ ! -x "/c/Program Files/7-Zip/7z.exe" ] || PATH="/c/Program Files/7-Zip:$PATH"
GS_TAG=gs10080   # Ghostscript 10.08.0，官方 Artifex 釋出
out=dist/PdfToolbox
rm -rf "$out" && mkdir -p "$out" dist/cache

GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui" -o "$out/PdfToolbox.exe" .

# Windows 版 gs 的初始化檔與字型已編進 dll，只要 bin/ 就能跑
inst=dist/cache/${GS_TAG}w64.exe
[ -s "$inst" ] || curl -fL "https://github.com/ArtifexSoftware/ghostpdl-downloads/releases/download/$GS_TAG/${GS_TAG}w64.exe" -o "$inst"
rm -rf dist/cache/gs && 7z x -y -odist/cache/gs "$inst" >/dev/null
mkdir -p "$out/gs/bin"
cp dist/cache/gs/bin/gswin64c.exe dist/cache/gs/bin/gsdll64.dll "$out/gs/bin/"

# gsdll64 需要 VC++ 執行階段，乾淨的 Windows 不一定有：從 gs 附的 vcredist 取出 x64 版放在旁邊（微軟允許 app-local 隨附）
vc=dist/cache/vc && rm -rf $vc
7z x -y -t# -o$vc dist/cache/gs/vcredist_x64.exe >/dev/null
7z x -y -o$vc/cab $vc/4.cab >/dev/null
for c in $vc/cab/*; do
  7z e -y -o$vc/x "$c" msvcp140.dll vcruntime140.dll vcruntime140_1.dll >/dev/null 2>&1 || true
  file $vc/x/msvcp140.dll 2>/dev/null | grep -q x86-64 && break
  rm -rf $vc/x
done
cp $vc/x/msvcp140.dll $vc/x/vcruntime140.dll $vc/x/vcruntime140_1.dll "$out/gs/bin/"
# AGPL：隨附授權；原始碼位置寫在 README
cp dist/cache/gs/doc/COPYING "$out/gs/" 2>/dev/null || find dist/cache/gs -maxdepth 2 -iname 'COPYING*' -exec cp {} "$out/gs/" \;

# Git Bash 沒有 zip，改用 7z
if command -v zip >/dev/null; then
  (cd dist && rm -f PdfToolbox.zip && zip -qr PdfToolbox.zip PdfToolbox)
else
  (cd dist && rm -f PdfToolbox.zip && 7z a -tzip -mx=9 PdfToolbox.zip PdfToolbox >/dev/null)
fi
du -sh "$out" dist/PdfToolbox.zip
