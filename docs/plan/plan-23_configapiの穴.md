# Plan-23: configapi の穴 3件（運営UIからの要求）

> **目的**: 管理UI `takoda99-config` の仕様書が「フロントでは埋められない」として挙げたサーバー側の課題を潰す。
> **対応issue**: #68
> **優先度**: 低〜中。**ゲーム進行には影響しない**（運営UIの使い勝手）。ただし §2 は小さいので先に入れてよい。
> **依存**: なし
> **出典**: `takoda99-config/docs/issues.md`, 同仕様書 §6.4・§12

---

## 0. 3件の概要

| # | 内容 | 規模 | 判断 |
|---|---|---|---|
| 1 | `PATCH /api/words/{id}`（1語の部分更新） | 中 | 運営が words を実際に編集し始めてからでよい |
| 2 | `DELETE /api/words/{id}` の未存在時を 500 → 404 | **小** | **小さな正しさの修正。先に入れる** |
| 3 | `GET /api/params` に `configHash` を含める | **小** | 運営が反映を照合できる。安い割に効く |

**推奨順: 2 → 3 → 1**

---

## 1. 現状のコード

### `internal/configapi/words.go`

```go
func (h *wordsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodOptions: ...
	case http.MethodGet:     ...
	case http.MethodPost:    ...   // upsert / replace の2モード
	case http.MethodDelete:  ...
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
```

`deleteWord`（193行目〜）の末尾:

```go
if err := h.store.Delete(r.Context(), id); err != nil {
	http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
	return
}
w.WriteHeader(http.StatusNoContent)
```

→ **存在しない id でも `500`** になる。クライアントから「サーバーの不具合」と「対象が無い」が区別できない。

### `WordStore` interface

```go
type WordStore interface {
	LoadAll(ctx context.Context) ([]odai.WordEntry, error)
	LoadFiltered(ctx context.Context, category string, level int, hasLevel bool) ([]odai.WordEntry, error)
	SaveAll(ctx context.Context, entries []odai.WordEntry, mode string) error
	Delete(ctx context.Context, id int) error
}
```

### `internal/configapi/handler.go` の GET

```go
func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	gp, err := h.store.Load(r.Context())
	...
	writeJSON(w, http.StatusOK, gp)     // ← GameParameters をそのまま返している
}
```

---

## 2. 課題2: DELETE の未存在時を 404 にする（先にやる）

### 方針

`db.WordStore.Delete` が「何件消えたか」を返せるようにし、0件なら 404。

`internal/db/words.go`:

```go
// Delete は id の1語を削除する。存在しなければ ErrNotFound を返す。
func (s *WordStore) Delete(ctx context.Context, id int) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM words WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return odai.ErrNotFound
	}
	return nil
}
```

**エラーの置き場所は `internal/odai` 一択**。理由は depguard:

```yaml
# .golangci.yml — http-adapters-boundary
files: ["**/internal/configapi/**"]
deny:
  - pkg: "takoda99/internal/db"
    desc: "configapi は db 実装に依存しない。Store インターフェースを合成ルートが注入する。"
```

**`configapi` から `db` は import できない**（CI が落ちる）。
一方 `configapi` も `db` も**既に `odai` に依存している**（`odai.WordEntry` を使う）ので、
そこに置けば両者から参照できる。

```go
// internal/odai/configurable.go
var ErrNotFound = errors.New("odai: word not found")
```

ハンドラ側:

```go
if err := h.store.Delete(r.Context(), id); err != nil {
	if errors.Is(err, odai.ErrNotFound) {
		http.Error(w, "word not found", http.StatusNotFound)
		return
	}
	http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
	return
}
w.WriteHeader(http.StatusNoContent)
```

> **確認コマンド**: 実装後に必ず回す。depguard 違反は CI で弾かれる。
> ```bash
> golangci-lint run
> ```

### テスト

```go
func TestDeleteWord_NotFound(t *testing.T) {
	h := NewWordsHandler(&stubWordStore{deleteErr: odai.ErrNotFound}, "tok", nil)
	req := httptest.NewRequest(http.MethodDelete, "/api/words/999", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestDeleteWord_Success(t *testing.T) {
	// 204 が返ることを確認（既存挙動を壊さない）
}
```

---

## 3. 課題3: GET /api/params に configHash を含める

### 方針

`GameParameters` に混ぜると**スキーマが汚れる**（config-front が保存時に送り返してくると
未知フィールドになる）。**ラッパで包む**か、**ヘッダで返す**。

#### 案A: レスポンスをラップする（破壊的）

```json
{ "params": {...}, "configHash": "a1b2c3d4" }
```

→ config-front の既存パース処理を壊す。**採らない**（issue も「既存フィールドは壊さない」と言っている）。

#### 案B: トップレベルに1フィールド足す（採用）

```json
{ "session": {...}, "credit": {...}, ..., "configHash": "a1b2c3d4" }
```

`GameParameters` を直接返すのをやめ、マップに変換して1キー足す:

```go
func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		http.Error(w, "config store not available (DATABASE_URL 未設定)", http.StatusServiceUnavailable)
		return
	}
	gp, err := h.store.Load(r.Context())
	if err != nil {
		http.Error(w, "load failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// GameParameters をそのまま出しつつ configHash を1つ足す。
	// POST 側は未知フィールドを無視するので、これを送り返されても壊れない。
	raw, err := json.Marshal(gp)
	if err != nil {
		http.Error(w, "marshal failed", http.StatusInternalServerError)
		return
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		http.Error(w, "marshal failed", http.StatusInternalServerError)
		return
	}
	m["configHash"], _ = json.Marshal(gp.ConfigHash())
	writeJSON(w, http.StatusOK, m)
}
```

> **POST 側が `configHash` を受け取っても壊れないこと**を必ず確認する。
> `json.Unmarshal` は未知フィールドを既定で無視するが、`DisallowUnknownFields()` を
> 使っていると 400 になる。
> ```bash
> grep -n "DisallowUnknownFields" internal/configapi/handler.go
> ```
> 使っていたら `configHash` を明示的に捨てる処理を入れる。

#### 案C: レスポンスヘッダで返す

```go
w.Header().Set("X-Config-Hash", gp.ConfigHash())
```

body を一切触らないので最も安全。config-front が読めるなら**これが一番きれい**。
CORS で露出させるには `Access-Control-Expose-Headers` が要る:

```go
head.Set("Access-Control-Expose-Headers", "X-Config-Hash")
```

**案Bと案Cのどちらを採るかは config-front 側の都合で決める。** 判断は Unity/front 担当と合わせる。

### POST のレスポンスにも入れる

保存直後に照合できるよう、POST の成功レスポンスにも同じものを返す。
これがあれば **2秒キャッシュ問題**（保存直後の GET が古い値を返す）を運営が検知できる。

### テスト

```go
func TestGetParams_IncludesConfigHash(t *testing.T) {
	gp := game.DefaultParameters()
	h := NewHandler(&stubStore{gp: gp}, "tok", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/params", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var m map[string]json.RawMessage
	json.Unmarshal(rec.Body.Bytes(), &m)

	var got string
	json.Unmarshal(m["configHash"], &got)
	if got != gp.ConfigHash() {
		t.Fatalf("configHash=%q want %q", got, gp.ConfigHash())
	}
	// 既存フィールドが消えていないこと
	if _, ok := m["credit"]; !ok {
		t.Fatal("credit が消えている")
	}
}

func TestPostParams_IgnoresConfigHash(t *testing.T) {
	// configHash 付きの JSON を POST しても 400 にならないこと
}
```

---

## 4. 課題1: PATCH /api/words/{id}

### 現状の何が困るか

`POST /api/words` は upsert / replace の2モードのみ。**1語の誤字を直すだけ**で:

- `upsert` で全件送り直す、または
- `DELETE` してから `POST` する

の2段になる。運営の操作としては重い。

### 実装

`WordStore` に1メソッド足す:

```go
type WordStore interface {
	LoadAll(ctx context.Context) ([]odai.WordEntry, error)
	LoadFiltered(ctx context.Context, category string, level int, hasLevel bool) ([]odai.WordEntry, error)
	SaveAll(ctx context.Context, entries []odai.WordEntry, mode string) error
	Update(ctx context.Context, id int, patch odai.WordPatch) error   // ← 追加
	Delete(ctx context.Context, id int) error
}
```

部分更新なのでポインタで「指定された項目だけ」を表す:

```go
// odai.WordPatch は words の部分更新。nil のフィールドは変更しない。
type WordPatch struct {
	Text           *string `json:"text,omitempty"`
	Reading        *string `json:"reading,omitempty"`
	KeystrokeCount *int    `json:"keystrokeCount,omitempty"`
	Level          *int    `json:"level,omitempty"`
	Category       *string `json:"category,omitempty"`
}
```

DB 側は COALESCE で1文にできる:

```go
func (s *WordStore) Update(ctx context.Context, id int, p odai.WordPatch) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE words SET
			text            = COALESCE($2, text),
			reading         = COALESCE($3, reading),
			keystroke_count = COALESCE($4, keystroke_count),
			level           = COALESCE($5, level),
			category        = COALESCE($6, category)
		WHERE id = $1`,
		id, p.Text, p.Reading, p.KeystrokeCount, p.Level, p.Category)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return odai.ErrNotFound
	}
	return nil
}
```

> `*string` / `*int` の nil は pgx が SQL NULL として渡すので `COALESCE` がそのまま効く。

ルーティング:

```go
case http.MethodPatch:
	h.patchWord(w, r, idFromPath(r.URL.Path))
```

CORS の許可メソッドにも追加:

```go
head.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
```

### 注意: `keystrokeCount` の再計算は **`reading`** から

`reading`（読み）を変えたのに `keystrokeCount` を変えないと、**打鍵数と実際の語がズレる**。
評価の精度計算（`missCount / keystrokeTotal`）が狂う。

**打鍵数は `text`（表示文字）ではなく `reading`（読み）から算出する**。
既存コードが2箇所でそうしているので合わせること:

```go
// internal/db/words.go:117
ks = odai.Keystrokes(e.Reading)

// internal/configapi/words.go:173
req.Words[i].KeystrokeCount = odai.Keystrokes(req.Words[i].Reading)
```

`odai.Keystrokes(reading string) int` は**既に公開済み**（`internal/odai/configurable.go:59`）。
新たに公開する作業は不要。

PATCH では:

```go
if p.Reading != nil && p.KeystrokeCount == nil {
	n := odai.Keystrokes(*p.Reading)
	p.KeystrokeCount = &n
}
```

`text` だけ変えた場合（誤字修正など）は打鍵数に影響しないので再計算しない。

**これを忘れると静かに壊れる**ので、テストで固定する。

---

## 5. 課題4（別リポジトリ・追跡のみ）

`Takoda99-Docs/02_共通仕様/03_パラメータ仕様.md` の乖離。**本リポジトリでは対応しない**。

- `matchTimeLimitMs` が残っている（proto v0.3.0 で削除済み・#33）
- `penaltyClaimer` が未実装
- キー名がフラット表記のまま（実際は入れ子構造）
- `presentation.*` が未記載（#64 で追加済み）

Takoda99-Docs 側の issue として起票する。

---

## 6. ローカル確認

```bash
go build ./... && go vet ./...
go test ./internal/configapi/ -v
```

```bash
# 手動確認（DATABASE_URL 必要）
curl -s https://takoda99.mooo.com/api/params | jq .configHash
curl -i -X DELETE -H "X-Admin-Token: $TOKEN" https://takoda99.mooo.com/api/words/999999   # → 404
curl -i -X PATCH -H "X-Admin-Token: $TOKEN" -H "Content-Type: application/json" \
     -d '{"text":"たこやき"}' https://takoda99.mooo.com/api/words/1                       # → 200
```

---

## 7. 完了条件

**課題2（先行）**
- [ ] `Delete` が未存在時に `ErrNotFound` を返す
- [ ] ハンドラが未存在時に **404**、成功時に **204** を返す
- [ ] テストで404/204の両方を固定

**課題3**
- [ ] `GET /api/params` が `configHash` を返す（案B or 案C。config-front と合意済み）
- [ ] **既存フィールドが1つも壊れていない**
- [ ] `configHash` 付きの JSON を POST しても 400 にならない
- [ ] POST の成功レスポンスにも `configHash` が入る

**課題1**
- [ ] `PATCH /api/words/{id}` が動く（`X-Admin-Token` 必須）
- [ ] nil のフィールドは変更されない
- [ ] 未存在 id で 404
- [ ] **`reading` 更新時に `keystrokeCount` が再計算される**（`text` のみの変更では再計算しない）
- [ ] `golangci-lint run` が通る（configapi が db を import していない）
- [ ] CORS の許可メソッドに `PATCH` が入っている

**課題4**
- [ ] Takoda99-Docs 側に issue を起票した
