package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"takoda99/internal/admin"
	"takoda99/internal/transport"
)

// /admin/ws はトークン未設定なら 503、誤トークン/未指定なら 401 で弾く（WS昇格前）。
// 正しいトークンだけが upgrade へ進む（＝401/503 にならない）。
func TestAdminWSHandler_TokenGate(t *testing.T) {
	hub := admin.NewHub()
	wsAccept := transport.AcceptOptions{AllowAll: true}

	cases := []struct {
		name    string
		token   string // サーバー側 CONFIG_ADMIN_TOKEN
		query   string // ?token=
		want    int
		upgrade bool // true=昇格まで進む（このテストの httptest では失敗するが 401/503 にはならない）
	}{
		{"トークン未設定は503", "", "whatever", http.StatusServiceUnavailable, false},
		{"トークン未指定は401", "s3cret", "", http.StatusUnauthorized, false},
		{"誤トークンは401", "s3cret", "wrong", http.StatusUnauthorized, false},
		{"正トークンは通過", "s3cret", "s3cret", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := adminWSHandler(hub, tc.token, wsAccept)
			req := httptest.NewRequest(http.MethodGet, "/admin/ws?token="+tc.query, nil)
			rec := httptest.NewRecorder()
			h(rec, req)

			if tc.upgrade {
				// 正トークンは token ゲートを通過し WS 昇格へ進む。httptest.ResponseRecorder は
				// Hijack 非対応なので昇格自体は失敗するが、401/503（ゲート拒否）にはならない。
				if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusServiceUnavailable {
					t.Fatalf("正トークンがゲートで拒否された: code=%d", rec.Code)
				}
				return
			}
			if rec.Code != tc.want {
				t.Fatalf("code=%d, want %d (body=%q)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
