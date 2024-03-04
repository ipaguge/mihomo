package resolver

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"time"

	"github.com/metacubex/mihomo/common/pool"

	D "github.com/miekg/dns"
)

const DefaultDnsReadTimeout = time.Second * 10
const DefaultDnsRelayTimeout = time.Second * 5

const SafeDnsPacketSize = 2 * 1024 // safe size which is 1232 from https://dnsflagday.net/2020/, so 2048 is enough

func RelayDnsConn(ctx context.Context, conn net.Conn) error {
	buff := pool.Get(pool.UDPBufferSize)
	defer func() {
		_ = pool.Put(buff)
		_ = conn.Close()
	}()
	for {
		if conn.SetReadDeadline(time.Now().Add(DefaultDnsReadTimeout)) != nil {
			break
		}

		length := uint16(0)
		if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
			break
		}

		if int(length) > len(buff) {
			break
		}

		n, err := io.ReadFull(conn, buff[:length])
		if err != nil {
			break
		}

		err = func() error {
			ctx, cancel := context.WithTimeout(ctx, DefaultDnsRelayTimeout)
			defer cancel()
			inData := buff[:n]
			msg, err := RelayDnsPacket(ctx, inData, buff)
			if err != nil {
				return err
			}

			err = binary.Write(conn, binary.BigEndian, uint16(len(msg)))
			if err != nil {
				return err
			}

			_, err = conn.Write(msg)
			if err != nil {
				return err
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

func RelayDnsPacket(ctx context.Context, payload []byte, target []byte) ([]byte, error) {
	msg := &D.Msg{}
	if err := msg.Unpack(payload); err != nil {
		return nil, err
	}

	r, err := ServeMsg(ctx, msg)
	if err != nil {
		m := new(D.Msg)
		m.SetRcode(msg, D.RcodeServerFailure)
		return m.PackBuffer(target)
	}

	r.SetRcode(msg, r.Rcode)
	r.Compress = true
	return r.PackBuffer(target)
}
