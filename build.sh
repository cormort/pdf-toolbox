#!/usr/bin/env bash
# 產出 dist/PdfToolbox/（Windows x64 可攜版）與 dist/PdfToolbox.zip。需要 go、7z、curl（zip 可有可無）。
set -euo pipefail
cd "$(dirname "$0")"
# Windows 裝 7-Zip 預設不會加進 PATH
command -v 7z >/dev/null || [ ! -x "/c/Program Files/7-Zip/7z.exe" ] || PATH="/c/Program Files/7-Zip:$PATH"
GS_TAG=gs10080   # Ghostscript 10.08.0，官方 Artifex 釋出
out=dist/PdfToolbox

# 先刪掉舊的 zip：建置中途失敗時，留著上一次的產物會讓人誤以為是這次建的
rm -f dist/PdfToolbox.zip

# profile/ 是執行時產生的 Edge 設定，不該進打包（體積，而且裡面是使用者的瀏覽資料）。
# 刪除要自成一行並自己檢查：寫成 `rm -rf "$out" && mkdir ...` 的話，rm 的失敗會被 && 吃掉，
# 視窗開著時就會若無其事地把整個 profile 打包進 zip，而且 exit code 還是 1（後面 du 失敗），
# 從產物看不出來。視窗開著就在這裡停下來。
if [ -e "$out/profile" ]; then
  rm -rf "$out/profile" || {
    echo "✗ 刪不掉 $out/profile：PdfToolbox 的視窗還開著？請關掉 PdfToolbox（Edge app 視窗）再重跑。" >&2
    echo "  那是執行時產生的 Edge 設定，不會也不該進打包。" >&2
    exit 1
  }
fi
rm -rf "$out"
mkdir -p "$out" dist/cache

# exe 的圖示是編進 rsrc_windows_amd64.syso 的（Go 會自動連結同目錄的 .syso）。
# 少了它不會有錯誤，只會默默做出一個沒有圖示的 exe，所以在這裡擋下來；換圖示見 README。
[ -f rsrc_windows_amd64.syso ] || {
  echo "✗ 找不到 rsrc_windows_amd64.syso：exe 會沒有圖示（重新產生見 README 的圖示說明）" >&2
  exit 1
}

GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui" -o "$out/PdfToolbox.exe" .

# Windows 版 gs 的初始化檔與字型已編進 dll，只要 bin/ 就能跑
inst=dist/cache/${GS_TAG}w64.exe
[ -s "$inst" ] || curl -fL "https://github.com/ArtifexSoftware/ghostpdl-downloads/releases/download/$GS_TAG/${GS_TAG}w64.exe" -o "$inst"
rm -rf dist/cache/gs
7z x -y -odist/cache/gs "$inst" >/dev/null
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

# Git Bash 沒有 zip，改用 7z；profile/ 再加一道排除，多一層保險
# （7z 的 -x 不吃 `PdfToolbox\profile\*` 這種寫法，要寫成不含結尾 \* 的路徑才會生效）
if command -v zip >/dev/null; then
  (cd dist && rm -f PdfToolbox.zip && zip -qr PdfToolbox.zip PdfToolbox -x 'PdfToolbox/profile/*')
else
  (cd dist && rm -f PdfToolbox.zip && 7z a -tzip -mx=9 PdfToolbox.zip PdfToolbox '-x!PdfToolbox\profile' >/dev/null)
fi
# 最後再確認一次：把使用者的瀏覽資料打包出貨是隱私問題，寧可讓 build 失敗。
# 這裡一定要用 dist/ 開頭的路徑（腳本的工作目錄是 repo 根目錄），而且列出失敗也要停，
# 不然檢查會變成什麼都沒檢查、只印一行 7z 錯誤就過去。
if ! listing=$(7z l dist/PdfToolbox.zip); then
  echo "✗ 讀不出 dist/PdfToolbox.zip，無法確認內容" >&2
  exit 1
fi
if printf '%s\n' "$listing" | grep -qE 'PdfToolbox[\\/]profile'; then
  echo "✗ PdfToolbox.zip 裡有 profile/（執行時產生的 Edge 設定），不該出貨" >&2
  exit 1
fi
du -sh "$out" dist/PdfToolbox.zip
