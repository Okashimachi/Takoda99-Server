package sim

import (
	"fmt"
	"math/rand"

	"takoda99/internal/game"
	"takoda99/internal/odai"
	"takoda99/internal/proto"
	"takoda99/internal/typist"
)

// dummy.go はシミュレータ内の仮想プレイヤー（ダミー店）。
// 実力を (1打鍵あたりのms, 打鍵ごとのミス率) の2値で表し、人間の分布を模す。

// Profile は店舗の実力分布のプリセット。
type Profile string

const (
	ProfileUniform Profile = "uniform" // 全員同じ実力（膠着の最悪ケース）
	ProfileNormal  Profile = "normal"  // 正規分布（現実に近い）
	ProfileBipolar Profile = "bipolar" // 二極化（上手い/下手がはっきり）
	ProfileWide    Profile = "wide"    // 実力差が非常に大きい

	// ProfileDuel は **速さ型 vs 正確型** を半々で戦わせる（plan-h26 §2.1・最重要の検証）。
	//
	// 狙いは「どちらかが常勝でない重み比率」を見つけること。
	// 速さ型は生の打鍵が速いぶん**たこ焼きを多く作れるが、ミスも多い**。
	// 正確型はほぼミスしないが**作れる数が少ない**。この2つが拮抗する
	// score.weightMiss を探すのが h26 の中心作業。
	ProfileDuel Profile = "duel"
)

// 速さ型 / 正確型 の実力値（ProfileDuel）。
//
// 実効時間（打ち直しを含む1打鍵あたり）は
//
//	速さ型 = 150 × (1 + 0.12) = 168ms
//	正確型 = 200 × (1 + 0.01) = 202ms
//
// 速さ型が約 1.20 倍のたこ焼きを作れる代わりに、ミスは 12 倍出る。
// **どちらが勝つかは weightMiss だけで決まる**ようにしてある。
const (
	duelFastMsPerKey    = 150
	duelFastMissRate    = 0.12
	duelPreciseMsPerKey = 200
	duelPreciseMissRate = 0.01
)

// 実力クラス（ProfileDuel でのみ付く）。
const (
	ClassFast    = "fast"
	ClassPrecise = "precise"
)

// AllProfiles は決着保証テストが回すプリセット。
//
// duel は「決着するか」ではなく「どちらが勝つか」を見るためのものなので、ここには含めない
// （含めても決着保証テストの実行時間が伸びるだけで得るものが無い）。
func AllProfiles() []Profile {
	return []Profile{ProfileUniform, ProfileNormal, ProfileBipolar, ProfileWide}
}

// ParseProfile は文字列を Profile に解決する。
func ParseProfile(s string) (Profile, error) {
	switch p := Profile(s); p {
	case ProfileUniform, ProfileNormal, ProfileBipolar, ProfileWide, ProfileDuel:
		return p, nil
	default:
		return "", fmt.Errorf("未知の profile %q（uniform|normal|bipolar|wide|duel）", s)
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
	class    string  // 実力クラス（ProfileDuel のみ。"fast" / "precise"）

	alive bool

	// queue は session 側の storeQueues[id] を写した行列（先頭 = 対応中）。
	// session は「行列先頭でない客」への OrderServed を弾くので、
	// 来店(CustomerArrived)と提供の2つで必ず同期を保つ。
	// （本戦では客が逃げないので、離脱による同期ズレはもう起きない。）
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
//
// 🔴 **判定式は実 Bot と共有する**（typist.Serve・plan-h31 §5）。別々に書くと必ずズレる
// ——実際 h31 の前は「sim は打鍵ごとにミス判定、実 Bot は miss∈{0,1}」とズレており、
// sim で詰めた weightMiss が実試合で成立しない状態だった。
//
// ⚠ heatLevel に 0 を渡しているのは**現時点で意図どおり**。sim のダミー店に難度追従と
// 速度/ミス率の相関を入れるのは h33 の範囲で、h31 では sim の挙動を1ビットも変えない
// （変えると h26 で詰めた weightMiss=30 の根拠が同時に動いて切り分けられなくなる）。
func (d *dummyStore) begin(q queuedOrder, rng *rand.Rand) *pendingOrder {
	keys := q.keystrokes
	if keys <= 0 {
		keys = 1
	}
	// ミス1回につき1打鍵ぶん打ち直す。報告する elapsedMs は打ち直しを含む実測時間にする
	// （実クライアントが送るのは「実際に掛かった時間」なので、そこを合わせる）。
	out := typist.Serve(typist.Ability{
		MsPerKey: float64(d.msPerKey),
		MissRate: d.missRate,
	}, keys, 0, rng)
	return &pendingOrder{
		customerId:  q.customerId,
		keystrokes:  keys,
		missCount:   out.MissCount,
		totalMs:     out.ElapsedMs,
		remainingMs: out.ElapsedMs,
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
func buildStores(n int, p Profile, rng *rand.Rand) []*dummyStore {
	stores := make([]*dummyStore, n)
	for i := range stores {
		var msPerKey int
		var missRate float64
		var class string
		switch p {
		case ProfileUniform:
			msPerKey, missRate = 200, 0.05
		case ProfileNormal:
			msPerKey = 200 + int(rng.NormFloat64()*50)
			missRate = clamp(0.05+rng.NormFloat64()*0.02, 0, 0.5)
		case ProfileBipolar:
			if i%2 == 0 {
				msPerKey, missRate = 130, 0.02
			} else {
				msPerKey, missRate = 300, 0.10
			}
		case ProfileWide:
			msPerKey = 100 + rng.Intn(400)
			missRate = rng.Float64() * 0.2
		case ProfileDuel:
			if i%2 == 0 {
				msPerKey, missRate, class = duelFastMsPerKey, duelFastMissRate, ClassFast
			} else {
				msPerKey, missRate, class = duelPreciseMsPerKey, duelPreciseMissRate, ClassPrecise
			}
		}
		if msPerKey < 50 {
			msPerKey = 50
		}
		stores[i] = &dummyStore{
			id:       game.PlayerId(fmt.Sprintf("s-%d", i+1)),
			msPerKey: msPerKey,
			missRate: missRate,
			class:    class,
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
