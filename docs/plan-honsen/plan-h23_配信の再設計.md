# Plan-h23: 配信の再設計（ランキング・配信順序・間引き）

> **目的**: `StoreListUpdate` の全店フル定期配信をやめ、**`RankingSnapshot`（全量）＋ `RankingDelta`（差分）** へ置き換える。あわせて**足切り時の配信順序を固定**し、`StoreEliminatedBatch` で脱落をまとめて配り、`EvaluationUpdate` を間引く。
> **依存**: h21（score）・h22（cullSchedule と finalRank）
> **正典**: [30_通信シーケンス](../../../Takoda99-Docs/00_本選差分/30_通信シーケンス.md) / [10_差分_プロト §2.4](../../../Takoda99-Docs/00_本選差分/10_差分_プロト.md) / `plan-h10`
> **範囲**: `internal/transport/` ・ `internal/room/` ・ `internal/game/`（Outbound の生成）
> **関連issue**: #81（`StoreListUpdate` の間引き未実装で egress が無料枠を割る）

---

## ▶ このファイル単体を実装プロンプトにする場合

1. **先に読む**: `AGENTS.md` → `docs/architecture.md` → `docs/plan-honsen/plan-h20` → **h21・h22**（score / cull / finalRank が前提）→ `docs/plan-honsen/plan-h10`（proto 契約）。
2. **前提チェック**（満たさなければ先に解消）:
   - `grep -rn "CullParams\|stepCull" internal/game/` がヒット（**h22 まで入っている**）。無ければ h21→h22 を先に。
   - `grep -rn "RankingSnapshot" internal/proto/messages.go` がヒット（h20-A の再輸出済み）。
3. **進め方**: `main` へ直 push しない。`feat/h23-publish` 等。配信は**スパイン（transport / room）**の仕事で、
   `internal/game/` は Outbound を順序付きで返すだけ（純粋性維持・§1.4）。proto には触らない。
4. **完了の定義**: 末尾「§7 完了条件」を全て満たす。**特に「SA1019 除外を外して緑」が本 plan の完了条件**
   （＝廃止フィールドの参照がサーバーから消えた証拠。plan-h20 §2.2）。`go test`・`golangci-lint run` が緑。
5. **PR 後**: `gh pr checks <N>` で CI 実走・緑を確認。スタック時は close→reopen。

---

## 0. 前提知識

| ファイル | 内容 |
|---|---|
| `internal/transport/publisher.go` | `StatePublisher` / `FullPublisher`（毎 `PublishIntervalMs` に全店フル配信） |
| `internal/transport/connection.go` | `sendBuffer = 64`（:57）／`Send` はキュー満杯で**接続を切る**（:155） |
| `internal/room/room.go` | `publish()`（:96）／`dispatch()`（:139）／`envelopeOf()`（:157） |
| `internal/game/session.go` | `Snapshot()`（:181）／`summaries()`／Outbound の生成 |

### なぜ変えるのか

1. **帯域**（#81）: 99店 × 99人 × 毎tick は O(N²)。予選の実測は **57KB/s/client ≒ 45Mbps・1試合675MB** で、
   GCP 無料枠の egress を1〜2試合で割る。
2. **観戦要件**: 脱落後も**全プレイヤーの順位**が見えること（6-E）。上位N名に絞る方式では満たせない。
3. **順序**: 予選は足切り時に複数メッセージが同時に届いて表示が崩れた（0章）。順序を契約として固定する。

---

## 1. ランキング配信

### 1.1 メッセージ（proto v0.8.0・定義済み）

```go
RankingSnapshot{ Entries []RankingEntry }              // 全量・低頻度
RankingEntry{ StoreId, Rank, Score, Alive }

RankingDelta{ Entries []RankingChange }                // 差分・高頻度
RankingChange{ StoreId, Score, Alive }                 // ★Rank を持たない
```

- **`Rank` の意味**: 生存店は**現在順位**、脱落店は**確定順位（以後不変）**。これで観戦時に99店を1本の Rank で並べられる。
- **`DisplayName` は送らない**。`MatchStart.stores[]` で配布済み（約束 2-D）。
- **差分に `Rank` を入れない理由**: Rank は相対値なので1店のスコア変動で間の全店の順位がずれ、
  「変化した店だけ送る」という差分の利点が消える。クライアントは `Score` でソートして表示順を復元し、
  **自店の権威 Rank は `EvaluationUpdate`** から取る。

### 1.2 ★実装は「全量のみ」から始める

| 方式 | 1クライアント | 99台合計 | 1試合(120秒) |
|---|---|---|---|
| 予選 `StoreListUpdate`（問題視された値） | 57 KB/s | 45 Mbps | 約 675 MB |
| **全量のみ 1Hz（本 plan の初期実装）** | 6 KB/s | 4.8 Mbps | **約 71 MB** |
| 全量4秒＋差分2Hz（後から config で有効化） | 2.4 KB/s | 1.9 Mbps | 約 28 MB |

**会場Wi-Fiは全量のみでも余裕**（45→4.8Mbps）。差分が効くのは egress コストなので、
**まず全量で確実に動かし、差分は後から**。契約（`RankingDelta`）は既にあるので proto を再度触らずに足せる。

### 1.3 差分の判定基準（差分を有効化するとき）

**epsilon 不要。** `score` は整数アキュムレータで `OrderServed` の瞬間しか動かないため、
`ApplyOrderServed` で dirty フラグを立てれば**厳密かつ安価**に「変化した店」が取れる。

### 1.4 `StoreListUpdate` をやめる

`FullPublisher` の定期配信を停止する。**`MatchStart.stores[]` の `StoreSummary` は継続使用**
（初期状態と表示名の唯一の供給源）。`StoreSummary.Score` を埋めること（h21 で追加済み）。

> `StatePublisher` interface は**差し替え可能な形のまま残す**（AGENTS.md §4.3 の設計意図）。
> 実装を `RankingPublisher` に差し替える形にすれば、フル⇔差分の切り替えも同じ継ぎ目で行える。

---

## 2. `StoreEliminatedBatch`（足切りの一括配信）

### 2.1 なぜ必要か（実害）

`StoreEliminated` は1店1メッセージ。**24店脱落＝24 Envelope**になる。
送信キューは `sendBuffer = 64`（`connection.go:57`）で、**溢れた接続は即座に切られる**
（slow-consumer eviction, 同 `:155`）。足切りの瞬間に脱落通知＋`EvaluationUpdate`＋`RankingSnapshot`＋
`ForcedEliminationWarning` が1tickで殺到すると、**軽く詰まっただけの健全なクライアントが
最も盛り上がる瞬間に切断され得る**。

1メッセージに畳めばこのリスクが消え、クライアントの演出集約（4-C）も素直になる。

```go
StoreEliminatedBatch{ StageIndex int, Entries []StoreEliminated }
```

### 2.2 個別の `StoreEliminated` はもう送らない

足切りが唯一の脱落経路になったので、**単発送信は発生しない**。`envelopeOf` からは外さず
（型は残る）、`game` 側が Batch を1つ返す形にする。

---

## 3. ★配信順序の固定（契約の一部）

型だけでは表現できないが、**守らないとフローが再現できない**。

### 3.1 足切りステージ（20/40/60/80/100秒）

```
1. StoreEliminatedBatch      … 誰が落ちたかを先に配る
2. PersonalResult            … 脱落した店にだけ（全員の試合終了を待たない）
3. EvaluationUpdate          … 生存店の新しい順位
4. RankingSnapshot           … 全量で整合をとる（大量の順位変動の直後なので差分にしない）
5. ForcedEliminationWarning  … 次ステージの秒読み
```

> **順位を配る前に脱落を配る。** 逆順だと脱落者を含んだ順位が一瞬表示される（予選のつまずき 4-A）。

### 3.2 試合終了（120秒）

```
1. StoreEliminatedBatch   … 残り10店（finalRank 1..10）を全員へ
2. PersonalResult         … 10店それぞれへ
3. RankingSnapshot        … ★最終スコアを全員へ
4. MatchEnd               … 空。全体の締め
```

> **3 を省略しない。** `StoreEliminated` は `score` を持たないため、これが無いと
> 「優勝 たこ太 12,400点」のスコア表示ができない。`MatchEnd` を拡張せずに済ませる条件（plan-h10 §1.6）。

### 3.3 実装上の注意

`game.Session` は `[]Outbound` を**順序付きで返す**。`room.dispatch` は受け取った順に配信するので、
**Outbound を append する順序がそのまま配信順序になる**。ここを崩さない。

---

## 4. 配信頻度（間引き）

| メッセージ | 分類 | 頻度 | 根拠 |
|---|---|---|---|
| `EvaluationUpdate` | 定期更新 | **2〜4Hz** | 自分のスコアと順位。唯一の指標なので遅延が体験に直結 |
| `ForcedEliminationWarning` | 定期更新 | **1〜2Hz** | 秒読みはクライアントがローカル補間する（3-B）ので高頻度不要 |
| `RankingSnapshot` | 定期更新 | **1Hz**（初期実装） | 全店ぶん |
| `RankingDelta` | 定期更新 | 1〜2Hz（後から） | 変化した店のみ |
| `DifficultyUpdate` | 定期更新 | 低頻度 | 表示優先度が低い |
| `CustomerArrived` | イベント | 提供完了後すぐ | **お題が途切れないことが最優先** |

### 4.1 🔴 `EvaluationUpdate` は現在「毎tick・全生存店」

`stepNormalize`（h21 で `stepRank`）が**毎tick全生存店へ**返している（`session.go:653`）。
仕様は 2〜4Hz なので**間引きが要る**（足すのではなく減らす作業）。

> 間引きは**配信層（room / publisher）で行う**のが素直。`game` は毎tick返し、
> room が「前回送信から N ms 経過していなければ捨てる」で足りる。
> **ただし足切り直後（§3.1 の3）は必ず送る**（順位が大きく変わった直後を間引くと表示がズレる）。

---

## 5. `room` / `envelopeOf` の更新

`envelopeOf`（`room.go:157`）に新メッセージの type タグを追加する。**ここに無い型は黙って捨てられる**
（`return proto.Envelope{}, false`）ので、追加漏れは「なぜか届かない」になる。

```
proto.RankingSnapshot      → proto.TypeRankingSnapshot
proto.RankingDelta         → proto.TypeRankingDelta
proto.StoreEliminatedBatch → proto.TypeStoreEliminatedBatch
```

> h20-A で `internal/proto/messages.go` への再輸出が済んでいる前提。

---

## 6. テスト（バグを注入して落ちることを確認する方式）

- `RankingSnapshot` が全99店を含み、`Rank` が生存店＝現在順位／脱落店＝確定順位になっている。
- `RankingDelta` が `Rank` を持たない（型に無いことと、変化した店のみが入ること）。
- **足切り時の Outbound の順序**が §3.1 のとおり。**順序を入れ替える変異でテストが落ちること**を確認。
- 120秒の Outbound が §3.2 のとおりで、`RankingSnapshot` が `MatchEnd` より**前**にある。
- `StoreEliminatedBatch` が1メッセージにまとまっている（24店脱落で Envelope が1つ）。
- `EvaluationUpdate` が 2〜4Hz に間引かれ、**足切り直後は間引かれない**。
- `envelopeOf` が新3種を変換できる（未知型として捨てられない）。
- 99接続 × 足切りで**送信キューが溢れない**（`sendBuffer=64` に対する余裕）。InMemory Connection で検証。

---

## 7. 完了条件

- [ ] `StoreListUpdate` の定期配信が停止し、`RankingSnapshot` に置き換わっている
- [ ] `MatchStart.stores[]` は継続（表示名と初期状態の供給源・`Score` を含む）
- [ ] `RankingSnapshot` が 1Hz で全店を配る。`RankingDelta` は**契約として実装済みだが既定OFF**
- [ ] `StoreEliminatedBatch` で足切りの脱落が1メッセージにまとまる
- [ ] 足切り時・試合終了時の**配信順序が §3 のとおり固定**され、テストで守られている
- [ ] `EvaluationUpdate` が 2〜4Hz に間引かれ、足切り直後は必ず送られる
- [ ] `envelopeOf` に新3種が登録されている
- [ ] **`.golangci.yml` の SA1019 除外を外して `golangci-lint run` が緑**
      （＝廃止フィールドの参照がサーバーから消えた。plan-h20 §2.2 の解除条件）
- [ ] `go build` / `go vet` / `go test -race ./internal/game/...` が緑
