// Modified from: https://github.com/Qv2ray/gun-lite
// License: MIT

package gun

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"go.uber.org/atomic"
	"golang.org/x/net/http2"
)

var (
	ErrInvalidLength = errors.New("invalid length")
	ErrSmallBuffer   = errors.New("buffer too small")
)

var (
	defaultHeader = http.Header{
		"content-type": []string{"application/grpc"},
		"user-agent":   []string{"grpc-go/1.36.0"},
	}
)

type DialFn = func(network, addr string) (net.Conn, error)

type Conn struct {
	response *http.Response
	request  *http.Request
	client   *http.Client
	writer   *io.PipeWriter
	once     sync.Once
	close    *atomic.Bool
	err      error
	remain   int
	br       *bufio.Reader
}

type Config struct {
	ServiceName string
	Host        string
}

func (g *Conn) initRequest() {
	response, err := g.client.Do(g.request)
	if err != nil {
		g.err = err
		g.writer.Close()
		return
	}

	if !g.close.Load() {
		g.response = response
	} else {
		response.Body.Close()
	}
}

func (g *Conn) Read(b []byte) (n int, err error) {
	g.once.Do(g.initRequest)
	if g.err != nil {
		return 0, g.err
	}

	if g.br != nil {
		remain := g.br.Buffered()
		if len(b) < remain {
			remain = len(b)
		}

		n, err = g.br.Read(b[:remain])
		if g.br.Buffered() == 0 {
			g.br = nil
		}
		return
	} else if g.remain != 0 {
		size := g.remain
		if len(b) < size {
			size = len(b)
		}

		n, err = g.response.Body.Read(b[:size])
		g.remain -= n
		return
	} else if g.response == nil {
		return 0, net.ErrClosed
	}

	// 0x00 grpclength(uint32) 0x0A uleb128 payload
	buf := make([]byte, 6)
	_, err = io.ReadFull(g.response.Body, buf)
	if err != nil {
		return 0, err
	}

	br := bufio.NewReaderSize(g.response.Body, 16)
	protobufPayloadLen, err := binary.ReadUvarint(br)
	if err != nil {
		return 0, ErrInvalidLength
	}

	bufferedSize := br.Buffered()
	if len(b) < bufferedSize {
		n, err = br.Read(b)
		g.br = br
		g.remain = int(protobufPayloadLen) - n - g.br.Buffered()
		return
	}

	_, err = br.Read(b[:bufferedSize])
	if err != nil {
		return
	}

	offset := int(protobufPayloadLen)
	if len(b) < int(protobufPayloadLen) {
		offset = len(b)
	}

	if offset == bufferedSize {
		return bufferedSize, nil
	}

	n, err = io.ReadFull(g.response.Body, b[bufferedSize:offset])
	if err != nil {
		return
	}

	remain := int(protobufPayloadLen) - offset
	if remain > 0 {
		g.remain = remain
	}

	return offset, nil
}

func (g *Conn) Write(b []byte) (n int, err error) {
	protobufHeader := appendUleb128([]byte{0x0A}, uint64(len(b)))
	grpcHeader := make([]byte, 5)
	grpcPayloadLen := uint32(len(protobufHeader) + len(b))
	binary.BigEndian.PutUint32(grpcHeader[1:5], grpcPayloadLen)

	buffers := net.Buffers{grpcHeader, protobufHeader, b}
	_, err = buffers.WriteTo(g.writer)
	if err == io.ErrClosedPipe && g.err != nil {
		err = g.err
	}

	return len(b), err
}

func (g *Conn) Close() error {
	g.close.Store(true)
	if r := g.response; r != nil {
		r.Body.Close()
	}

	return g.writer.Close()
}

func (g *Conn) LocalAddr() net.Addr                { return &net.TCPAddr{IP: net.IPv4zero, Port: 0} }
func (g *Conn) RemoteAddr() net.Addr               { return &net.TCPAddr{IP: net.IPv4zero, Port: 0} }
func (g *Conn) SetDeadline(t time.Time) error      { return nil }
func (g *Conn) SetReadDeadline(t time.Time) error  { return nil }
func (g *Conn) SetWriteDeadline(t time.Time) error { return nil }

func NewHTTP2Client(dialFn DialFn, tlsConfig *tls.Config) *http2.Transport {
	dialFunc := func(network, addr string, cfg *tls.Config) (net.Conn, error) {
		pconn, err := dialFn(network, addr)
		if err != nil {
			return nil, err
		}

		cn := tls.Client(pconn, cfg)
		if err := cn.Handshake(); err != nil {
			pconn.Close()
			return nil, err
		}
		state := cn.ConnectionState()
		if p := state.NegotiatedProtocol; p != http2.NextProtoTLS {
			cn.Close()
			return nil, fmt.Errorf("http2: unexpected ALPN protocol %s, want %s", p, http2.NextProtoTLS)
		}
		return cn, nil
	}

	return &http2.Transport{
		DialTLS:            dialFunc,
		TLSClientConfig:    tlsConfig,
		AllowHTTP:          false,
		DisableCompression: true,
		ReadIdleTimeout:    0,
		PingTimeout:        0,
	}
}

func StreamGunWithTransport(transport *http2.Transport, cfg *Config) (net.Conn, error) {
	serviceName := "GunService"
	if cfg.ServiceName != "" {
		serviceName = cfg.ServiceName
	}

	client := &http.Client{
		Transport: transport,
	}

	reader, writer := io.Pipe()
	request := &http.Request{
		Method: http.MethodPost,
		Body:   reader,
		URL: &url.URL{
			Scheme: "https",
			Host:   cfg.Host,
			Path:   fmt.Sprintf("/%s/Tun", serviceName),
		},
		Proto:      "HTTP/2",
		ProtoMajor: 2,
		ProtoMinor: 0,
		Header:     defaultHeader,
	}

	conn := &Conn{
		request: request,
		client:  client,
		writer:  writer,
		close:   atomic.NewBool(false),
	}

	go conn.once.Do(conn.initRequest)
	return conn, nil
}

func StreamGunWithConn(conn net.Conn, tlsConfig *tls.Config, cfg *Config) (net.Conn, error) {
	dialFn := func(network, addr string) (net.Conn, error) {
		return conn, nil
	}

	transport := NewHTTP2Client(dialFn, tlsConfig)
	return StreamGunWithTransport(transport, cfg)
}
