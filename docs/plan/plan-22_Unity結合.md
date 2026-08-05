# Plan-22: Unity クライアントとの結合

> **目的**: 本番クライアント `Takoda99-Unity` とサーバーを結合し、Unity で実際に遊べる状態にする。
> **対応issue**: #39
> **優先度**: 中。Unity が本番クライアントなら必須だが、Web で代替できるなら後回し可。
> **依存**: Plan-13（Web結合でパターン確立）, Plan-21（#38 WebGL疎通）
> **担当**: りーせ ＋ Unity 担当（合同）

---

## 0. 前提

**先に Web で通しておくこと**。Plan-13 で `Takoda99-WebFront` が1試合を最後まで通せていれば、
プロトコルの疎通パターン（接続・ディスパッチ・状態遷移）が確立している。
Unity はそれを **C# でミラーする**のが最短で、ゼロから設計しない。

| リポジトリ | 役割 |
|---|---|
| `Takoda99-WebFront` | TS 実装。**参照元** |
| `Takoda99-Client-Docs` | クライアント設計（アーキテクチャ・状態管理・打鍵判定・画面遷移）。**設計の正典** |
| `docs/client-integration.md` | ワイヤ仕様（サーバー視点） |
| `Takoda99-Proto` | メッセージ契約（Go/TS/**C#** ミラーあり） |

---

## 1. サーバー側でやること

**ほぼ無い。** サーバーはクライアント種別を区別しない設計になっている。

- [ ] `ALLOWED_ORIGINS` に Unity WebGL の配信元を追加（Plan-21 で対応済みのはず）
- [ ] Unity 用の特別なエンドポイント・メッセージを**作らない**

サーバー側で「Unity だから」という分岐を入れた時点で設計が壊れる。
Bot も人間も Web も Unity も、すべて同じ `Connection` として扱う。

---

## 2. Unity 側の実装範囲

### 2.1 ディスパッチ層

WebFront の実装をミラーする。封筒は共通:

```json
{ "type": "<MessageName>", "payload": { ... } }
```

C# 側:

```csharp
[Serializable] public class Envelope {
    public string type;
    public string payload;   // 生JSONとして受け、type で分岐してから個別型へ
}
```

> Unity の `JsonUtility` は `object` / 辞書 / null許容が弱い。**Newtonsoft.Json（com.unity.nuget.newtonsoft-json）**
> を使うのが無難。`Takoda99-Proto` の C# ミラーがどちらを前提にしているか確認して合わせる。

扱う S2C（12種）:

```
MatchmakingStatus / MatchStart / CustomerArrived / CustomerLeft /
CreditUpdate / EvaluationUpdate / PhaseChange / DifficultyUpdate /
StoreListUpdate / ForcedEliminationWarning / StoreEliminated / MatchEnd
```

送る C2S（実質1種）:

```
OrderServed   （＋ MatchmakingJoin / MatchmakingLeave）
```

### 2.2 打鍵判定

**クライアントの責務**。サーバーは判定しない。

- ローマ字テーブルは `Takoda99-Proto` の共有定義を使う（Web と判定がズレると不公平になる）
- 判定仕様は `Takoda99-Client-Docs/06_打鍵判定共通仕様.md`
- 注文N個を打ち切ったら `OrderServed` を1回送る（**単語ごとに送らない**）

### 2.3 やってはいけないこと

`docs/client-integration.md` §6 のチェックリストがそのまま適用される。特に:

- ❌ 我慢ゲージが 0 になったらローカルで客を消す → `CustomerLeft` を待つ
- ❌ `OrderServed` 送信直後にローカルで客を消す → サーバーの確定を待つ
- ❌ 脱落したら接続を切る → 観戦のため維持し `MatchEnd` まで受ける
- ❌ 残り時間UIを作る → 制限時間は廃止済み

---

## 3. 結合テストの段取り

### 段階1: Unity 単独（solo）

```
wss://takoda99-solo.mooo.com/ws
```

- [ ] 接続 → `MatchStart` → `CustomerArrived` → 打つ → `OrderServed` → `EvaluationUpdate`
- [ ] 放置 → `CustomerLeft` → `CreditUpdate` でライフが減る
- [ ] ライフ0 → `StoreEliminated`(SelfCollapse) → リザルト画面
- [ ] `MatchEnd` で `finalRank` が表示される

### 段階2: Unity 複数台（match）

`minPlayers` を人数に合わせて下げてから:

```
wss://takoda99.mooo.com/ws
```

- [ ] 複数台が同じ試合に入る
- [ ] `StoreListUpdate` で他店の状況が見える
- [ ] 他店の `StoreEliminated` が盤面に反映される

### 段階3: Unity + Web 混在

**これが本命の受入条件**。サーバーがクライアント種別を区別しないことの証明。

- [ ] Unity と Web が同じ試合に参加できる
- [ ] 双方から見て相手が正しく盤面に出る
- [ ] 順位が両者で矛盾しない

---

## 4. 判定の公平性（重要）

Web(TS) と Unity(C#) で**打鍵判定の実装が2つある**。同じ入力に対して判定が違うと、
評価スコア（精度）が変わり**不公平になる**。

- [ ] 同じお題を Web と Unity で打ち、`missCount` / `elapsedMs` が概ね一致することを確認
- [ ] ローマ字の複数入力（`し` = `si`/`shi`、`ん` = `n`/`nn` 等）の扱いが揃っている

ズレる場合は `Takoda99-Proto` のローマ字テーブルを単一ソースとして両者を寄せる。
**サーバーは判定しないので、ここはクライアント間で揃えるしかない。**

---

## 5. 完了条件

- [ ] Unity クライアントで接続〜試合〜リザルトまで一通り遊べる（solo）
- [ ] Unity 複数台が同じ試合に参加できる（match）
- [ ] **Unity と Web が同時接続して同じ試合に参加でき、双方から正しく見える**
- [ ] 打鍵判定が Web と Unity で概ね一致する（精度が不公平にならない）
- [ ] サーバー側にクライアント種別の分岐が入っていない
- [ ] `ALLOWED_ORIGINS` に Unity の本番配信元が入っている
- [ ] 12種の S2C すべてを Unity 側でハンドリングしている（未対応で落ちない）
