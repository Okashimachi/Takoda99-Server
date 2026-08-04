# Plan-10: デプロイ戦略・本番オペレーション

> ⚠ **この plan は Render 前提で書かれているが、ホスティングは GCP Compute Engine（e2-micro）に変更済み**。
> 以降の「Render」「render.yaml」「Starter プラン」「ダッシュボードで手動デプロイ」に関する記述は**無効**。
> 実際の構成・手順は **`docs/deploy.md`（GCP + systemd + Caddy）** が正典。
> 本文はモード/設定反映タイミング/当日オペレーションの考え方の部分だけ有効。

> **目的**: ハッカソン本番に向けたデプロイ構成・手順・当日オペレーションを定め、試合運営を円滑に行う。
> **対応issue**: #37, #41 + 新規
> **依存**: Plan-01（基盤移行）, Plan-06（DB/config-front）
> **参照**: render.yaml, マッチング仕様 $4

---

## 1. 前提知識

### 1.1 サーバーの動作モード

`cmd/server/main.go` は `--mode` フラグで2つのモードを持つ:

| モード | 用途 | 動作 |
|---|---|---|
| `--mode match` | **本番** | マッチングプール起動。minPlayers 到達 + カウントダウンで試合開始。Bot で定員補完 |
| `--mode solo` | 開発/デモ | `/ws` 接続ごとに「人間1 + Bot N体」で即試合開始。マッチング待ちなし |

### 1.2 設定の反映タイミング

サーバーは `loadDeps()` をマッチ生成のたびに呼ぶ設計になっている（`cmd/server/main.go` L49-59）:

- **試合系パラメータ**（credit/customer/eval/phase/heat/storm/distribution/patience）: config-front で編集すると**次の試合から**再起動なしで反映
- **マッチングパラメータ**（minPlayers/maxPlayers/startCountdownMs）: 起動時のスナップショットを使用。**変更には再起動が必要**
  - ただし config-front で minPlayers を変更 → Render 手動デプロイで即反映できる
- **環境変数の変更**: Render ダッシュボードで変更後に手動デプロイが必要

### 1.3 状態のライフサイクル

- **試合状態は in-memory**（`game.Session` / `room.Room`）。デプロイ=プロセス再起動で**進行中の試合は消滅する**
- **永続データ**（config/お題/結果）は Postgres。デプロイで失われない
- → 試合中のデプロイは厳禁。**試合の合間（MatchEnd 後、次の MatchStart 前）にデプロイする**

---

## 2. 本番構成図

```
                         ブラウザ / Unity WebGL
                              │ wss://
                              ▼
┌──────────────────────────────────────────────┐
│  Render (Starter+)                           │
│  takoda99-server                             │
│  ┌─────────────┐  ┌─────────────┐            │
│  │ /ws          │  │ /api/params │            │
│  │ WebSocket    │  │ config API  │←──CORS──┐  │
│  │ (match mode) │  │ (GET/POST)  │         │  │
│  └─────────────┘  └─────────────┘         │  │
│        │                    │              │  │
│        │  /healthz (200 OK) │              │  │
│        │  ↑ Render ヘルス   │              │  │
│        │  チェック          │              │  │
│  ┌─────┴────────────────────┴──┐            │  │
│  │ cmd/server --mode match    │            │  │
│  │ (Dockerfile → /server)     │            │  │
│  └────────────────────────────┘            │  │
│        │ DATABASE_URL                      │  │
│        ▼                                   │  │
│  ┌────────────────┐                        │  │
│  │ Render Postgres │                       │  │
│  │ or Supabase     │                       │  │
│  │ config / words  │                       │  │
│  │ match_result    │                       │  │
│  └────────────────┘                        │  │
└──────────────────────────────────────────────┘  │
                                                  │
┌──────────────────────────────────────────────┐  │
│  Vercel                                      │  │
│  takoda99-config (config-front)              │──┘
│  NEXT_PUBLIC_API_URL → Render /api/params    │
└──────────────────────────────────────────────┘
```

---

## 3. render.yaml の更新内容

現在の `render.yaml` はサービス名が `textro99-server`、コマンドが `--mode solo --bots 5` になっている。本番用に更新する。

### 現在（暫定）

```yaml
services:
  - type: web
    name: textro99-server
    runtime: docker
    dockerfilePath: ./Dockerfile
    plan: free
    healthCheckPath: /healthz
    autoDeploy: true
    dockerCommand: /server --mode solo --bots 5
```

### 更新後（本番）

```yaml
# Render Blueprint: ダッシュボードの「New > Blueprint」からこのリポジトリを指定すると
# 下記のサービスが作成される（Dockerfile でビルド）。
services:
  - type: web
    name: takoda99-server
    runtime: docker
    dockerfilePath: ./Dockerfile
    plan: starter          # Starter 以上（Free はスリープあり・Zero Downtime Deploy なし）
    healthCheckPath: /healthz
    autoDeploy: true       # main push で自動デプロイ
    # 本番: マッチングプール起動。99人まで Bot で補完
    dockerCommand: /server --mode match --bots 99
    envVars:
      - key: DATABASE_URL
        sync: false        # ダッシュボードで Postgres 接続文字列を設定
      - key: CONFIG_ADMIN_TOKEN
        sync: false        # config-front の書き込みトークン
      - key: CONFIG_FRONT_ORIGIN
        sync: false        # config-front のオリジン（CORS許可）
      - key: ALLOWED_ORIGINS
        sync: false        # WS接続を許可するオリジン（カンマ区切り）
      # CONFIG_URL は DATABASE_URL 使用時は不要（DB 優先）
      # GOGC=200 を推奨（GC頻度低減、Plan-09 参照）
      - key: GOGC
        value: "200"
```

### 変更点まとめ

| 項目 | 旧 | 新 |
|---|---|---|
| サービス名 | `textro99-server` | `takoda99-server` |
| プラン | `free` | `starter` |
| コマンド | `--mode solo --bots 5` | `--mode match --bots 99` |
| 環境変数 | コメントアウト | 全て宣言（値はダッシュボードで設定） |
| GOGC | なし | `200`（GC頻度低減） |

> **注意**: Render のサービス名を変更すると URL が変わる。旧URLのクライアントは接続できなくなるので、フロント/Unity 側の接続先も同時に更新すること。

---

## 4. 環境変数一覧

| 変数名 | 必須 | 値の例 | 説明 |
|---|---|---|---|
| `DATABASE_URL` | 推奨 | `postgres://user:pass@host:5432/db` | Postgres 接続文字列。未設定でも内蔵デフォルトで起動する |
| `CONFIG_ADMIN_TOKEN` | 推奨 | `my-secret-token-abc123` | config-front の POST 認証トークン。未設定なら POST は 503 |
| `CONFIG_FRONT_ORIGIN` | 推奨 | `https://takoda99-config.vercel.app` | config-front からの CORS 許可オリジン（カンマ区切りで複数可）。未設定なら `*` |
| `ALLOWED_ORIGINS` | 推奨 | `https://takoda99.vercel.app,https://localhost:5173` | WS 接続を許可するフロント/Unity WebGL のオリジン。未設定なら全許可 |
| `CONFIG_URL` | 任意 | `https://takoda99-config.vercel.app/api/params` | config を HTTP 取得するURL。`DATABASE_URL` がある場合は DB が優先されるため不要 |
| `PORT` | 自動 | `10000` | Render が自動注入。サーバーは `$PORT` があればそれを listen する |
| `GOGC` | 推奨 | `200` | Go GC 頻度の調整（デフォルト100。200で GC 半減） |

### 設定の優先順位（`cmd/server/main.go` の `chooseProvider`）

```
DATABASE_URL (Postgres) > CONFIG_URL / --config-url (HTTP) > 内蔵デフォルト
```

DB 接続やマイグレーションに失敗しても、起動を止めず内蔵デフォルトで継続する（安全寄り設計）。

---

## 5. 当日オペレーション手順

### 5.1 事前準備チェックリスト（前日〜当日朝）

```
[ ] 1. render.yaml を更新済み
       - name: takoda99-server
       - plan: starter (以上)
       - dockerCommand: /server --mode match --bots 99

[ ] 2. Render ダッシュボードで環境変数を全て設定済み
       - DATABASE_URL
       - CONFIG_ADMIN_TOKEN
       - CONFIG_FRONT_ORIGIN
       - ALLOWED_ORIGINS
       - GOGC=200

[ ] 3. main ブランチにマージ → Render の自動デプロイ完了を確認

[ ] 4. ヘルスチェック確認
       curl https://takoda99-server.onrender.com/healthz
       → 200 OK "ok"

[ ] 5. WebSocket 接続テスト（wscat 等で疎通確認）
       wscat -c wss://takoda99-server.onrender.com/ws
       → 接続成功、MatchmakingStatus 受信

[ ] 6. config-front 疎通確認
       - config-front を開き、パラメータ一覧が表示される
       - minPlayers を変更して保存 → GET /api/params で反映を確認

[ ] 7. DB テーブル確認
       - config テーブルにデフォルト値が入っている
       - お題単語データが登録済み

[ ] 8. minPlayers を当日の想定人数に合わせて設定
       - 参加者30人なら minPlayers=20 程度（全員来なくても始まるように）
       - 少人数テストなら minPlayers=2

[ ] 9. フロント/Unity の接続先URLを確認
       - wss://takoda99-server.onrender.com/ws が正しいこと

[ ] 10. Render のプラン確認
        - Free ではないこと（スリープ/Zero Downtime Deploy なし）
        - Starter ($7/月) 以上
```

### 5.2 試合開始手順

```
1. 参加者に接続URLまたはQRコードを共有する

2. 参加者が接続を開始
   - 各クライアントは /ws に WebSocket 接続
   - サーバーが MatchmakingStatus を配信（waitingCount が増えていく）

3. 人数確認
   - config-front のリアルタイム状態 or ログで waitingCount を確認
   - Render ログで "match: 参加 s-N" を確認

4. minPlayers 到達 → 自動的にカウントダウン開始
   - startCountdownMs（デフォルト15秒）後に試合開始
   - maxPlayers（デフォルト99）到達で即開始
   - 不足分は Bot で自動補完（--bots 99 で99人まで埋める）

5. 試合開始
   - MatchStart が全員に配信される
   - Render ログで "match: 試合開始 players=N" を確認

6. 試合終了
   - MatchEnd が全員に配信される
   - 再マッチングに入りたい場合は参加者に再接続を案内
```

### 5.3 トラブル時対応

#### サーバーが落ちた / 応答しない

```
1. Render ダッシュボードでサービスの状態を確認
2. ログを確認（"listen:" エラー、OOM 等）
3. Render は自動再起動する。再起動を待つ（数十秒）
4. 再起動後に /healthz を確認
5. 参加者にページリロード（再接続）を案内
   → 進行中だった試合は消えるが、次の試合は問題なく始まる
```

#### 試合が始まらない（マッチングで止まる）

```
1. waitingCount を確認（ログ or config-front）
2. minPlayers を確認。参加者数より多い場合:
   - config-front で minPlayers を下げる
   - ※ matchingParams は起動時スナップショットなので、反映には再デプロイが必要
   - Render ダッシュボード → Manual Deploy → "Deploy latest commit"
3. 再デプロイ後、参加者に再接続を案内
```

#### クライアントが接続できない

```
1. CORS / Origin 設定を確認
   - ALLOWED_ORIGINS にフロントのオリジンが含まれているか
   - ブラウザの開発者ツールで WebSocket エラーを確認
2. wss:// でアクセスしているか（Render は HTTPS 強制）
3. Render のサービスがスリープしていないか（Free プランの場合）
4. Render ログで WS upgrade のエラーを確認
```

#### パラメータがおかしい（バランス崩壊等）

```
1. config-front でパラメータを修正
2. 修正は「次の試合から」反映される（進行中の試合には影響しない）
3. 進行中の試合を中断したい場合は、Render で手動デプロイ（プロセス再起動）
```

### 5.4 デモ用構成（審査員/少人数向け）

#### 方法A: 本番サーバーの minPlayers を下げる

```
1. config-front で minPlayers=2 に変更
2. Render で手動デプロイ（matchingParams 反映のため）
3. 審査員2人が接続すればカウントダウン → Bot 補完で試合開始
```

#### 方法B: solo モードで別インスタンスを用意

```
1. Render ダッシュボードで別のサービスを作成（or dockerCommand を一時変更）
   dockerCommand: /server --mode solo --bots 5
2. 接続した瞬間に Bot 5体と即試合開始（マッチング待ちなし）
3. デモ後に本番コマンドに戻す
```

#### 推奨

- 審査員がプレイする場合: **方法A**（minPlayers=2、残りは Bot 補完）
- 動作デモだけの場合: **方法B**（solo モードで即開始）

---

## 6. ロールバック手順

### 通常のロールバック（コードの問題）

```
1. Render ダッシュボード → Events タブ
2. 前の正常なデプロイの「Rollback」ボタンを押す
3. 旧バージョンが再デプロイされる
4. /healthz で正常起動を確認
```

### DB マイグレーションがある場合

- **カラム追加のみ（前方互換）**: ロールバック可能。旧コードは新カラムを無視する
- **カラム削除/名前変更**: ロールバック不可。**本番では破壊的マイグレーションは行わない**
- マイグレーション（`cs.Migrate(ctx)`）が失敗してもサーバーは内蔵デフォルトで起動継続する設計

### config-front のロールバック

- Vercel ダッシュボード → Deployments → 前のデプロイの「...」→「Promote to Production」

### 緊急時: 全てリセット

```
1. Render で手動デプロイ（最新の main ブランチ）
2. DB の config テーブルをリセット（デフォルト値に戻す）
   → サーバーは config 取得失敗時に内蔵デフォルトで起動するので、
     最悪 DATABASE_URL を一時的に外しても動く
3. 参加者に再接続を案内
```

---

## 7. 完了条件

- [ ] render.yaml が `takoda99-server` / `--mode match` / `starter` に更新されている
- [ ] Render ダッシュボードで環境変数が全て設定されている
- [ ] デプロイ → /healthz OK → WebSocket 接続可能
- [ ] config-front からパラメータ変更 → 次試合で反映される
- [ ] minPlayers 変更 → 再デプロイ → 反映される
- [ ] 当日オペレーション手順がチームに共有されている
- [ ] ロールバック手順を1回以上テスト済み
