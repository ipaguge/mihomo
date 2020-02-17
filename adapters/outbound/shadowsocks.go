package outbound

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"github.com/Dreamacro/clash/common/structure"
	"github.com/Dreamacro/clash/component/dialer"
	obfs "github.com/Dreamacro/clash/component/simple-obfs"
	"github.com/Dreamacro/clash/component/socks5"
	v2rayObfs "github.com/Dreamacro/clash/component/v2ray-plugin"
	C "github.com/Dreamacro/clash/constant"

	"github.com/Dreamacro/go-shadowsocks2/core"
)

type ShadowSocks struct {
	*Base
	server string
	cipher core.Cipher

	// obfs
	obfsMode    string
	obfsOption  *simpleObfsOption
	v2rayOption *v2rayObfs.Option
}

type ShadowSocksOption struct {
	Name       string                 `proxy:"name"`
	Server     string                 `proxy:"server"`
	Port       int                    `proxy:"port"`
	Password   string                 `proxy:"password"`
	Cipher     string                 `proxy:"cipher"`
	UDP        bool                   `proxy:"udp,omitempty"`
	Plugin     string                 `proxy:"plugin,omitempty"`
	PluginOpts map[string]interface{} `proxy:"plugin-opts,omitempty"`

	// deprecated when bump to 1.0
	Obfs     string `proxy:"obfs,omitempty"`
	ObfsHost string `proxy:"obfs-host,omitempty"`
}

type simpleObfsOption struct {
	Mode string `obfs:"mode"`
	Host string `obfs:"host,omitempty"`
}

type v2rayObfsOption struct {
	Mode           string            `obfs:"mode"`
	Host           string            `obfs:"host,omitempty"`
	Path           string            `obfs:"path,omitempty"`
	TLS            bool              `obfs:"tls,omitempty"`
	Headers        map[string]string `obfs:"headers,omitempty"`
	SkipCertVerify bool              `obfs:"skip-cert-verify,omitempty"`
	Mux            bool              `obfs:"mux,omitempty"`
}

func (ss *ShadowSocks) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	c, err := dialer.DialContext(ctx, "tcp", ss.server)
	if err != nil {
		return nil, fmt.Errorf("%s connect error: %w", ss.server, err)
	}
	tcpKeepAlive(c)
	switch ss.obfsMode {
	case "tls":
		c = obfs.NewTLSObfs(c, ss.obfsOption.Host)
	case "http":
		_, port, _ := net.SplitHostPort(ss.server)
		c = obfs.NewHTTPObfs(c, ss.obfsOption.Host, port)
	case "websocket":
		var err error
		c, err = v2rayObfs.NewV2rayObfs(c, ss.v2rayOption)
		if err != nil {
			return nil, fmt.Errorf("%s connect error: %w", ss.server, err)
		}
	}
	c = ss.cipher.StreamConn(c)
	_, err = c.Write(serializesSocksAddr(metadata))
	return newConn(c, ss), err
}

func (ss *ShadowSocks) DialUDP(metadata *C.Metadata) (C.PacketConn, error) {
	pc, err := dialer.ListenPacket("udp", "")
	if err != nil {
		return nil, err
	}

	addr, err := resolveUDPAddr("udp", ss.server)
	if err != nil {
		return nil, err
	}

	pc = ss.cipher.PacketConn(pc)
	return newPacketConn(&ssPacketConn{PacketConn: pc, rAddr: addr}, ss), nil
}

func (ss *ShadowSocks) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"type": ss.Type().String(),
	})
}

func NewShadowSocks(option ShadowSocksOption) (*ShadowSocks, error) {
	server := net.JoinHostPort(option.Server, strconv.Itoa(option.Port))
	cipher := option.Cipher
	password := option.Password
	ciph, err := core.PickCipher(cipher, nil, password)
	if err != nil {
		return nil, fmt.Errorf("ss %s initialize error: %w", server, err)
	}

	var v2rayOption *v2rayObfs.Option
	var obfsOption *simpleObfsOption
	obfsMode := ""

	// forward compatibility before 1.0
	if option.Obfs != "" {
		obfsMode = option.Obfs
		obfsOption = &simpleObfsOption{
			Host: "bing.com",
		}
		if option.ObfsHost != "" {
			obfsOption.Host = option.ObfsHost
		}
	}

	decoder := structure.NewDecoder(structure.Option{TagName: "obfs", WeaklyTypedInput: true})
	if option.Plugin == "obfs" {
		opts := simpleObfsOption{Host: "bing.com"}
		if err := decoder.Decode(option.PluginOpts, &opts); err != nil {
			return nil, fmt.Errorf("ss %s initialize obfs error: %w", server, err)
		}

		if opts.Mode != "tls" && opts.Mode != "http" {
			return nil, fmt.Errorf("ss %s obfs mode error: %s", server, opts.Mode)
		}
		obfsMode = opts.Mode
		obfsOption = &opts
	} else if option.Plugin == "v2ray-plugin" {
		opts := v2rayObfsOption{Host: "bing.com", Mux: true}
		if err := decoder.Decode(option.PluginOpts, &opts); err != nil {
			return nil, fmt.Errorf("ss %s initialize v2ray-plugin error: %w", server, err)
		}

		if opts.Mode != "websocket" {
			return nil, fmt.Errorf("ss %s obfs mode error: %s", server, opts.Mode)
		}
		obfsMode = opts.Mode

		var tlsConfig *tls.Config
		if opts.TLS {
			tlsConfig = &tls.Config{
				ServerName:         opts.Host,
				InsecureSkipVerify: opts.SkipCertVerify,
				ClientSessionCache: getClientSessionCache(),
			}
		}
		v2rayOption = &v2rayObfs.Option{
			Host:      opts.Host,
			Path:      opts.Path,
			Headers:   opts.Headers,
			TLSConfig: tlsConfig,
			Mux:       opts.Mux,
		}
	}

	return &ShadowSocks{
		Base: &Base{
			name: option.Name,
			tp:   C.Shadowsocks,
			udp:  option.UDP,
		},
		server: server,
		cipher: ciph,

		obfsMode:    obfsMode,
		v2rayOption: v2rayOption,
		obfsOption:  obfsOption,
	}, nil
}

type ssPacketConn struct {
	net.PacketConn
	rAddr net.Addr
}

func (spc *ssPacketConn) WriteTo(b []byte, addr net.Addr) (n int, err error) {
	packet, err := socks5.EncodeUDPPacket(socks5.ParseAddrToSocksAddr(addr), b)
	if err != nil {
		return
	}
	return spc.PacketConn.WriteTo(packet[3:], spc.rAddr)
}

func (spc *ssPacketConn) WriteWithMetadata(p []byte, metadata *C.Metadata) (n int, err error) {
	packet, err := socks5.EncodeUDPPacket(socks5.ParseAddr(metadata.RemoteAddress()), p)
	if err != nil {
		return
	}
	return spc.PacketConn.WriteTo(packet[3:], spc.rAddr)
}

func (spc *ssPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, _, e := spc.PacketConn.ReadFrom(b)
	addr := socks5.SplitAddr(b[:n])
	var from net.Addr
	if e == nil {
		// Get the source IP/Port of packet.
		from = addr.UDPAddr()
	}
	copy(b, b[len(addr):])
	return n - len(addr), from, e
}
