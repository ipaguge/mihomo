package tproxy

import (
	"net"

	"github.com/Dreamacro/clash/adapter/inbound"
	C "github.com/Dreamacro/clash/constant"
	"github.com/Dreamacro/clash/transport/socks5"
)

type Listener struct {
	listener net.Listener
	addr     string
	closed   bool
}

// RawAddress implements C.Listener
func (l *Listener) RawAddress() string {
	return l.addr
}

// Address implements C.Listener
func (l *Listener) Address() string {
	return l.listener.Addr().String()
}

// Close implements C.Listener
func (l *Listener) Close() error {
	l.closed = true
	return l.listener.Close()
}

func (l *Listener) handleTProxy(conn net.Conn, in chan<- C.ConnContext, additions ...inbound.Addition) {
	target := socks5.ParseAddrToSocksAddr(conn.LocalAddr())
	conn.(*net.TCPConn).SetKeepAlive(true)
	in <- inbound.NewSocket(target, conn, C.TPROXY, additions...)
}

func New(addr string, in chan<- C.ConnContext, additions ...inbound.Addition) (*Listener, error) {
	if len(additions) == 0 {
		additions = []inbound.Addition{{
			InName:       "DEFAULT-TPROXY",
			SpecialRules: "",
		}}
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	tl := l.(*net.TCPListener)
	rc, err := tl.SyscallConn()
	if err != nil {
		return nil, err
	}

	err = setsockopt(rc, addr)
	if err != nil {
		return nil, err
	}

	rl := &Listener{
		listener: l,
		addr:     addr,
	}

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				if rl.closed {
					break
				}
				continue
			}
			go rl.handleTProxy(c, in, additions...)
		}
	}()

	return rl, nil
}
