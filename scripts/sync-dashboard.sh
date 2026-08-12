#!/usr/bin/env bash
# sync-dashboard.sh — 別リポ Takoda99-DashBoard のビルド成果物（素の静的サイト）を
# このサーバーの embed ディレクトリへ取り込む。Go が //go:embed で単一バイナリに同梱し、
# /admin で配信する（plan-h01 / plan-h00 §5）。単一デプロイを保つための橋渡し。
#
# 使い方:
#   scripts/sync-dashboard.sh [DASHBOARD_REPO_PATH]
# 既定の DASHBOARD_REPO_PATH は ../Takoda99-DashBoard。
#
# ダッシュボードは現状フレームワーク無しの素の HTML/JS/CSS なので「ビルド成果物 == ソース」。
# 将来バンドラを入れたら、コピー元を dist/ に変えるだけでよい。
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
src="${1:-$here/../Takoda99-DashBoard}"
# dist/ があればそれを、無ければリポジトリ直下を配信物とみなす。
if [ -d "$src/dist" ]; then
  src="$src/dist"
fi
dest="$here/internal/admin/webdist"

if [ ! -f "$src/index.html" ]; then
  echo "error: $src に index.html が見つからない（DashBoard リポのパスを引数で指定）" >&2
  exit 1
fi

echo "sync: $src -> $dest"
rm -rf "$dest"
mkdir -p "$dest"
# 静的配信物のみコピー（.git やドキュメントは含めない）。
( cd "$src" && \
  find . \( -name '*.html' -o -name '*.css' -o -name '*.js' -o -name '*.svg' \
           -o -name '*.png' -o -name '*.ico' -o -name '*.webp' -o -name '*.woff2' \) \
    -not -path './.git/*' -print0 \
  | while IFS= read -r -d '' f; do
      mkdir -p "$dest/$(dirname "$f")"
      cp "$f" "$dest/$f"
    done )

count=$(find "$dest" -type f | wc -l | tr -d ' ')
echo "sync: $count ファイルを同梱しました"
