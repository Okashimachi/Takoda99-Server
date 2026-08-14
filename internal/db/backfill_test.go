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

	// 1. 全フィールドがゼロでなくなっていること（＝どのグループも取りこぼしていない）。
	v := reflect.ValueOf(gp)
	tp := v.Type()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Errorf("グループ %q が backfill されていない。"+
				"backfillDefaults が GameParameters の全フィールドを走査しているか確認すること",
				tp.Field(i).Name)
		}
	}

	// 2. その結果が Validate を通ること。これが通らないと本番で
	//    「内蔵デフォルトで起動」に落ちる（今回の事故そのもの）。
	if err := gp.Validate(); err != nil {
		t.Fatalf("空JSONを backfill した結果が Validate を通らない: %v", err)
	}

	// 3. 中身が既定値と一致すること（別の値で埋めていない）。
	if gp != def {
		t.Errorf("backfill 後が DefaultParameters と一致しない")
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
