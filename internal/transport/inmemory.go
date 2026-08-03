package transport

import (
	"errors"
	"sync"

	"takoda99/internal/proto"
)

// inmemory.go は【#31】実ソケット無しの疑似接続。Bot（#35）と99人負荷検証（#50a/b）の土台。
// Connection interface を実WSと同一に満たすので、room/session からは区別なく扱える。

// ErrConnClosed は閉じた接続への Send で返る。
var ErrConnClosed = errors.New("transport: connection closed")

// Pipe は相互接続された2つの Connection を返す（net.Pipe と同型）。
// 一方の Send は他方の Receive に届く。片方が Close すると両方の Receive が閉じる（切断）。
// 想定用途: room 側が一方を持ち、Bot/テスト側がもう一方を駆動する。
func Pipe() (Connection, Connection) {
	const buf = 64
	ai := make(chan proto.Envelope, buf) // a の受信箱（b が届ける）
	bi := make(chan proto.Envelope, buf) // b の受信箱（a が届ける）
	ad := make(chan struct{})
	bd := make(chan struct{})

	a := &inMemoryConn{peerInbox: bi, out: make(chan proto.Envelope, buf), done: ad, peerDone: bd}
	b := &inMemoryConn{peerInbox: ai, out: make(chan proto.Envelope, buf), done: bd, peerDone: ad}
	go a.forward(ai)
	go b.forward(bi)
	return a, b
}

type inMemoryConn struct {
	peerInbox chan<- proto.Envelope // Send はここへ書く（相手が受け取る）。閉じない（panic回避）
	out       chan proto.Envelope   // Receive が返す公開チャネル。forward が done で閉じる
	done      chan struct{}         // この端の Close で閉じる
	peerDone  <-chan struct{}       // 相手の done（相手切断の検知）
	once      sync.Once
}

// forward は内部受信箱 inbox を公開チャネル out へ中継し、自端/相手の Close で out を閉じる。
// （送信側は inbox を閉じないので、中継役の out クローズで EOF を表現する）
func (c *inMemoryConn) forward(inbox <-chan proto.Envelope) {
	defer close(c.out)
	for {
		select {
		case env := <-inbox:
			select {
			case c.out <- env:
			case <-c.done:
				return
			case <-c.peerDone:
				return
			}
		case <-c.done:
			return
		case <-c.peerDone:
			return
		}
	}
}

func (c *inMemoryConn) Send(env proto.Envelope) error {
	// 切断を最優先で確認する。バッファに空きがあると select が送信成功を選び得るため、
	// 先に非ブロッキングで done/peerDone を見る（さもないと閉じた接続への Send が成功してしまう）。
	select {
	case <-c.done:
		return ErrConnClosed
	case <-c.peerDone:
		return ErrConnClosed
	default:
	}
	select {
	case c.peerInbox <- env:
		return nil
	case <-c.done:
		return ErrConnClosed
	case <-c.peerDone:
		return ErrConnClosed
	}
}

func (c *inMemoryConn) Receive() <-chan proto.Envelope { return c.out }

func (c *inMemoryConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

var _ Connection = (*inMemoryConn)(nil)
