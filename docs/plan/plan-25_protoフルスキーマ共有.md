# Plan-25: GameParameters をフルスキーマで proto 共有型へ寄せる

> **目的**: フル `GameParameters` を proto の共有型として定義し、Go / TS / C# の単一ソースにする。
> **対応issue**: #19
> **優先度**: **低（イベント後）**。当日の成立には不要。
> **⚠ 後回しにすることで抱える問題は §0.5 に明記してある。無害な先送りではない。**
> **依存**: たこ焼き版パラメータが固まってから
> **要承認**: proto の変更なので**実装前にりーせの承認が要る**（AGENTS.md 1.2）

---

## 0. 現状の割り切り

| 対象 | 今 |
|---|---|
| on-wire の契約 | `GameParametersPublicSubset`（3フィールドのみ）だけが proto にある |
| フルスキーマ | サーバーの `internal/game/params.go`（12セクション） |
| config-front | TS で**手ミラー**（`lib/params.ts`） |

**手ミラーが問題**。Go 側でフィールドを足しても TS 側は自動で追随しないので、
Plan-20 で「キー名まで一致しているか」を人間が突き合わせる作業が発生している。

ズレると:
- POST 時に未知のキーは無視され、欠けたキーはゼロ値になる
- `backfillDefaults` はセクション丸ごとゼロの時しか救済しない
- **セクション内の一部フィールドだけ欠けると 0 が保存されて試合が壊れる**（例: `patience.lateMul=0` で0除算）

---

## 0.5 後回しにすることで抱える問題（承知の上で先送りする）

「やらない」ではなく「**問題を抱えたまま運用する**」という判断。何を我慢しているかを明示する。

### 問題1: スキーマのズレが静かに壊す（実害あり）

Go を変えても TS は自動で追随しない。ズレると:

| ズレ方 | 起きること |
|---|---|
| TS に**余分な**キーがある | サーバーが無視するだけ。実害なし |
| TS に**キーが欠けている** | **その項目がゼロ値で保存される** |

後者が危険。`backfillDefaults` は**セクション丸ごとゼロ**の時しか救済しないので、
**セクション内の一部フィールドだけ欠けると 0 が入る**。

現行 `Validate()` が既に弾く項目（＝安全）:

```
customer.total / credit.initialLife / session.tickIntervalMs
bot.baseElapsedMs / bot.baseAccuracy / heat.maxLevel
storm.intervalTicks / storm.thresholdPct / phase.midAliveThreshold
```

**まだ弾いていない項目**（ここが穴）:

| キー | 0 になると |
|---|---|
| `distribution.queueRefillThreshold` | **客が1人も配られない**。`len(queue) < 0` は常に偽なので分配候補が空になり、試合は始まるが誰も何もできない |
| `matching.minPlayers` | 待機0人でもカウントダウンが始まりうる |
| `eval.emaAlpha` | `evalRaw` が更新されず全店が同値。評価が機能しない |
| `session.publishIntervalMs` | 盤面が毎tick配信され帯域が跳ねる |

> `patience.lateMul` は `stepPatience` 側に `> 0` のガードがあるので**0除算は起きない**
> （Late での短縮が効かなくなるだけ）。全部が全部むき出しではない。

**config-front で保存ボタンを押した瞬間に本番が壊れる**類の事故になるのは上表の項目。

### 問題2: 検出が人力

現状の防御は Plan-20 の「キー名を目視で突き合わせる」だけ。
**新しいセクションを足すたびに人間が照合する必要がある**。忘れたら問題1が発動する。

### 当面の緩和策（必ずやること）

先送りする代わりに、以下で被害を抑える:

1. **`Validate()` に未検証の項目を足す** — 上表の4つ。
   これは proto と無関係にサーバー側だけで完結し、**最も費用対効果が高い**。
   ```go
   if gp.Distribution.QueueRefillThreshold <= 0 {
       return fmt.Errorf("distribution.queueRefillThreshold は正である必要 (got %d)",
           gp.Distribution.QueueRefillThreshold)
   }
   if gp.Matching.MinPlayers <= 0 { ... }
   if gp.Eval.EmaAlpha <= 0 || gp.Eval.EmaAlpha > 1 { ... }
   if gp.Session.PublishIntervalMs <= 0 { ... }
   ```
   POST が 400 で弾かれれば、壊れた値は保存されない。
2. **パラメータを増やしたら Plan-20 の突き合わせを必ず実施**
3. **§4 の代替案（スキーマ一致テスト）を先に検討** — proto を触らずにズレを検出できる

> **イベント前に最低限やるべきは §4 ではなく上記1**。`Validate()` の強化だけは
> 本プランの着手を待たずに入れること。

---

## 1. なぜ今やらないか

1. **パラメータがまだ動く**。イベント前後で項目が増減する可能性が高い。動いているうちに単一ソース化すると、変更のたびに3言語＋proto のリリースが要って逆に遅くなる
2. **`GameParametersPublicSubset` は既に proto にある**。on-wire の契約としては足りている
3. **手ミラーのズレは Plan-20 の突き合わせで検出できる**。当日までの回避策として成立している

イベントが終わって**パラメータが安定してから**やるのが正しい順序。

---

## 2. やること（将来）

### Step 1: proto にフルスキーマを定義

`Takoda99-Proto/proto/messages.go` に 12 セクション全てを定義する。

**制約**: サーバーは `GameParameters` を `==` で比較している（`backfillDefaults` が
`gp.Phase == (game.PhaseParams{})` の形で使う）。proto 側の型も
**comparable に保つ**（map / slice を入れない）。

```go
// Takoda99-Proto 側
type GameParameters struct {
	Session      SessionParams      `json:"session"`
	Matching     MatchingParams     `json:"matching"`
	Credit       CreditParams       `json:"credit"`
	Customer     CustomerParams     `json:"customer"`
	Eval         EvalParams         `json:"eval"`
	Phase        PhaseParams        `json:"phase"`
	Heat         HeatParams         `json:"heat"`
	Storm        StormParams        `json:"storm"`
	Distribution DistributionParams `json:"distribution"`
	Patience     PatienceParams     `json:"patience"`
	Presentation PresentationParams `json:"presentation"`
	Bot          BotParams          `json:"bot"`
}
```

### Step 2: TS / C# ミラーを生成

Takoda99-Proto は既に Go / TS / C# のミラーを持っている。同じ方式で追加する。

### Step 3: サーバーを proto の型へ寄せる

`internal/game/params.go` の型定義を消し、`internal/proto` 経由で参照する。

```go
type GameParameters = proto.GameParameters
```

**ただし `Validate()` と `DefaultParameters()` と `ConfigHash()` は game 側に残す**。
これらは「ゲームのルール」であって契約ではない。proto は型だけを持つ。

> AGENTS.md 1.2「`internal/proto/messages.go` は canonical の薄い再輸出ラッパ。
> ここに独自の型を生やさない」との整合を保つ。

### Step 4: config-front の手ミラーを廃止

`lib/params.ts` を消し、proto の TS 型を import する。

### Step 5: 一致担保のテスト

proto と DB の JSON 形が一致していることをテストで固定する。

```go
func TestGameParameters_JSONRoundTrip(t *testing.T) {
	def := game.DefaultParameters()
	b, _ := json.Marshal(def)
	var got game.GameParameters
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got != def {   // comparable なので == で比較できる
		t.Fatal("round trip でズレた")
	}
}
```

---

## 3. 判断のポイント

やる前に確認すること:

- [ ] パラメータの項目が安定したか（直近1ヶ月で増減していないか）
- [ ] proto のリリース手順が回っているか（3言語のミラー更新が負担になっていないか）
- [ ] config-front 以外に TS の消費者が増えたか（増えたなら単一ソース化の価値が上がる）

**1つも当てはまらないなら、まだやらなくてよい。**

---

## 4. 代替案（もっと安い解決）

フル単一ソース化は重い。手ミラーのズレを検出するだけなら**もっと安い方法がある**:

### 案: スキーマ一致テストを CI に置く

サーバーが `GET /api/params/schema` でキー一覧を返し、config-front の CI がそれと
自分の型を突き合わせる。

```go
// キー一覧だけを返す（値は不要）
func schemaKeys(gp GameParameters) []string { ... }
```

これなら proto を触らずにズレを検出できる。**#19 を丸ごとやる前に、まずこちらを検討する。**

---

## 5. 完了条件

> **本プランは着手前に判断が要る。** §3 のチェックが通らなければ着手しない。

- [ ] りーせが proto 変更を承認した
- [ ] proto に フル `GameParameters` が Go/TS/C# で定義されている
- [ ] 全型が comparable（`==` 比較が壊れていない）
- [ ] サーバーが proto の型を参照している
- [ ] `Validate()` / `DefaultParameters()` / `ConfigHash()` は game 側に残っている
- [ ] config-front が proto の TS 型を import し、手ミラーが消えている
- [ ] JSON ラウンドトリップのテストがある
- [ ] DB に保存済みの既存 config が読める（後方互換）
