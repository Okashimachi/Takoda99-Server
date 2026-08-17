package db

import (
	"encoding/json"
	"reflect"
	"testing"

	"takoda99/internal/game"
)

// TestBackfillDefaults_CoversEveryGroup は「DB に何も入っていない JSON でも
// backfill 後に Validate を通る」ことを固定する。
//
// 🔴 **これは 2026-08-14（本戦10日前）に本番で実際に起きた事故の再発防止テスト。**
//
//	h23 で publish グループを足したとき backfillDefaults への追記を忘れた
//	→ 予選スキーマのままの本番DBには publish が無い
//	→ ゼロ値 `publish.evaluationIntervalMs = 0` が Validate に弾かれる
//	→ Load 全体が失敗し、config が丸ごと内蔵デフォルト起動になった
//	→ **config-front から何を変えても効かない状態**になっていた
//
// backfillDefaults をリフレクション化したので構造的には起きないはずだが、
// 誰かが列挙方式へ戻したら**このテストが落ちる**ようにしてある。
func TestBackfillDefaults_CoversEveryGroup(t *testing.T) {
	def := game.DefaultParameters()

	// 空の JSON（＝DBに何のキーも無い最悪ケース）から復元する。
	var gp game.GameParameters
	if err := json.Unmarshal([]byte(`{}`), &gp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	backfillDefaults(&gp, def)

	// 1. 中身が既定値と完全に一致すること（＝どのグループも取りこぼしていない／別の値で埋めていない）。
	//    `!=` で比較できること自体が「GameParameters は comparable」の検査にもなっている
	//    （AGENTS.md §1.3。map/slice を足すとここがコンパイルエラーになる）。
	if gp != def {
		t.Errorf("空JSONを backfill した結果が DefaultParameters と一致しない")
	}

	// 2. その結果が Validate を通ること。これが通らないと本番で
	//    「内蔵デフォルトで起動」に落ちる（今回の事故そのもの）。
	if err := gp.Validate(); err != nil {
		t.Fatalf("空JSONを backfill した結果が Validate を通らない: %v", err)
	}

	// 3. 番兵デフォルトで「全グループが実際に埋まる」ことを見る。
	//
	//    ⚠ かつてはここを「backfill 後に全グループが非ゼロ」で検査していたが、
	//    **既定値がすべてゼロのグループ**（plan-h35 の odai は levelSpread=0/levelOffset=0 が
	//    「現行と同じ挙動」なので既定が全ゼロ）が現れた瞬間に成立しなくなる。
	//    リーフを全部非ゼロにした番兵を既定として渡せば、検出力を保ったまま
	//    「既定がゼロのグループ」と共存できる。
	sentinel := def
	fillNonZero(reflect.ValueOf(&sentinel).Elem())

	var gp2 game.GameParameters
	if err := json.Unmarshal([]byte(`{}`), &gp2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	backfillDefaults(&gp2, sentinel)

	v := reflect.ValueOf(gp2)
	tp := v.Type()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Errorf("グループ %q が backfill されていない。"+
				"backfillDefaults が GameParameters の全フィールドを走査しているか確認すること",
				tp.Field(i).Name)
		}
	}
}

// fillNonZero は構造体の全リーフを非ゼロ値で埋める（番兵デフォルトの生成用）。
// 値そのものに意味は無く、「ゼロでない」ことだけが要る。
func fillNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				fillNonZero(v.Field(i))
			}
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			fillNonZero(v.Index(i))
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(0.5)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.String:
		v.SetString("x")
	default:
		// 想定外の型（map/slice/ポインタ）は GameParameters に存在しないはず。
		// 増えたらここを通らないまま IsZero 検査で落ちる。
	}
}

// TestBackfillNewFields_CullWarnMaxIds は「既存グループに後から足したキーが補完される」ことを固定する。
//
// 🔴 **これは 2026-08-16 の事故（heat.perElapsedSec）と同じ形の穴を塞ぐテスト。**
//
//	backfillDefaults の補完は**グループ単位**（グループ全体がゼロのときだけ既定値を入れる）
//	→ 本番DBには `cull` グループが既にあるので、新設の warnMaxIds は 0 のまま読まれる
//	→ 0 を「予告を1件も送らない」と解釈すると足切り予告が消える
//
// 本番DBを模した「cull.stages はあるが warnMaxIds が無い」JSON から検証する。
func TestBackfillNewFields_CullWarnMaxIds(t *testing.T) {
	def := game.DefaultParameters()

	// 本番DBの形（cull グループはあるが warnMaxIds キーが無い）。
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cull, ok := m["cull"].(map[string]any)
	if !ok {
		t.Fatal("cull グループが JSON に無い")
	}
	delete(cull, "warnMaxIds")
	stripped, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var gp game.GameParameters
	if err := json.Unmarshal(stripped, &gp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// まずグループ単位の補完では埋まらないことを確認する（前提が崩れたら気付けるように）。
	backfillDefaults(&gp, def)
	if gp.Cull.WarnMaxIds != 0 {
		t.Fatalf("前提が変わった: グループ単位の補完で warnMaxIds が埋まっている (%d)。"+
			"backfillDefaults がフィールド単位になったなら backfillNewFields は不要になる",
			gp.Cull.WarnMaxIds)
	}

	backfillNewFields(&gp, def)
	if gp.Cull.WarnMaxIds != game.DefaultCullWarnMaxIds {
		t.Fatalf("warnMaxIds が補完されていない: %d, want %d",
			gp.Cull.WarnMaxIds, game.DefaultCullWarnMaxIds)
	}
	if gp.Cull.Stages != def.Cull.Stages {
		t.Fatal("既存の足切りスケジュールが壊れた")
	}
	if err := gp.Validate(); err != nil {
		t.Fatalf("Validate を通らない: %v", err)
	}
}

// TestBackfillNewFields_KeepsOperatorValue は「運営が入れた値を既定へ巻き戻さない」ことを固定する。
func TestBackfillNewFields_KeepsOperatorValue(t *testing.T) {
	def := game.DefaultParameters()
	gp := game.DefaultParameters()
	gp.Cull.WarnMaxIds = 8 // 当日、帯域のために下げたつもりの値

	backfillNewFields(&gp, def)

	if gp.Cull.WarnMaxIds != 8 {
		t.Fatalf("運営の値が既定で上書きされた: %d, want 8", gp.Cull.WarnMaxIds)
	}
}

// TestBackfillDefaults_KeepsExistingValues は「DBにある値を上書きしない」ことを固定する。
//
// backfill はあくまで欠けたグループを埋めるだけで、運用中の調整値を既定へ戻してはいけない。
func TestBackfillDefaults_KeepsExistingValues(t *testing.T) {
	def := game.DefaultParameters()

	gp := game.DefaultParameters()
	gp.Score.WeightMiss = 18 // 運営が config-front で変えた値のつもり

	backfillDefaults(&gp, def)

	if gp.Score.WeightMiss != 18 {
		t.Fatalf("DBの値が既定で上書きされた: WeightMiss=%d, want 18", gp.Score.WeightMiss)
	}
}

// TestBackfillDefaults_FillsOnlyMissingGroup は「欠けたグループだけ埋める」ことを確認する。
//
// 予選スキーマの本番DB（publish が無い）を模したケース。今回の事故と同じ形。
func TestBackfillDefaults_FillsOnlyMissingGroup(t *testing.T) {
	def := game.DefaultParameters()

	gp := game.DefaultParameters()
	gp.Score.WeightMiss = 18          // 既存の調整値
	gp.Publish = game.PublishParams{} // DBに publish が無い状態

	backfillDefaults(&gp, def)

	if gp.Publish != def.Publish {
		t.Errorf("欠けた publish が埋まっていない: %+v", gp.Publish)
	}
	if gp.Score.WeightMiss != 18 {
		t.Errorf("既存の調整値が壊れた: WeightMiss=%d, want 18", gp.Score.WeightMiss)
	}
	if err := gp.Validate(); err != nil {
		t.Fatalf("Validate を通らない: %v", err)
	}
}

// TestBackfillNewFields_BotTiers は「本番DB相当（bot グループが**旧スキーマ**で入っている）JSON を
// 読んでも壊れない」ことを固定する（plan-h31 §4.1）。
//
// 🔴 **これが h31 で最も危険だった箇所。** 経路は #124 とまったく同じ:
//
//	本番DBの bot = {baseAccuracy, baseElapsedMs, accuracyJitter, elapsedJitterMs}
//	  → グループは非ゼロなので backfillDefaults は触らない
//	  → 新設の tiers は [{0,0,0,0} ×3] のまま
//	  → ここで Validate が弾くと Load 失敗 → **score/cull/heat を含む全設定が内蔵デフォルトへ転落**
//
// 対処は「Validate で弾かず、ゼロを既定へ読み替える」。読み替えは game.BotParams.EffectiveTiers に
// 一本化し、db 側（この関数）でも同じものを当てて二重化している。
func TestBackfillNewFields_BotTiersFromLegacySchema(t *testing.T) {
	def := game.DefaultParameters()

	// 本番DB（2026-08-17 時点）を模した JSON。bot は旧スキーマのまま、
	// 他のグループは本番の実値（heat.perElapsedSec=0.12 等）が入っている。
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m["bot"] = map[string]any{
		"baseAccuracy":    0.85,
		"baseElapsedMs":   6000, // 本番の実値。新スキーマには対応するキーが無い
		"accuracyJitter":  0.1,
		"elapsedJitterMs": 500,
	}
	legacy, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var gp game.GameParameters
	if err := json.Unmarshal(legacy, &gp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// 前提1: 旧キーは無視され、新フィールドはゼロのまま読まれる。
	if gp.Bot.Tiers != ([game.BotTierCount]game.BotTier{}) {
		t.Fatalf("前提が変わった: tiers がゼロでない %+v", gp.Bot.Tiers)
	}
	// 前提2: グループ単位の補完では埋まらない（elapsedJitterMs があるのでグループは非ゼロ）。
	backfillDefaults(&gp, def)
	if gp.Bot.Tiers != ([game.BotTierCount]game.BotTier{}) {
		t.Fatalf("前提が変わった: グループ単位の補完で tiers が埋まっている %+v。"+
			"backfillDefaults がフィールド単位になったならこの関数は不要になる", gp.Bot.Tiers)
	}

	// 🔴 ここが本題。**Validate は落ちてはいけない**（落ちると全設定が既定へ巻き戻る）。
	if err := gp.Validate(); err != nil {
		t.Fatalf("旧スキーマの bot で Validate が落ちた（#124 と同じ事故になる）: %v", err)
	}

	backfillNewFields(&gp, def)

	if gp.Bot.Tiers != game.DefaultBotTiers() {
		t.Fatalf("tiers が既定へ補完されていない: %+v", gp.Bot.Tiers)
	}
	if gp.Bot.IndividualSpread != game.DefaultBotIndividualSpread {
		t.Fatalf("individualSpread が補完されていない: %v", gp.Bot.IndividualSpread)
	}
	// DB にある値（旧スキーマから引き継げるもの）は保持する。
	if gp.Bot.ElapsedJitterMs != 500 {
		t.Fatalf("DB の elapsedJitterMs が失われた: %d", gp.Bot.ElapsedJitterMs)
	}
	// 🔴 他のグループが巻き戻っていないこと（#124 で実際に起きた被害はこちら）。
	if gp.Score != def.Score || gp.Cull != def.Cull || gp.Heat != def.Heat {
		t.Fatal("bot 以外のグループが変化した")
	}
	if err := gp.Validate(); err != nil {
		t.Fatalf("補完後に Validate を通らない: %v", err)
	}
}

// tiers を運営が config-front から保存した後は、その値を既定へ巻き戻さないこと。
func TestBackfillNewFields_KeepsOperatorBotTiers(t *testing.T) {
	def := game.DefaultParameters()
	gp := game.DefaultParameters()
	gp.Bot.Tiers[game.BotTierStrong].MsPerKey = 120 // 当日「Bot を強くした」つもりの値
	gp.Bot.IndividualSpread = 0.35

	backfillNewFields(&gp, def)

	if gp.Bot.Tiers[game.BotTierStrong].MsPerKey != 120 {
		t.Fatalf("運営の tier 値が既定で上書きされた: %d", gp.Bot.Tiers[game.BotTierStrong].MsPerKey)
	}
	if gp.Bot.IndividualSpread != 0.35 {
		t.Fatalf("運営の individualSpread が上書きされた: %v", gp.Bot.IndividualSpread)
	}
}
