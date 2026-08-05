# Plan-22: Unity クライアントとの結合

> **目的**: 本番クライアント `Takoda99-Unity` とサーバーを結合し、Unity で実際に遊べる状態にする。
> **対応issue**: #39
> **優先度**: **高**。~~中。Unity が本番クライアントなら必須だが、Web で代替できるなら後回し可。~~
> **Unity は唯一のクライアントで、代替が無い**（WebFront 廃止）。ここが通らないと当日遊べない。
> **依存**: ~~Plan-13（Web結合でパターン確立）,~~ Plan-21（#38 WebGL疎通）
> **担当**: りーせ ＋ Unity 担当（合同）

---

## 0. 前提

> ⚠ **訂正（2026-08-05）**: 旧版は「先に Web で通し、Unity はそれを C# でミラーする」としていたが、
> `Takoda99-WebFront` は**廃止・凍結**され参照元が存在しない。**Unity は Client-Docs から直接組む**。
> ~~先に Web で通しておくこと。Plan-13 で `Takoda99-WebFront` が1試合を最後まで通せていれば、
> プロトコルの疎通パターン（接続・ディスパッチ・状態遷移）が確立している。~~

サーバーの実挙動を正とし、設計は `Takoda99-Client-Docs` に従う。
食い違いが見つかったらドキュメントではなくサーバーの実挙動を正として Client-Docs を直す。

| リポジトリ | 役割 |
|---|---|
| `Takoda99-Client-Docs` | クライアント設計（アーキテクチャ・状態管理・打鍵判定・画面遷移）。**設計の正典** |
| `Takoda99-Proto` | メッセージ契約の正典（Go/**C#** ミラーあり） |
| ~~`Takoda99-WebFront`~~ | ~~TS 実装。参照元~~ **廃止・凍結。存在しない** |

---

## 1. サーバー側でやること

**ほぼ無い。** サーバーはクライアント種別を区別しない設計になっている。

- [ ] `ALLOWED_ORIGINS` に Unity WebGL の配信元を追加（Plan-21 で対応済みのはず）
- [ ] Unity 用の特別なエンドポイント・メッセージを**作らない**

サーバー側で「Unity だから」という分岐を入れた時点で設計が壊れる。
Bot も人間も、すべて同じ `Connection` として扱う。

---

## 2. Unity 側の実装範囲

### 2.1 ディスパッチ層

`Takoda99-Client-Docs` の「メッセージディスパッチ層」に従う（参照元の TS 実装は存在しない）。封筒は共通:

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

`Takoda99-Client-Docs` のチェックリストがそのまま適用される。特に:

- ❌ 我慢ゲージが 0 になったらローカルで客を消す → `CustomerLeft` を待つ
- ❌ `OrderServed` 送信直後にローカルで客を消す → サーバーの確定を待つ
- ❌ 脱落したら接続を切る → 観戦のため維持し `MatchEnd` まで受ける
- ❌ 残り時間UIを作る → 制限時間は廃止済み

---

## 3. 結合テストの段取り

### 段階1: Unity 単独

Plan-15 の手順で `matching.minPlayers=1` にしてから、**本番と同じエンドポイント**へ:

```
wss://takoda99.mooo.com/ws
```

> **単独検証のやり方**: 専用の solo エンドポイントは**作らない**（Plan-15 で方式Cを採用）。
> 本番と同じ `wss://takoda99.mooo.com/ws` に対し、config で `matching.minPlayers=1` /
> `startCountdownMs=2000` にすると1接続で試合が始まる。**検証が終わったら必ず戻す**（Plan-16）。

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

### 段階3: Unity + Bot 混在

**これが本命の受入条件**。サーバーがクライアント種別を区別しないことの証明。

- [ ] Unity 実機と Bot（`minFill` 補完）が同じ試合に混在できる
- [ ] Unity から見て Bot の店が正しく盤面に出る
- [ ] 順位が Unity 側とサーバーの `Results()` で矛盾しない

> ~~段階3: Unity + Web 混在~~ — **WebFront 廃止により消滅**。
> クライアント実装は Unity ただ1つなので、「2実装が同じ試合に入れるか」は検証対象にならない。

---

## 4. 判定の公平性（重要）

> ⚠ **訂正（2026-08-05）**: 旧版は「Web(TS) と Unity(C#) で打鍵判定の実装が2つあるので揃える」
> としていたが、**WebFront 廃止で実装は Unity ただ1つ**になり、実装間のズレは起きない。
> ~~同じお題を Web と Unity で打ち、`missCount` / `elapsedMs` が概ね一致することを確認~~

ただし**判定の妥当性そのものは依然として要る**。サーバーは打鍵判定をしない（`AGENTS.md` §1.1）ので、
`missCount` / `elapsedMs` が壊れていても評価に素通りする。

- [ ] ローマ字の複数入力（`し` = `si`/`shi`、`ん` = `n`/`nn` 等）が仕様どおり受理される
- [ ] ローマ字テーブルは `Takoda99-Proto` の共有定義を単一ソースとして使っている
      （サーバーの `odai.Keystrokes` と食い違うと `missCount > keystrokeTotal` になり丸められる）
- [ ] 判定仕様は `Takoda99-Client-Docs/06_打鍵判定共通仕様.md` に従っている

---

## 5. 完了条件

- [ ] Unity クライアントで接続〜試合〜リザルトまで一通り遊べる（solo）
- [ ] Unity 複数台が同じ試合に参加できる（match）
- [ ] **Unity 実機と Bot が同時接続して同じ試合に参加でき、盤面が正しく見える**
- [ ] 打鍵判定が `Takoda99-Proto` のローマ字テーブルに従っている
- [ ] サーバー側にクライアント種別の分岐が入っていない
- [ ] `ALLOWED_ORIGINS` に Unity の本番配信元が入っている
- [ ] 12種の S2C すべてを Unity 側でハンドリングしている（未対応で落ちない）
