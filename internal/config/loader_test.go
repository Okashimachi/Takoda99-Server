package config

import (
	"context"
	"errors"
	"testing"

	"textro99/internal/game"
)

// DefaultLoader は内蔵デフォルトをそのまま返す。
func TestDefaultLoader_ReturnsDefaults(t *testing.T) {
	got, err := DefaultLoader{}.Load(context.Background())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != game.DefaultParameters() {
		t.Fatalf("got %+v, want DefaultParameters()", got)
	}
}

// スタックの契約：失敗時も第1返り値は有効な GameParameters（起動を止めない）。
// 後輩が Load を実装したら、このテストは「正常系: 有効なJSONをパースして返す」等へ差し替える。
func TestRemoteLoader_StubFallsBackToDefaults(t *testing.T) {
	got, err := NewRemoteLoader("http://example.invalid").Load(context.Background())
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("err = %v, want ErrNotImplemented（実装後はこのテストを差し替える）", err)
	}
	if got != game.DefaultParameters() {
		t.Fatalf("失敗時の第1返り値 = %+v, want DefaultParameters()", got)
	}
}
