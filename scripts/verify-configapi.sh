#!/usr/bin/env bash
# 運営API（/api/params・/api/words）と辞書 seed の受入検証。
#
# **本番には一切触らない。** 使い捨ての Postgres を自前で立て、終了時に必ず捨てる。
#
#   bash scripts/verify-configapi.sh
#
# DB の用意は次の順で自動選択する:
#   1. 環境変数 VERIFY_DATABASE_URL が設定されていればそれを使う（中身は消される。本番を指さないこと）
#   2. docker が使えれば使い捨てコンテナ
#   3. initdb/pg_ctl があればテンポラリディレクトリに使い捨てクラスタ
#
# 検証する内容は docs/plan/plan-20（config-front 残作業）と plan-23（configapi の穴）、
# plan-27 §1（辞書 seed）の受入条件。

set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

SERVER_PORT="${VERIFY_SERVER_PORT:-18099}"
DB_PORT="${VERIFY_DB_PORT:-55432}"
TOKEN="verify-token"
BASE="http://127.0.0.1:${SERVER_PORT}"
WORK="$(mktemp -d)"
SERVER_PID=""
DB_KIND=""
DB_DIR=""

RED=$'\033[31m'; GRN=$'\033[32m'; YEL=$'\033[33m'; DIM=$'\033[2m'; OFF=$'\033[0m'
PASS=0; FAIL=0

ok()   { PASS=$((PASS+1)); printf '  %sPASS%s %s\n' "$GRN" "$OFF" "$1"; }
ng()   { FAIL=$((FAIL+1)); printf '  %sFAIL%s %s\n' "$RED" "$OFF" "$1"; [ $# -gt 1 ] && printf '       %s%s%s\n' "$DIM" "$2" "$OFF"; }
info() { printf '%s\n' "$1"; }
head2(){ printf '\n%s── %s %s\n' "$YEL" "$1" "$OFF"; }

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  case "$DB_KIND" in
    docker) docker rm -f takoda99-verify-db >/dev/null 2>&1 ;;
    local)  pg_ctl -D "$DB_DIR" -m immediate stop >/dev/null 2>&1 ;;
  esac
  rm -rf "$WORK" "$DB_DIR" 2>/dev/null
}
trap cleanup EXIT INT TERM

# ── DB を用意 ───────────────────────────────────────────────
start_db() {
  if [ -n "${VERIFY_DATABASE_URL:-}" ]; then
    DB_KIND="provided"; DSN="$VERIFY_DATABASE_URL"
    info "DB: 指定された VERIFY_DATABASE_URL を使う（中身は消えます）"
    return 0
  fi
  if docker info >/dev/null 2>&1; then
    DB_KIND="docker"
    info "DB: 使い捨て docker コンテナ（:${DB_PORT}）"
    docker rm -f takoda99-verify-db >/dev/null 2>&1
    docker run -d --name takoda99-verify-db -e POSTGRES_PASSWORD=postgres \
      -p "${DB_PORT}:5432" postgres:16-alpine >/dev/null || return 1
    DSN="postgres://postgres:postgres@127.0.0.1:${DB_PORT}/postgres?sslmode=disable"
    return 0
  fi
  if command -v initdb >/dev/null 2>&1 && command -v pg_ctl >/dev/null 2>&1; then
    DB_KIND="local"; DB_DIR="$(mktemp -d)/pgdata"
    info "DB: 使い捨てローカルクラスタ（:${DB_PORT}）"
    initdb -D "$DB_DIR" -U postgres --auth=trust >/dev/null 2>&1 || return 1
    pg_ctl -D "$DB_DIR" -o "-p ${DB_PORT} -k ${DB_DIR}" -l "$WORK/pg.log" start >/dev/null 2>&1 || return 1
    DSN="postgres://postgres@127.0.0.1:${DB_PORT}/postgres?sslmode=disable"
    return 0
  fi
  info "${RED}DB を用意できません${OFF}。docker を起動するか、VERIFY_DATABASE_URL を指定してください。"
  return 1
}

wait_db() {
  for _ in $(seq 1 60); do
    if psql "$DSN" -c 'select 1' >/dev/null 2>&1; then return 0; fi
    if [ "$DB_KIND" = docker ] && docker exec takoda99-verify-db pg_isready -U postgres >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

start_server() {
  DATABASE_URL="$DSN" CONFIG_ADMIN_TOKEN="$TOKEN" PORT="$SERVER_PORT" \
    "$WORK/server" --mode match >"$WORK/server.log" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 40); do
    if curl -fsS -m 2 "$BASE/healthz" >/dev/null 2>&1; then return 0; fi
    kill -0 "$SERVER_PID" 2>/dev/null || { info "${RED}サーバーが起動直後に落ちました${OFF}"; tail -20 "$WORK/server.log"; return 1; }
    sleep 0.5
  done
  return 1
}

stop_server() { kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null; SERVER_PID=""; }

# status とボディを取る。$1=出力先ファイル 以降 curl 引数
req() { local out="$1"; shift; curl -sS -o "$out" -w '%{http_code}' -m 20 "$@"; }

py() { python3 - "$@"; }

# ── 検証 ────────────────────────────────────────────────────

check_seed() {
  head2 "1. 辞書の seed（plan-27 §1 / #86）"
  local body; body="$WORK/words.json"
  local code; code=$(req "$body" "$BASE/api/words")
  [ "$code" = 200 ] || { ng "GET /api/words が $code"; return; }

  py "$body" <<'PY' && ok "全段階 0〜17 が揃い、語数が data.go と一致" || ng "辞書の内容が data.go と一致しない"
import json,sys,subprocess
ws=json.load(open(sys.argv[1]))
ws=ws if isinstance(ws,list) else ws.get("words",[])
lv={}
for w in ws: lv[w["level"]]=lv.get(w["level"],0)+1
missing=[l for l in range(0,18) if lv.get(l,0)==0]
if missing: print("  欠けている level:",missing); sys.exit(1)
if len(ws)!=360: print(f"  語数 {len(ws)}, want 360"); sys.exit(1)
sys.exit(0)
PY

  local v; v=$(psql "$DSN" -tAc 'select coalesce(max(version),0) from word_seed_version' 2>/dev/null)
  [ "$v" = "2" ] && ok "word_seed_version = 2" || ng "word_seed_version = ${v:-取得失敗}, want 2"
}

check_seed_idempotent() {
  head2 "2. 再起動しても増えない・運営が足した語が消えない（plan-27 §1）"
  local code; code=$(req "$WORK/x.json" -X POST "$BASE/api/words" \
    -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' \
    -d '{"mode":"upsert","words":[{"text":"うんめー","reading":"うんめー","level":0,"category":"operator"}]}')
  [ "$code" = 200 ] || [ "$code" = 204 ] || { ng "運営語の追加が $code" "$(cat "$WORK/x.json")"; return; }

  stop_server
  start_server || { ng "再起動に失敗"; return; }

  req "$WORK/words2.json" "$BASE/api/words" >/dev/null
  py "$WORK/words2.json" <<'PY' && ok "361語（360 + 運営追加1）で、運営語が残っている" || ng "再起動で語彙が壊れた"
import json,sys
ws=json.load(open(sys.argv[1]))
ws=ws if isinstance(ws,list) else ws.get("words",[])
if len(ws)!=361: print(f"  語数 {len(ws)}, want 361"); sys.exit(1)
if not any(w["text"]=="うんめー" for w in ws): print("  運営が足した語が消えた"); sys.exit(1)
sys.exit(0)
PY
}

check_params_roundtrip() {
  head2 "3. params の往復（plan-20 Step 1・キャッシュを跨ぐ）"
  local before="$WORK/before.json" mod="$WORK/mod.json" after="$WORK/after.json"
  local code; code=$(req "$before" "$BASE/api/params")
  [ "$code" = 200 ] || { ng "GET /api/params が $code"; return; }

  py "$before" "$mod" <<'PY' || { ng "変更JSONを作れない"; return; }
import json,sys
d=json.load(open(sys.argv[1]))
# 12セクションから1つずつ、Validate を通る範囲で変える
d["session"]["publishIntervalMs"]=300
d["matching"]["minPlayers"]=7
d["credit"]["initialLife"]=11
d["customer"]["total"]=280
d["eval"]["emaAlpha"]=0.35
d["phase"]["midTimeMs"]=31000
d["heat"]["phaseMid"]=4
d["storm"]["warnTicks"]=25
d["distribution"]["weightFloor"]=0.3
d["patience"]["alertMs"]=2500
d["presentation"]["finalStageAliveThreshold"]=18
d["bot"]["accuracyJitter"]=0.15
json.dump(d,open(sys.argv[2],"w"),ensure_ascii=False)
PY

  code=$(req "$WORK/post.json" -X POST "$BASE/api/params" \
    -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' --data-binary "@$mod")
  [ "$code" = 200 ] || { ng "POST /api/params が $code" "$(cat "$WORK/post.json")"; return; }
  ok "configHash 入りのボディをそのまま POST して 200（400 にならない）"

  # ★ ConfigStore は保存時に自分のキャッシュ(2秒)を更新する。すぐ GET すると
  #   DB を読まずに送った値がそのまま返り、DB 往復の欠落を検出できない。
  info "  ${DIM}2秒キャッシュを跨ぐため 3 秒待つ${OFF}"
  sleep 3

  code=$(req "$after" "$BASE/api/params")
  [ "$code" = 200 ] || { ng "再取得が $code"; return; }

  py "$before" "$mod" "$after" <<'PY' && ok "12セクションの変更値が DB 往復後も保持され、リーフキーが1つも欠けない" || ng "往復で値かキーが壊れた"
import json,sys
b,m,a=[json.load(open(p)) for p in sys.argv[1:4]]
def leaves(o,pre=""):
    s=set()
    for k,v in o.items():
        if k=="configHash": continue
        p=f"{pre}{k}"
        s|=leaves(v,p+".") if isinstance(v,dict) else {p}
    return s
lost=sorted(leaves(b)-leaves(a))
if lost: print("  消えたキー:",lost); sys.exit(1)
def get(o,path):
    for k in path.split("."): o=o[k]
    return o
bad=[]
for p in ["session.publishIntervalMs","matching.minPlayers","credit.initialLife",
          "customer.total","eval.emaAlpha","phase.midTimeMs","heat.phaseMid",
          "storm.warnTicks","distribution.weightFloor","patience.alertMs",
          "presentation.finalStageAliveThreshold","bot.accuracyJitter"]:
    if get(a,p)!=get(m,p): bad.append(f"{p}: {get(a,p)} != {get(m,p)}")
if bad: print("  保存されていない:", *bad, sep="\n   "); sys.exit(1)
sys.exit(0)
PY

  py "$after" <<'PY' && ok "customer.*.attribute が文字列のまま保たれている" || ng "attribute が欠落/変質した（全客が Normal 扱いになる）"
import json,sys
d=json.load(open(sys.argv[1]))
want={"normal":"Normal","bonus":"Bonus","claimer":"Claimer","buzz":"Buzz"}
for k,v in want.items():
    got=d["customer"][k].get("attribute")
    if got!=v: print(f"  customer.{k}.attribute = {got!r}, want {v!r}"); sys.exit(1)
sys.exit(0)
PY
}

check_config_hash() {
  head2 "4. configHash（plan-23 §3）"
  local hdr="$WORK/h.txt" body="$WORK/p.json"
  curl -sS -m 20 -D "$hdr" -o "$body" "$BASE/api/params" >/dev/null

  local h; h=$(grep -i '^x-config-hash:' "$hdr" | tr -d '\r' | awk '{print $2}')
  [ -n "$h" ] && ok "ヘッダ X-Config-Hash がある（案C）" || ng "X-Config-Hash が無い"

  local b; b=$(py "$body" <<'PY'
import json,sys; print(json.load(open(sys.argv[1])).get("configHash",""))
PY
)
  [ -n "$b" ] && ok "ボディに configHash がある（案B）" || ng "ボディに configHash が無い"
  [ "$h" = "$b" ] && ok "ヘッダとボディの値が一致" || ng "ヘッダ($h) とボディ($b) が違う"

  grep -qi '^access-control-expose-headers:.*X-Config-Hash' "$hdr" \
    && ok "Expose-Headers に載っている（ブラウザから読める）" \
    || ng "Access-Control-Expose-Headers が無い（config-front から読めない）"
}

check_params_guards() {
  head2 "5. params の入力検証（plan-20 §2）"
  local code
  code=$(req "$WORK/e.json" -X POST "$BASE/api/params" -H 'Content-Type: application/json' \
    --data-binary "@$WORK/before.json")
  [ "$code" = 401 ] && ok "トークン無しの POST は 401" || ng "トークン無しが $code, want 401"

  py "$WORK/before.json" "$WORK/broken.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1])); d["credit"]["initialLife"]=0
json.dump(d,open(sys.argv[2],"w"))
PY
  code=$(req "$WORK/e.json" -X POST "$BASE/api/params" -H "X-Admin-Token: $TOKEN" \
    -H 'Content-Type: application/json' --data-binary "@$WORK/broken.json")
  [ "$code" = 400 ] && ok "Validate に弾かれる値（initialLife=0）は 400" || ng "壊れた値が $code, want 400"
}

check_words_crud() {
  head2 "6. words の CRUD（plan-23 §1・§2）"
  local code id
  code=$(req "$WORK/w.json" -X POST "$BASE/api/words" -H "X-Admin-Token: $TOKEN" \
    -H 'Content-Type: application/json' \
    -d '{"mode":"upsert","words":[{"text":"けんしょうよう","reading":"けんしょうよう","level":3,"category":"verify"}]}')
  [ "$code" = 200 ] || [ "$code" = 204 ] && ok "POST /api/words (upsert) で追加できる" || ng "追加が $code"

  id=$(psql "$DSN" -tAc "select id from words where text='けんしょうよう' limit 1" 2>/dev/null | tr -d ' ')
  [ -n "$id" ] || { ng "追加した語の id が取れない"; return; }

  # reading を変えたら打鍵数が再計算されること
  code=$(req "$WORK/w.json" -X PATCH "$BASE/api/words/$id" -H "X-Admin-Token: $TOKEN" \
    -H 'Content-Type: application/json' -d '{"reading":"たこやき"}')
  [ "$code" = 204 ] || { ng "PATCH が $code" "$(cat "$WORK/w.json")"; return; }
  local ks; ks=$(psql "$DSN" -tAc "select keystroke_count from words where id=$id" | tr -d ' ')
  [ "$ks" = "8" ] && ok "PATCH で reading を変えると打鍵数が再計算される（たこやき=8）" \
                  || ng "keystroke_count = ${ks:-?}, want 8"

  local lv; lv=$(psql "$DSN" -tAc "select level from words where id=$id" | tr -d ' ')
  [ "$lv" = "3" ] && ok "指定していないフィールド（level）は据え置き" || ng "level が $lv に変わった"

  code=$(req "$WORK/w.json" -X PATCH "$BASE/api/words/999999" -H "X-Admin-Token: $TOKEN" \
    -H 'Content-Type: application/json' -d '{"level":1}')
  [ "$code" = 404 ] && ok "存在しない id の PATCH は 404" || ng "$code, want 404"

  # (text, level) 衝突 → 409
  local other; other=$(psql "$DSN" -tAc "select text from words where level=3 and id<>$id limit 1" | sed "s/'/''/g")
  if [ -n "$other" ]; then
    code=$(req "$WORK/w.json" -X PATCH "$BASE/api/words/$id" -H "X-Admin-Token: $TOKEN" \
      -H 'Content-Type: application/json' -d "$(py <<PY
import json;print(json.dumps({"text":"""$other"""}))
PY
)")
    [ "$code" = 409 ] && ok "(text, level) の衝突は 409" || ng "衝突が $code, want 409"
  fi

  code=$(req "$WORK/w.json" -X DELETE "$BASE/api/words/$id" -H "X-Admin-Token: $TOKEN")
  [ "$code" = 204 ] && ok "DELETE は 204" || ng "DELETE が $code"

  code=$(req "$WORK/w.json" -X DELETE "$BASE/api/words/999999" -H "X-Admin-Token: $TOKEN")
  [ "$code" = 404 ] && ok "存在しない id の DELETE は 404（以前は 500）" || ng "$code, want 404"
}

check_matching_reload() {
  head2 "7. matching は再起動なしで反映（docs/deploy.md）"
  py "$WORK/before.json" "$WORK/mp.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1])); d["matching"]["minPlayers"]=3
json.dump(d,open(sys.argv[2],"w"))
PY
  local code; code=$(req "$WORK/e.json" -X POST "$BASE/api/params" -H "X-Admin-Token: $TOKEN" \
    -H 'Content-Type: application/json' --data-binary "@$WORK/mp.json")
  [ "$code" = 200 ] || { ng "POST が $code"; return; }
  sleep 4
  local got; got=$(req "$WORK/e.json" "$BASE/api/params" >/dev/null; py "$WORK/e.json" <<'PY'
import json,sys; print(json.load(open(sys.argv[1]))["matching"]["minPlayers"])
PY
)
  [ "$got" = "3" ] && ok "minPlayers の変更が再起動なしで反映される" || ng "minPlayers = $got, want 3"
}

# ── 実行 ────────────────────────────────────────────────────
info "使い捨て環境で運営API を検証します（本番には接続しません）"
start_db || exit 1
wait_db  || { info "${RED}DB が起動しません${OFF}"; exit 1; }

info "サーバーをビルド中…"
GOWORK=off go build -o "$WORK/server" ./cmd/server || { info "${RED}ビルド失敗${OFF}"; exit 1; }
start_server || { info "${RED}サーバーが起動しません${OFF}"; tail -20 "$WORK/server.log"; exit 1; }
info "サーバー起動: $BASE"

check_seed
check_seed_idempotent
check_params_roundtrip
check_config_hash
check_params_guards
check_words_crud
check_matching_reload

printf '\n%s────────────────────────%s\n' "$YEL" "$OFF"
printf '  %sPASS %d%s / %sFAIL %d%s\n' "$GRN" "$PASS" "$OFF" "$RED" "$FAIL" "$OFF"
[ "$FAIL" -eq 0 ] || { printf '\n%sサーバーログ（末尾）%s\n' "$DIM" "$OFF"; tail -30 "$WORK/server.log"; exit 1; }
exit 0
