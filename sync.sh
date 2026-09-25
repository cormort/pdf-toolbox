#!/usr/bin/env bash
# 從 GitHub 拉四個前端工具到 web/，拿掉 gtag、把 CDN 換成 web/vendor/ 的本機檔。
# 工具更新後跑一次再 commit；build 本身不需要網路。（Windows 用 Git Bash 跑）
set -euo pipefail
cd "$(dirname "$0")"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

# repo  目的地  要帶的檔案（其餘 README、測試、開發工具不帶）
fetch() {
  local repo=$1 dest=$2; shift 2
  git clone -q --depth 1 "https://github.com/cormort/$repo.git" "$tmp/$repo"
  rm -rf "web/$dest" && mkdir -p "web/$dest"
  (cd "$tmp/$repo" && cp -R "$@" "$OLDPWD/web/$dest/")
  echo "$repo@$(git -C "$tmp/$repo" rev-parse --short HEAD) -> web/$dest"
}
fetch pdf-row-shifter row-shifter index.html
fetch xls2spread      xls2spread  index.html
fetch diffpdf-web     diffpdf     index.html
# 不帶 service-worker.js：本機服務用不到離線快取，留著反而會讓改版後跑舊檔
fetch pdfviewer_v2    viewer      index.html instructions.html style.css script.js db.js \
                                  manifest.json lib icons LICENSE THIRD-PARTY-NOTICES.md
rm -f web/viewer/icons/compose-icons.py

vendor() { # URL 本機路徑
  mkdir -p "web/vendor/$(dirname "$2")"
  [ -s "web/vendor/$2" ] || curl -fsSL "$1" -o "web/vendor/$2"
}
vendor https://cdnjs.cloudflare.com/ajax/libs/pdf.js/3.11.174/pdf.min.js        pdfjs-3.11.174/pdf.min.js
vendor https://cdnjs.cloudflare.com/ajax/libs/pdf.js/3.11.174/pdf.worker.min.js pdfjs-3.11.174/pdf.worker.min.js
vendor https://cdn.jsdelivr.net/npm/pdfjs-dist@4.10.38/build/pdf.min.mjs         pdfjs-4.10.38/pdf.min.mjs
vendor https://cdn.jsdelivr.net/npm/pdfjs-dist@4.10.38/build/pdf.worker.min.mjs  pdfjs-4.10.38/pdf.worker.min.mjs
vendor https://cdnjs.cloudflare.com/ajax/libs/jspdf/3.0.3/jspdf.umd.min.js       jspdf-3.0.3/jspdf.umd.min.js

for f in web/row-shifter/index.html web/xls2spread/index.html web/diffpdf/index.html web/viewer/index.html; do
  perl -0pi -e '
    s{<!-- Google tag \(gtag\.js\) -->.*?</script>\s*<script>.*?</script>\n?}{}s;
    s{<link[^>]*fonts\.(googleapis|gstatic)\.com[^>]*>\n?}{}g;
    s{https://cdnjs\.cloudflare\.com/ajax/libs/pdf\.js/3\.11\.174/}{/vendor/pdfjs-3.11.174/}g;
    s{https://cdn\.jsdelivr\.net/npm/pdfjs-dist\@4\.10\.38/build/}{/vendor/pdfjs-4.10.38/}g;
    s{https://cdnjs\.cloudflare\.com/ajax/libs/jspdf/3\.0\.3/}{/vendor/jspdf-3.0.3/}g;
  ' "$f"
done

# 還有外連就停下來，免得打包後離線才發現缺檔
if grep -nE "https://(cdn\.|cdnjs\.|unpkg\.|esm\.sh|fonts\.g|www\.googletagmanager)" web/*/index.html web/viewer/*.js; then
  echo "↑ 還有外部資源沒處理" >&2; exit 1
fi
echo "OK"
