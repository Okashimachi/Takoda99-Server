# Plan-07: お題単語データの整備

> **目的**: お題単語プールを大阪/関西テーマで整備する。fire力(Heat)に応じた難度別プールを構築し、config-front で追加/編集/削除/CSVインポートを可能にする。DBが空・取得失敗時はハードコードのフォールバック語彙で動く。
> **対応issue**: 新規
> **依存**: Plan-01（基盤移行）, Plan-06（DB基盤 + config-front デプロイ）
> **参照**: 企画書 section3/8, 用語集 section4, パラメータ仕様 section8

---

## 1. 前提知識

### お題の役割

プレイヤー（たこ焼き店主）は来店した客のセリフではなく、**寿司打と同じような単語**を打つ。客が注文として出す語を正確に素早く打つ = たこ焼きを焼いて提供する、というメタファー。火力(Heat)が上がると難しい語が出る。

### 現在の WordSource アーキテクチャ

```
game/ports.go:
  type Word struct { Text string; KeystrokeCount int }
  type WordSource interface {
      Next(effectiveLevel int, rng *rand.Rand) Word
      // 注: NextTrap は Plan-01 で削除済み（たこ焼き版にトラップ機構はない）。
      //     このプランは Next のみを実装する。
  }

odai/pool.go:
  type StaticPool struct { wordsByLevel map[int][]game.Word }
  func NewStaticPool() *StaticPool  // data.go の語彙を積む
  func (p *StaticPool) Next(effectiveLevel int, rng *rand.Rand) game.Word
  // 注: traps フィールドと NextTrap は Plan-01 で削除済み

odai/data.go:
  var rawWords = map[int][]string{
      0:  {"ねこ", "いぬ", ...},  // 2字・清音
      1:  {"さくら", "ひかり", ...},
      ...
      10: {"すーぱーこんぼ99", ...},
  }
```

### 打鍵数の算出

`odai/romaji.go` の `keystrokes(s string) int` が「かな/カタカナ/英数字混じり文字列」の正準ローマ字打鍵数を返す。方針は最短系: し=si(2), ち=ti(2), っ=子音重ね(+1), ん=n(1), 母音=1, その他基本かな=2, 英数字=1。

サーバー側で打鍵数を計算するため、config-front での単語登録時に「読み」を渡せばサーバーが打鍵数を算出できる。

### 注入先

`app.DefaultDeps()` が `odai.NewStaticPool()` を `Deps.Words` に入れ、`game.NewSession()` の `words WordSource` に渡る。`session.admitCustomer()` が `s.words.Next(s.wordLevel(), s.rng)` で語を引く。

---

## 2. 現状のコード

### `internal/odai/data.go`

```go
var rawWords = map[int][]string{
    0:  {"ねこ", "いぬ", "そら", "うみ", "やま"},           // 5語
    1:  {"さくら", "ひかり", "みどり", "ことり", "はなび"},   // 5語
    // ... level 2〜10 各5語（計55語）
}
// 注: rawTraps（旧 Textro の煽り長文トラップ）は Plan-01 で削除済み。

func placeholderWords() map[int][]game.Word  // rawWords → keystrokes 算出付きの Word
```

### `internal/odai/pool.go`

```go
type StaticPool struct {
    wordsByLevel map[int][]game.Word
}

func (p *StaticPool) Next(effectiveLevel int, rng *rand.Rand) game.Word {
    list := p.wordsByLevel[effectiveLevel]
    for l := effectiveLevel - 1; l >= 0 && len(list) == 0; l-- {
        list = p.wordsByLevel[l]  // 下位レベルへフォールバック
    }
    if len(list) == 0 { return fallbackWord }
    return list[rng.Intn(len(list))]
}
```

### `internal/odai/romaji.go`

```go
func keystrokes(s string) int  // ひらがな/カタカナ/英数字 → 打鍵数
```

---

## 3. DB スキーマ

`internal/db/words.go`（新規ファイル）に実装する。

```sql
CREATE TABLE IF NOT EXISTS words (
    id              SERIAL PRIMARY KEY,
    text            TEXT NOT NULL,          -- 表示テキスト（ひらがな/カタカナ/漢字混じり可）
    reading         TEXT NOT NULL,          -- 読み（全ひらがな。打鍵数算出の入力）
    keystroke_count INT NOT NULL,           -- 正準ローマ字打鍵数（サーバーが算出）
    level           INT NOT NULL DEFAULT 0, -- 難度レベル（0=易〜4=難）
    category        TEXT NOT NULL DEFAULT 'general',  -- 分類タグ
    UNIQUE(text, level)                     -- 同じ語が同レベルに重複しない
);
```

マイグレーション（冪等）:
```go
func (s *WordStore) Migrate(ctx context.Context) error {
    // CREATE TABLE IF NOT EXISTS ...
    // テーブルが空なら、data.go のフォールバック語彙を seed する
}
```

---

## 4. API 仕様

`/api/words` エンドポイントを `internal/configapi/handler.go` に追加（または別ファイル `internal/configapi/words.go`）。認証は `/api/params` と同じ `X-Admin-Token` ヘッダ。

### GET /api/words

全単語を JSON 配列で返す。クエリパラメータでフィルタ可能。

```
GET /api/words                → 全件
GET /api/words?level=2        → レベル2のみ
GET /api/words?category=osaka → カテゴリ "osaka" のみ
```

レスポンス:
```json
[
  {"id": 1, "text": "めっちゃ", "reading": "めっちゃ", "keystrokeCount": 5, "level": 1, "category": "osaka"},
  {"id": 2, "text": "たこやき", "reading": "たこやき", "keystrokeCount": 8, "level": 2, "category": "food"},
  ...
]
```

### POST /api/words

単語リストを一括保存する。モードは `replace`（全削除→挿入）と `upsert`（text+level で一致すれば更新、なければ挿入）。

```
POST /api/words
X-Admin-Token: <token>
Content-Type: application/json

{
  "mode": "upsert",
  "words": [
    {"text": "めっちゃ", "reading": "めっちゃ", "level": 1, "category": "osaka"},
    {"text": "なんぼ",   "reading": "なんぼ",   "level": 1, "category": "osaka"},
    ...
  ]
}
```

`keystrokeCount` はリクエストに含まなくてよい（サーバーが `reading` から `keystrokes()` で算出する）。含まれていれば上書きしない（config-front 側で手動指定した場合を尊重）。

### DELETE /api/words/:id

個別削除（config-front のUI用）。

```
DELETE /api/words/42
X-Admin-Token: <token>
```

---

## 5. WordSource の拡張

### ConfigurablePool（新規）

`internal/odai/configurable.go`（新規ファイル）:

```go
// ConfigurablePool は DB から取得した語彙で WordSource を満たす。
// DB取得失敗時はフォールバック(StaticPool)に委譲する。
type ConfigurablePool struct {
    wordsByLevel map[int][]game.Word
    fallback     *StaticPool  // DB が空/取得失敗時のフォールバック
}

// NewConfigurablePool は DB の語彙リストから構築する。
// entries が空ならフォールバック(StaticPool)のみで動く。
func NewConfigurablePool(entries []WordEntry) *ConfigurablePool {
    fb := NewStaticPool()
    if len(entries) == 0 {
        return &ConfigurablePool{
            wordsByLevel: fb.wordsByLevel,
            fallback:     fb,
        }
    }
    byLevel := make(map[int][]game.Word)
    for _, e := range entries {
        w := game.Word{Text: e.Text, KeystrokeCount: e.KeystrokeCount}
        byLevel[e.Level] = append(byLevel[e.Level], w)
    }
    return &ConfigurablePool{
        wordsByLevel: byLevel,
        fallback:     fb,
    }
}

func (p *ConfigurablePool) Next(effectiveLevel int, rng *rand.Rand) game.Word {
    // StaticPool と同じフォールバックロジック
    list := p.wordsByLevel[effectiveLevel]
    for l := effectiveLevel - 1; l >= 0 && len(list) == 0; l-- {
        list = p.wordsByLevel[l]
    }
    if len(list) == 0 {
        return p.fallback.Next(effectiveLevel, rng)
    }
    return list[rng.Intn(len(list))]
}

```

### WordEntry

```go
// WordEntry は DB/API で受け渡す単語データ。
type WordEntry struct {
    ID             int    `json:"id,omitempty"`
    Text           string `json:"text"`
    Reading        string `json:"reading"`
    KeystrokeCount int    `json:"keystrokeCount"`
    Level          int    `json:"level"`
    Category       string `json:"category"`
}
```

### 合成ルートでの配線

`cmd/server/main.go` の `loadDeps()` 内:

```go
loadDeps := func() app.Deps {
    d := baseDeps
    p, _ := provider.Load(ctx)
    d.Params = p
    // DB があれば語彙も DB から取得
    if ws != nil {  // ws = *db.WordStore
        entries, err := ws.LoadAll(ctx)
        if err != nil {
            log.Printf("odai: DB取得失敗。フォールバック語彙で続行: %v", err)
            // d.Words はデフォルトの StaticPool のまま
        } else {
            d.Words = odai.NewConfigurablePool(entries, nil)
        }
    }
    return d
}
```

これにより、config-front で語彙を編集 → **次のマッチから**新しい語彙が使われる（再起動不要）。

---

## 6. 単語リスト（初期データ: 関西弁/大阪テーマ 100語）

### Level 0（2-3文字、清音中心）: 20語

| text | reading | category | keystroke_count |
|---|---|---|---|
| たこ | たこ | food | 4 |
| うまい | うまい | osaka | 4 |
| あつい | あつい | general | 4 |
| やきめ | やきめ | food | 5 |
| ソース | そーす | food | 4 |
| あかん | あかん | osaka | 4 |
| ほんま | ほんま | osaka | 5 |
| やすい | やすい | osaka | 5 |
| うまー | うまー | osaka | 4 |
| でんき | でんき | general | 5 |
| おいし | おいし | general | 4 |
| ねぎ | ねぎ | food | 4 |
| まち | まち | general | 4 |
| えき | えき | general | 3 |
| そら | そら | general | 4 |
| みせ | みせ | general | 4 |
| てつ | てつ | general | 4 |
| あめ | あめ | general | 3 |
| はし | はし | general | 4 |
| かわ | かわ | general | 4 |

### Level 1（3-4文字、濁音混じり）: 20語

| text | reading | category | keystroke_count |
|---|---|---|---|
| めっちゃ | めっちゃ | osaka | 5 |
| なんぼ | なんぼ | osaka | 5 |
| おおきに | おおきに | osaka | 6 |
| しゃーない | しゃーない | osaka | 7 |
| てっぱん | てっぱん | food | 6 |
| マヨネーズ | まよねーず | food | 7 |
| かつおぶし | かつおぶし | food | 9 |
| あおのり | あおのり | food | 6 |
| たこやき | たこやき | food | 8 |
| ぱりぱり | ぱりぱり | food | 8 |
| おばちゃん | おばちゃん | osaka | 7 |
| だんじり | だんじり | osaka | 7 |
| にいちゃん | にいちゃん | osaka | 7 |
| あきない | あきない | osaka | 6 |
| うどん | うどん | food | 4 |
| ねぎやき | ねぎやき | food | 7 |
| おかん | おかん | osaka | 4 |
| いちびる | いちびる | osaka | 7 |
| ちゃうちゃう | ちゃうちゃう | osaka | 8 |
| なにわ | なにわ | place | 5 |

### Level 2（4-5文字、拗音/促音/カタカナ混じり）: 20語

| text | reading | category | keystroke_count |
|---|---|---|---|
| つうてんかく | つうてんかく | place | 10 |
| どうとんぼり | どうとんぼり | place | 10 |
| くいだおれ | くいだおれ | place | 8 |
| しんせかい | しんせかい | place | 8 |
| いらっしゃい | いらっしゃい | osaka | 8 |
| えげつない | えげつない | osaka | 7 |
| たこやきや | たこやきや | food | 9 |
| おこのみやき | おこのみやき | food | 10 |
| いかやき | いかやき | food | 7 |
| くしかつ | くしかつ | food | 7 |
| かすうどん | かすうどん | food | 8 |
| ぶたまん | ぶたまん | food | 6 |
| なんばグランド | なんばぐらんど | place | 11 |
| しんさいばし | しんさいばし | place | 10 |
| てんのうじ | てんのうじ | place | 8 |
| おもろい | おもろい | osaka | 6 |
| かなわんわ | かなわんわ | osaka | 8 |
| まいどまいど | まいどまいど | osaka | 10 |
| はりきって | はりきって | general | 8 |
| もうかりまっか | もうかりまっか | osaka | 10 |

### Level 3（5-7文字、長文寄り）: 20語

| text | reading | category | keystroke_count |
|---|---|---|---|
| なんでやねん | なんでやねん | osaka | 10 |
| どないしたん | どないしたん | osaka | 10 |
| えらいこっちゃ | えらいこっちゃ | osaka | 9 |
| かんにんしてや | かんにんしてや | osaka | 12 |
| よういわんわ | よういわんわ | osaka | 9 |
| いてこましたる | いてこましたる | osaka | 12 |
| しゃーないやん | しゃーないやん | osaka | 9 |
| おもろすぎるやろ | おもろすぎるやろ | osaka | 13 |
| ほんまかいな | ほんまかいな | osaka | 9 |
| あきまへんで | あきまへんで | osaka | 9 |
| ちょっとまってや | ちょっとまってや | osaka | 11 |
| つきあたりひだり | つきあたりひだり | place | 14 |
| おいしなっとる | おいしなっとる | osaka | 10 |
| まけときまっせ | まけときまっせ | osaka | 11 |
| あんじょうたのむ | あんじょうたのむ | osaka | 11 |
| いっぺんたべてみ | いっぺんたべてみ | food | 12 |
| てっぱんのうえで | てっぱんのうえで | food | 12 |
| ひっくりかえして | ひっくりかえして | food | 12 |
| できたてあつあつ | できたてあつあつ | food | 12 |
| ソースたっぷりで | そーすたっぷりで | food | 11 |

### Level 4（7文字以上、全部盛り）: 20語

| text | reading | category | keystroke_count |
|---|---|---|---|
| いらっしゃいおいでやす | いらっしゃいおいでやす | osaka | 14 |
| おおさかじょうこうえん | おおさかじょうこうえん | place | 14 |
| ユニバーサルスタジオ | ゆにばーさるすたじお | place | 14 |
| きたしんちのよるは | きたしんちのよるは | place | 14 |
| たこやきひゃっこたべた | たこやきひゃっこたべた | food | 16 |
| ソースとマヨのハーモニー | そーすとまよのはーもにー | food | 15 |
| てっぱんでじゅーじゅー | てっぱんでじゅーじゅー | food | 14 |
| おこのみやきもうまいで | おこのみやきもうまいで | food | 16 |
| なんばのまちをあるいた | なんばのまちをあるいた | place | 16 |
| かにどうらくのかんばん | かにどうらくのかんばん | place | 16 |
| どうとんぼりでくいだおれ | どうとんぼりでくいだおれ | place | 18 |
| つうてんかくからのけしき | つうてんかくからのけしき | place | 18 |
| たこやきはそとぱりなかとろ | たこやきはそとぱりなかとろ | food | 20 |
| おおさかのおばちゃんはつよい | おおさかのおばちゃんはつよい | osaka | 18 |
| なにわのしょうにんこんじょう | なにわのしょうにんこんじょう | osaka | 18 |
| しんせかいのジャンジャンよこちょう | しんせかいのじゃんじゃんよこちょう | place | 21 |
| おおさかのたこやきせかいいち | おおさかのたこやきせかいいち | food | 20 |
| あべのハルカスてっぺんまで | あべのはるかすてっぺんまで | place | 18 |
| まいどおおきにありがとう | まいどおおきにありがとう | osaka | 16 |
| ほんまにおいしいたこやきやで | ほんまにおいしいたこやきやで | food | 20 |

注: keystroke_count の値は `romaji.go` の `keystrokes()` で算出した概算。実装時に正確な値へ再計算する。

---

## 7. config-front のお題管理ページ

### ページ構成

パラメータ調整ページ（`/params`）とは**別ページ**（`/words`）として実装。ナビゲーションにタブを追加:

```
[パラメータ調整] [お題管理]
```

### 機能一覧

1. **単語一覧表示**: テーブル形式。レベル別タブまたはフィルタ。各行に text / reading / keystrokeCount / level / category を表示。
2. **単語の追加**: フォームで text / reading / level / category を入力。keystrokeCount は reading から自動算出（config-front 側で簡易計算、または POST 後にサーバーが算出した値で更新）。
3. **単語の編集**: 行をクリックして inline 編集、または編集モーダル。
4. **単語の削除**: 行ごとの削除ボタン + 確認ダイアログ。
5. **CSV インポート**: `text,reading,level,category` のCSVファイルをアップロード → POST /api/words で一括保存。
6. **CSV エクスポート**: 全単語を CSV でダウンロード。
7. **打鍵数の再計算**: 一括で全語の keystrokeCount をサーバー側で再算出するボタン（ローマ字テーブル更新時用）。

### 打鍵数の自動計算

config-front 側で `romaji.go` のロジックを TypeScript に移植するか、POST 時にサーバーが算出して返す方式のどちらか。
推奨: **サーバー算出**（正典が `romaji.go` にあり、二重管理を避ける）。POST レスポンスに `keystrokeCount` 付きの語を返し、config-front は表示を更新する。

---

## 8. 実装手順（まとめ）

1. `internal/db/words.go` を新規作成: `WordStore` + `Migrate` + `LoadAll` + `SaveAll` + `Delete`
2. `internal/odai/configurable.go` を新規作成: `ConfigurablePool` + `WordEntry`
3. `internal/configapi/words.go` を新規作成: GET/POST/DELETE `/api/words` ハンドラ
4. `cmd/server/main.go` で配線: WordStore の Migrate + loadDeps で ConfigurablePool を構築
5. `internal/odai/data.go` を大阪/関西テーマのフォールバック語彙に差し替え（level 0〜4、各20語）
6. config-front に `/words` ページを追加
7. 初期データ（上の100語）を CSV 化し、config-front からインポートして動作確認

---

## 9. ローカル確認

```bash
# サーバー起動（DB付き）
DATABASE_URL="postgres://takoda:dev@localhost:5432/takoda99?sslmode=disable" \
CONFIG_ADMIN_TOKEN="devtoken" \
go run ./cmd/server --mode solo

# 全語取得（初回は seed されたフォールバック語彙が返る）
curl http://localhost:8080/api/words | jq '. | length'

# 語の追加
curl -X POST http://localhost:8080/api/words \
  -H "Content-Type: application/json" \
  -H "X-Admin-Token: devtoken" \
  -d '{
    "mode": "upsert",
    "words": [
      {"text": "めっちゃ", "reading": "めっちゃ", "level": 1, "category": "osaka"},
      {"text": "なんぼ", "reading": "なんぼ", "level": 1, "category": "osaka"}
    ]
  }'

# レベル別取得
curl "http://localhost:8080/api/words?level=1" | jq .

# 削除
curl -X DELETE http://localhost:8080/api/words/1 -H "X-Admin-Token: devtoken"

# solo モードで試合を開始し、CustomerArrived の words が大阪テーマになっていること確認
# wscat -c ws://localhost:8080/ws で接続し、CustomerArrived メッセージを観察
```

---

## 10. 完了条件

- [ ] `words` テーブルが自動作成される（`WordStore.Migrate`）
- [ ] 大阪/関西テーマの語彙が最低100語、レベル0〜4に分布している
- [ ] GET/POST/DELETE `/api/words` が動作し、`X-Admin-Token` で保護されている
- [ ] `ConfigurablePool` が `WordSource` を満たし、DB語彙で出題が変わる
- [ ] DB取得失敗時・DB空のときに `StaticPool`（フォールバック語彙）で動く
- [ ] config-front の「お題管理」ページで単語の一覧表示/追加/編集/削除ができる
- [ ] CSV インポートで一括登録ができる
- [ ] 語彙の変更が次のマッチから反映される（再起動不要）
- [ ] テスト: ConfigurablePool の Next がレベルフォールバック含めて正しく動く
