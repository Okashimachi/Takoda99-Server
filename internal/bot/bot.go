// Package bot は サーバー内で自動入力する仮想クライアント（CPU）。
//
// 人間と同じ transport.Connection に乗り、session からは区別されない。
// 強さは typist.Ability（1打鍵あたりのms・ミス率・難度追従）で表す。
// **その値をどう決めるか（tier 抽選・個体差）は app 層の責務**で、bot は受け取った個体を演じるだけ
// （game/bot の切り分けと同じ理由で、抽選ロジックを合成ルート側に置く・plan-h31 §3）。
package bot

import (
	"context"
	"encoding/json"
	"math/rand"
	"time"

	"takoda99/internal/odai"
	"takoda99/internal/proto"
	"takoda99/internal/transport"
	"takoda99/internal/typist"
)

// idleInterval は行列が空のときの再確認間隔（安全網）。
//
// 通常は CustomerArrived を受けた瞬間に打ち始めるので、このタイマーが実際に効くのは
// 「取りこぼした」ときだけ。短くすると 99体ぶんの空回りが増えるので長めに取る。
const idleInterval = time.Second

// pendingOrder は来店したがまだ打ち始めていない客。
type pendingOrder struct {
	customerId proto.CustomerId
	keystrokes int
}

// activeOrder は現在打鍵中の客。結果（所要ms・ミス数）は**打ち始めた時点で確定**させ、
// 打ち切る時刻に報告する。sim のダミー店（internal/sim/dummy.go）と同じ流儀。
type activeOrder struct {
	customerId proto.CustomerId
	out        typist.Outcome
}

// Bot は1接続を自動操作する。
type Bot struct {
	conn    transport.Connection
	ability typist.Ability
	rng     *rand.Rand

	pending []pendingOrder
	current *activeOrder

	// heatLevel は DifficultyUpdate で届く全店共通の難度。難度追従（HeatPenalty）に使う。
	heatLevel int
}

// New は Bot を作る。conn は自分の（クライアント側）接続。
func New(conn transport.Connection, ability typist.Ability, rng *rand.Rand) *Bot {
	return &Bot{conn: conn, ability: ability, rng: rng}
}

// Run は Bot を駆動する。
//
// 🔴 **固定周期の ticker では回さない**（plan-h31 §2.3）。1注文にかかる時間は
// お題の打鍵数（＝heat）と個体の実力で決まるので、**打ち切るたびに次の期限を計算し直す**。
// 固定周期だと heat が上がっても Bot の提供ペースが変わらず、終盤ほど Bot が
// 相対的に強くなる（人間だけが長い語で遅くなる）。
func (b *Bot) Run(ctx context.Context) {
	defer func() { _ = b.conn.Close() }()
	timer := time.NewTimer(idleInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case env, ok := <-b.conn.Receive():
			if !ok {
				return
			}
			if b.onMessage(env) {
				return
			}
			// 手が空いているなら、来店した瞬間に打ち始める。
			if b.current == nil {
				resetTimer(timer, b.nextDelay())
			}
		case <-timer.C:
			b.finishCurrent()
			resetTimer(timer, b.nextDelay())
		}
	}
}

// resetTimer はタイマーを d 後へ張り直す。
// Go 1.23 以降は Stop 後に古い発火がチャネルへ残らないので、ドレインは不要。
func resetTimer(t *time.Timer, d time.Duration) {
	t.Stop()
	t.Reset(d)
}

// nextDelay は次の客に取り掛かり、打ち切るまでの待ち時間を返す。行列が空なら idleInterval。
func (b *Bot) nextDelay() time.Duration {
	if d, ok := b.startNext(); ok {
		return d
	}
	return idleInterval
}

func (b *Bot) onMessage(env proto.Envelope) bool {
	switch env.Type {
	case proto.TypeMatchStart:
		// MatchStart には客が含まれない（客は stepDistribute で来店する）
	case proto.TypeCustomerArrived:
		var m proto.CustomerView
		if json.Unmarshal(env.Payload, &m) == nil {
			// 🔴 打鍵数は **CustomerArrived.words[] から Bot 自身が数える**。
			// サーバーが発行した語がそのまま届くので、proto を変えずに済む（plan-h31 §2.2）。
			b.pending = append(b.pending, pendingOrder{
				customerId: m.CustomerId,
				keystrokes: countKeystrokes(m.Words),
			})
		}
	case proto.TypeDifficultyUpdate:
		var m proto.DifficultyUpdate
		if json.Unmarshal(env.Payload, &m) == nil {
			b.heatLevel = m.HeatLevel
		}
	// 客の離脱（CustomerLeft）は本戦で廃止された。客は逃げないので、
	// 一度受け取ったお題は必ず打ち切られる（plan-h21）。
	case proto.TypeMatchEnd, proto.TypePersonalResult:
		return true
	}
	return false
}

// startNext は行列の先頭に取り掛かる。結果（所要ms・ミス数）はここで確定し、
// 打ち切りは finishCurrent が報告する。戻り値は打ち切るまでの時間。
func (b *Bot) startNext() (time.Duration, bool) {
	if b.current != nil || len(b.pending) == 0 {
		return 0, false
	}
	p := b.pending[0]
	b.pending = b.pending[1:]
	out := typist.Serve(b.ability, p.keystrokes, b.heatLevel, b.rng)
	b.current = &activeOrder{customerId: p.customerId, out: out}
	return time.Duration(out.ElapsedMs) * time.Millisecond, true
}

// finishCurrent は打鍵中の客を打ち切って OrderServed を送る。
func (b *Bot) finishCurrent() {
	if b.current == nil {
		return
	}
	cur := b.current
	b.current = nil
	b.send(proto.TypeOrderServed, proto.OrderServed{
		CustomerId: cur.customerId,
		ElapsedMs:  cur.out.ElapsedMs,
		MissCount:  cur.out.MissCount,
	})
}

// countKeystrokes は CustomerView.Words のローマ字打鍵数を求める。
//
// CustomerView は表示テキストしか持たず reading が無い（proto にも載っていない）。
// ただし現行のお題辞書は Text がそのまま読み（ひらがな）なので、公開済みの
// odai.Keystrokes をそのまま当てれば実際の打鍵数（サーバーが記録する値）と一致する。
// 将来 Text に漢字が入ったら概算に劣化するが、reading を proto に載せるのは
// 契約変更＝要承認なのでここではやらない（internal/sim/dummy.go と同じ判断）。
func countKeystrokes(words []string) int {
	n := 0
	for _, w := range words {
		n += odai.Keystrokes(w)
	}
	return n
}

func (b *Bot) send(typ string, msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_ = b.conn.Send(proto.Envelope{Type: typ, Payload: data})
}
