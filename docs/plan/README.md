# 実装計画（plan）索引

サーバー実装の作業単位ごとの仕様書。**着手前に該当 plan を読むこと**。
plan 同士で型・関数シグネチャが噛み合うよう調整済みなので、勝手に別案へ差し替えない。

- **内容の正典は plan**。タスクの**進捗・担当**は GitHub issue が正典（`gh issue list`）。
  食い違ったら plan が正しい（issue 本文は起票時点のスナップショット）。詳細は `AGENTS.md` §7
- 設計の全体像は `docs/architecture.md`
- 責務・禁止事項は `AGENTS.md`

---

## plan-01〜12: コア実装（済／設計）

たこ焼き版サーバーを一から立ち上げるための計画。**大半は実装済み**。

| plan | 内容 | issue |
|---|---|---|
| [01](plan-01_基盤移行と旧実装消去.md) | 基盤移行（Textro99-Server → Takoda99-Server）・旧実装消去 | #42 |
| [02](plan-02_我慢離脱信用.md) | 我慢ゲージ・離脱・信用・自滅脱落（`stepPatience`） | #6, #29 |
| [03](plan-03_客分配と評価正規化.md) | 客分配・評価正規化（`stepDistribute` / `stepNormalize`） | #7 |
| [04](plan-04_フェーズ火力下位淘汰.md) | フェーズ・火力・下位淘汰（`stepPhase` / `stepHeat` / `stepStorm`） | #8 |
| [05](plan-05_脱落順位リザルト.md) | 終了条件・順位確定・MatchEnd（`checkFinish`） | #9 |
| [06](plan-06_config基盤とDB.md) | config 基盤・DB（設計） | #43 |
| [07](plan-07_お題単語データ.md) | お題単語データ（関西弁語彙・config 管理） | #44 |
| [08](plan-08_試合結果永続化.md) | 試合結果永続化（match / store_result） | #14 |
| [09](plan-09_スパイク対策.md) | マッチングスパイク対策・接続リミッター | #45 |
| [10](plan-10_デプロイ戦略.md) | デプロイ戦略（**Render 時代の設計記録**） | #46 |
| [11](plan-11_負荷テスト.md) | 負荷テスト（**Render 時代の設計**） | #47 |
| [12](plan-12_observability.md) | Observability（**Render 時代の設計**） | #48 |

> ⚠ plan-10/11/12 は Render 前提で書かれている。**GCP 移行後の実施版は plan-17/18/19**。
> デプロイ手順の正典は `docs/deploy.md`。

---

## plan-13〜26: 残作業（優先度順）

**番号が小さいほど優先度が高い。** 上から潰す。

### Tier A — 当日「遊べて終わる」ための必須

| plan | 内容 | issue |
|---|---|---|
| [13](plan-13_ヘッドレスsimとWeb結合.md) | ~~ヘッドレスsim＋Web結合~~ **ヘッドレスsim（`cmd/matchsim`）**。§2 Web結合は無効 | #12 ✅ |
| [14](plan-14_決着保証テスト.md) | **決着保証テスト**。試合が必ず終わることを固定する | #34 |
| [15](plan-15_soloモード起動構成.md) | **単独検証の起動構成**（config で `minPlayers=1`）。クライアント開発のブロックを外す | #36 |
| [16](plan-16_本番前のmatchモード復帰.md) | 本番前に検証用設定を戻す（`minPlayers`） | #37 |

### Tier B — 当日運用

| plan | 内容 | issue |
|---|---|---|
| [17](plan-17_observability実装.md) | Observability の実装（GCP版） | #48 |
| [18](plan-18_負荷テストの再現可能化.md) | 負荷テストの再現可能化（`cmd/loadtest`） | #47 |
| [19](plan-19_当日オペレーション手順.md) | 当日オペレーション手順（`docs/runbook.md`） | #46 |
| [20](plan-20_configfront残作業.md) | config-front / DB の残作業（スキーマ一致） | #43 |

### Tier C — クライアント結合

| plan | 内容 | issue |
|---|---|---|
| [21](plan-21_WebGL疎通確認.md) | WebGL ⇄ サーバー 疎通確認 | #38 |
| [22](plan-22_Unity結合.md) | Unity クライアントとの結合 | #39 |

### Tier D — イベント後

| plan | 内容 | issue |
|---|---|---|
| [23](plan-23_configapiの穴.md) | configapi の穴3件（運営UI要求） | #68 |
| [24](plan-24_BOT学習ループ.md) | BOT学習ループ（統計→プロファイル→反映） | #15 |
| [25](plan-25_protoフルスキーマ共有.md) | GameParameters を proto 共有型へ（**要承認**） | #19 |
| [26](plan-26_Redis化スケールアウト.md) | Redis 化・スケールアウト（**着手判定から**） | #16 |

---

## 依存関係

```
plan-13 (ヘッドレスsim) ┬─→ plan-14 (決着保証)
                        └─→ plan-18 (負荷テスト)
plan-15 (minPlayers=1) ┬─→ plan-16 (設定を戻す)
                       └─→ plan-21/22 (クライアント結合)
plan-21 (WebGL疎通) ───→ plan-22 (Unity結合)     ※plan-21 は単独で着手可
plan-17 (ログ) ────────┬─→ plan-18 (計測)
                       └─→ plan-19 (当日運用)
```

> ⚠ **`Takoda99-WebFront` は廃止・凍結**。Web先行戦略をやめて **Unity 単独**開発に変わったため、
> plan-13 §2（Web結合）は無効で、クライアント結合は plan-21 / plan-22 が唯一の経路。
> plan-15 / 19 / 21 / 22 に残る「WebFront」の記述は読み替えること（各ファイル冒頭に注記あり）。

**plan-20 / 23 / 24 / 25 / 26 は他に依存しない**（いつでも着手可）。

---

## 着手判定が要る plan

以下は「やるかどうか」から判断する。無条件で着手しない。

| plan | 判定 |
|---|---|
| [16](plan-16_本番前のmatchモード復帰.md) | plan-15 で `minPlayers` を下げたなら**必ず実施**（戻し忘れ＝当日1人で試合開始） |
| [25](plan-25_protoフルスキーマ共有.md) | パラメータが安定したか。proto 変更は**要承認** |
| [26](plan-26_Redis化スケールアウト.md) | 1台で捌けなくなったか（実測ベース）。垂直スケールで足りないか |
