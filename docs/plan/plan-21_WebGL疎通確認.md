# Plan-21: WebGL ⇄ サーバー 疎通確認

> **目的**: `Takoda99-Unity`（WebGLビルド）からサーバーに繋がるかを早期に潰す。クライアント側の最重要技術リスク。
> **対応issue**: #38
> **優先度**: 中。ただし**問題が出た時の手戻りが大きい**ので早めに。
> **依存**: Plan-15（solo エンドポイント）
> **担当**: りーせ ＋ Unity 担当（合同）

---

## 0. なぜリスクなのか

Unity WebGL の WebSocket は**ブラウザの制約をそのまま受ける**が、Unity 側の実装が特殊。

| 制約 | 内容 |
|---|---|
| `System.Net.WebSockets` が使えない | WebGL は .NET のソケットを持たない。**JavaScript ブリッジ（.jslib）が必須** |
| `wss://` 必須 | https のページから `ws://` には繋げない。→ サーバーは Caddy で TLS 済みなのでOK |
| Origin が送られる | ブラウザが自動で付ける。サーバーの `ALLOWED_ORIGINS` に載っている必要がある |
| CORS はWSには効かない | WebSocket は CORS ではなく Origin チェック。サーバー側の実装で見ている |

ここで詰まると Unity クライアント自体が成立しないので、**接続だけ**を最短で確認する。

---

## 1. サーバー側の準備

### Step 1: Origin を許可する

Unity WebGL ビルドの**配信元オリジン**を `ALLOWED_ORIGINS` に足す。

配信元の候補:

| 配信方法 | Origin |
|---|---|
| Unity Editor の Play（WebGLでない） | Origin なし → 素通し |
| ローカルで `python -m http.server` | `http://localhost:8000` |
| Unity の Build & Run | `http://localhost:<ランダム>` ← **ポートが毎回変わる** |
| Vercel / Netlify / itch.io に配置 | `https://<host>` |
| GitHub Pages | `https://<user>.github.io` |

```bash
sudo nano /etc/takoda99.env
# ALLOWED_ORIGINS=http://localhost:5173,https://<webfront>.vercel.app,https://<unity-host>
```

```bash
sudo systemctl restart takoda99 takoda99-solo
```

> **開発中の逃げ道**: `ALLOWED_ORIGINS` を**未設定にすると全許可**になる（既定）。
> Unity の Build & Run はポートが毎回変わるので、疎通確認の段階は全許可のままでよい。
> `/ws` は Cookie 認証を持たないので実害は小さい。**本番前に絞る**。

現在の設定を確認:

```bash
sudo grep ALLOWED_ORIGINS /etc/takoda99.env || echo "未設定（＝全許可）"
```

### Step 2: 接続先を伝える

| 用途 | URL |
|---|---|
| 疎通確認・単独検証 | `wss://takoda99-solo.mooo.com/ws`（1接続で即 `MatchStart`） |
| 本番 | `wss://takoda99.mooo.com/ws`（`MatchmakingStatus` 待機） |

**疎通確認は solo を使う**。match だと `MatchmakingStatus` しか来ないので
「繋がったが何も起きない」のか「繋がっていない」のか切り分けづらい。

---

## 2. Unity 側の最小実装

### 選択肢

| 方法 | 評価 |
|---|---|
| **NativeWebSocket**（endel/NativeWebSocket） | **推奨**。WebGL/エディタ両対応。導入が最も速い |
| 自前 .jslib ブリッジ | 学習コストが高い。今回は不要 |
| `System.Net.WebSockets` | **WebGLで動かない**。エディタで動いてビルドで落ちる罠 |

> ⚠ **エディタで動いても WebGL ビルドで動くとは限らない**。必ず**ビルドして**確認すること。
> `System.Net.WebSockets` はエディタでは動くので、これで「疎通OK」と誤判定しやすい。

### 最小コード（NativeWebSocket）

```csharp
using NativeWebSocket;

public class ConnTest : MonoBehaviour {
    WebSocket ws;

    async void Start() {
        ws = new WebSocket("wss://takoda99-solo.mooo.com/ws");

        ws.OnOpen    += ()      => Debug.Log("[WS] open");
        ws.OnError   += (e)     => Debug.LogError("[WS] error: " + e);
        ws.OnClose   += (c)     => Debug.Log("[WS] close: " + c);
        ws.OnMessage += (bytes) => {
            var json = System.Text.Encoding.UTF8.GetString(bytes);
            Debug.Log("[WS] recv: " + json.Substring(0, Mathf.Min(200, json.Length)));
        };

        await ws.Connect();
    }

    void Update() {
        #if !UNITY_WEBGL || UNITY_EDITOR
        ws?.DispatchMessageQueue();   // WebGL 以外は手動ポンプが要る
        #endif
    }

    async void OnApplicationQuit() {
        if (ws != null) await ws.Close();
    }
}
```

---

## 3. 受入確認

WebGL ビルドをブラウザで開き、コンソール（F12）で:

- [ ] `[WS] open` が出る
- [ ] `[WS] recv: {"type":"MatchStart",...}` が出る（solo の場合）
- [ ] `[WS] error` が出ていない

`MatchStart` の JSON が読めれば**疎通は完全に成立**。ここから先は Plan-22（本格結合）。

### 本番（match）でも確認

```
wss://takoda99.mooo.com/ws
```

- [ ] `[WS] recv: {"type":"MatchmakingStatus",...}` が出る

---

## 4. トラブルシュート

| 症状 | 原因 | 対処 |
|---|---|---|
| ブラウザコンソールに Mixed Content エラー | `ws://` に繋いでいる | `wss://` にする |
| `403` / 即 close | Origin が拒否された | `ALLOWED_ORIGINS` に追加、または未設定にする |
| エディタでは動くがビルドで動かない | `System.Net.WebSockets` を使っている | NativeWebSocket 等の WebGL 対応ライブラリに変える |
| `OnMessage` が呼ばれない（エディタ） | `DispatchMessageQueue()` を呼んでいない | `Update()` で呼ぶ |
| 繋がるが何も来ない | match モードに繋いでいる | solo エンドポイントを使う |
| TLS 証明書エラー | Caddy の証明書取得に失敗 | `sudo journalctl -u caddy -n 50` を確認 |

サーバー側でも接続を確認できる（Plan-17 のログ）:

```bash
sudo journalctl -u takoda99-solo -f -o cat | jq -c 'select(.msg=="ws_connect")'
```

---

## 5. 完了条件

- [ ] WebGL ビルド（**エディタではなく実ビルド**）から `wss://takoda99-solo.mooo.com/ws` へ接続できる
- [ ] `MatchStart` の JSON をブラウザコンソールで受信できている
- [ ] 本番エンドポイントでも `MatchmakingStatus` を受信できる
- [ ] サーバー側のログに `ws_connect` が出ることを確認
- [ ] Unity で使う WebSocket ライブラリが決まっている（WebGL対応が確認済み）
- [ ] Unity WebGL の配信元オリジンが決まり、`ALLOWED_ORIGINS` の方針（全許可 or 列挙）が決まっている
- [ ] 詰まりどころがあれば本ドキュメントのトラブルシュートに追記されている
