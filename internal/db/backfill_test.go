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
