package socket

import (
	"net"
	"sync"

	C "github.com/metacubex/mihomo/constant"
)

var (
	socketListener    *SocketListener
	socketListenerMux sync.Mutex
)

type SocketListener struct {
	tcpListener net.Listener
	udpConn     net.PacketConn
	addr        string
	tunnel      C.Tunnel
	closed      bool
}

func (l *SocketListener) RawAddress() string {
	return l.addr
}

func (l *SocketListener) Address() string {
	return l.tcpListener.Addr().String()
}

func (l *SocketListener) Close() error {
	l.closed = true
	if l.tcpListener != nil {
		l.tcpListener.Close()
	}
	if l.udpConn != nil {
		l.udpConn.Close()
	}
	return nil
}

func NewSocketListener(addr string, tunnel C.Tunnel) (*SocketListener, error) {
	tcpListener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	udpConn, err := net.ListenPacket("udp", addr)
	if err != nil {
		tcpListener.Close()
		return nil, err
	}

	sl := &SocketListener{
		tcpListener: tcpListener,
		udpConn:     udpConn,
		addr:        addr,
		tunnel:      tunnel,
	}

	go sl.handleTCPConnections()
	go sl.handleUDPConnections()

	return sl, nil
}

func (sl *SocketListener) handleTCPConnections() {
	//for {
	//	conn, err := sl.tcpListener.Accept()
	//	if err != nil {
	//		if sl.closed {
	//			break
	//		}
	//		log.Warnln("Failed to accept TCP connection:", err)
	//		continue
	//	}
	//
	//}
}

func (sl *SocketListener) handleUDPConnections() {
	//buf := make([]byte, 1500)
	//for {
	//	n, addr, err := sl.udpConn.ReadFrom(buf)
	//	if err != nil {
	//		if sl.closed {
	//			break
	//		}
	//		log.Warnln("Failed to read UDP packet:", err)
	//		continue
	//	}
	//
	//}
}
