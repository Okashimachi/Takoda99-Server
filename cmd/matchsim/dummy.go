package main

import (
	"fmt"
	"math/rand"

	"takoda99/internal/game"
	"takoda99/internal/odai"
	"takoda99/internal/proto"
)

// dummy.go はシミュレータ内の仮想プレイヤー（ダミー店）。
// 実力を (1打鍵あたりのms, 打鍵ごとのミス率) の2値で表し、人間の分布を模す。

// profile は99店の実力分布のプリセット。
type profile string

const (
	profileUniform profile = "uniform" // 全員同じ実力（膠着の最悪ケース）
	profileNormal  profile = "normal"  // 正規分布（現実に近い）
	profileBipolar profile = "bipolar" // 二極化（上手い/下手がはっきり）
	profileWide    profile = "wide"    // 実力差が非常に大きい
)

func parseProfile(s string) (profile, error) {
	switch p := profile(s); p {
	case profileUniform, profileNormal, profileBipolar, profileWide:
		return p, nil
	default:
		return "", fmt.Errorf("未知の profile %q（uniform|normal|bipolar|wide）", s)
	}
}

// queuedOrder は「来店したが、まだ打ち終わっていない客」1人ぶん。
type queuedOrder struct {
	customerId proto.CustomerId
	keystrokes int
}

// pendingOrder は現在打鍵中の客の進行。
type pendingOrder struct {
	customerId proto.CustomerId
	keystrokes int
	missCount  int
	// totalMs は打ち切るまでに実際に掛かる時間（ミスの打ち直しを含む）。
	totalMs     int
	remainingMs int
}

// dummyStore はシミュレータ内の仮想プレイヤー。
type dummyStore struct {
	id       game.PlayerId
	msPerKey int     // 1打鍵あたりのms（小さいほど速い）
	missRate float64 // 打鍵ごとのミス確率

	alive bool

	// queue は session 側の storeQueues[id] を写した行列（先頭 = 対応中）。
	// session は「行列先頭でない客」への OrderServed を弾くので、
	// 来店(CustomerArrived)・離脱(CustomerLeft)・提供の3つで必ず同期を保つ。
	queue   []queuedOrder
	current *pendingOrder
}

// arrive は来店を受け付けて行列末尾に積む。
func (d *dummyStore) arrive(v proto.CustomerView) {
	d.queue = append(d.queue, queuedOrder{
		customerId: v.CustomerId,
		keystrokes: countKeystrokes(v.Words),
	})
}

// leave は我慢切れで帰った客を行列から外す。
//
// これを取りこぼすと、打鍵中の客が居なくなったのに current が残り続け、
// その店は以後1人も捌けなくなる（＝ sim が実態と別物になる）。
func (d *dummyStore) leave(cid proto.CustomerId) {
	for i, q := range d.queue {
		if q.customerId == cid {
			d.queue = append(d.queue[:i], d.queue[i+1:]...)
			break
		}
	}
	if d.current != nil && d.current.customerId == cid {
		d.current = nil
	}
}

// step は dtMs ぶん打鍵を進め、打ち終わったら提供報告を返す。
func (d *dummyStore) step(dtMs int, rng *rand.Rand) (proto.OrderServed, bool) {
	if !d.alive {
		return proto.OrderServed{}, false
	}
	if d.current == nil {
		if len(d.queue) == 0 {
			return proto.OrderServed{}, false
		}
		d.current = d.begin(d.queue[0], rng)
	}

	d.current.remainingMs -= dtMs
	if d.current.remainingMs > 0 {
		return proto.OrderServed{}, false
	}

	o := proto.OrderServed{
		CustomerId: d.current.customerId,
		ElapsedMs:  d.current.totalMs,
		MissCount:  d.current.missCount,
	}
	d.queue = d.queue[1:]
	d.current = nil
	return o, true
}

// begin は行列先頭の客に取り掛かり、ミス回数と所要時間を先に確定させる。
func (d *dummyStore) begin(q queuedOrder, rng *rand.Rand) *pendingOrder {
	keys := q.keystrokes
	if keys <= 0 {
		keys = 1
	}
	miss := 0
	for i := 0; i < keys; i++ {
		if rng.Float64() < d.missRate {
			miss++
		}
	}
	// ミス1回につき1打鍵ぶん打ち直す。報告する elapsedMs は打ち直しを含む実測時間にする
	// （実クライアントが送るのは「実際に掛かった時間」なので、そこを合わせる）。
	total := (keys + miss) * d.msPerKey
	return &pendingOrder{
		customerId:  q.customerId,
		keystrokes:  keys,
		missCount:   miss,
		totalMs:     total,
		remainingMs: total,
	}
}

// countKeystrokes は CustomerView.Words のローマ字打鍵数を求める。
//
// CustomerView は表示テキストしか持たず reading が無い（proto にも載っていない）。
// ただし現行のお題辞書は Text がそのまま読み（ひらがな）なので、公開済みの
// odai.Keystrokes をそのまま当てれば実際の打鍵数と一致する。将来 Text に漢字が
// 入ったら概算に劣化するが、reading を proto に載せるのは契約変更＝要承認なのでここではやらない。
//
// ルーン数で概算すると打鍵数が約半分になり、speed 評価が SpeedCap(2.0) に張り付いて
// 全店が同点になる。それではバランス検証にならないので、この精度が要る。
func countKeystrokes(words []string) int {
	n := 0
	for _, w := range words {
		n += odai.Keystrokes(w)
	}
	return n
}

// buildStores は n 店ぶんの実力をプリセットに従って割り当てる。
func buildStores(n int, p profile, rng *rand.Rand) []*dummyStore {
	stores := make([]*dummyStore, n)
	for i := range stores {
		var msPerKey int
		var missRate float64
		switch p {
		case profileUniform:
			msPerKey, missRate = 200, 0.05
		case profileNormal:
			msPerKey = 200 + int(rng.NormFloat64()*50)
			missRate = clamp(0.05+rng.NormFloat64()*0.02, 0, 0.5)
		case profileBipolar:
			if i%2 == 0 {
				msPerKey, missRate = 130, 0.02
			} else {
				msPerKey, missRate = 300, 0.10
			}
		case profileWide:
			msPerKey = 100 + rng.Intn(400)
			missRate = rng.Float64() * 0.2
		}
		if msPerKey < 50 {
			msPerKey = 50
		}
		stores[i] = &dummyStore{
			id:       game.PlayerId(fmt.Sprintf("s-%d", i+1)),
			msPerKey: msPerKey,
			missRate: missRate,
			alive:    true,
		}
	}
	return stores
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
