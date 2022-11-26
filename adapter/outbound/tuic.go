package outbound

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/metacubex/quic-go"

	"github.com/Dreamacro/clash/common/generics/list"
	"github.com/Dreamacro/clash/component/dialer"
	tlsC "github.com/Dreamacro/clash/component/tls"
	C "github.com/Dreamacro/clash/constant"
	"github.com/Dreamacro/clash/transport/tuic"
)

type Tuic struct {
	*Base
	dialFn    func(ctx context.Context, t *Tuic, opts ...dialer.Option) (net.PacketConn, net.Addr, error)
	newClient func(udp bool, opts ...dialer.Option) *tuic.Client
	getClient func(udp bool, opts ...dialer.Option) *tuic.Client
}

type TuicOption struct {
	BasicOption
	Name                  string   `proxy:"name"`
	Server                string   `proxy:"server"`
	Port                  int      `proxy:"port"`
	Token                 string   `proxy:"token"`
	Ip                    string   `proxy:"ip,omitempty"`
	HeartbeatInterval     int      `proxy:"heartbeat-interval,omitempty"`
	ALPN                  []string `proxy:"alpn,omitempty"`
	ReduceRtt             bool     `proxy:"reduce-rtt,omitempty"`
	RequestTimeout        int      `proxy:"request-timeout,omitempty"`
	UdpRelayMode          string   `proxy:"udp-relay-mode,omitempty"`
	CongestionController  string   `proxy:"congestion-controller,omitempty"`
	DisableSni            bool     `proxy:"disable-sni,omitempty"`
	MaxUdpRelayPacketSize int      `proxy:"max-udp-relay-packet-size,omitempty"`

	FastOpen            bool   `proxy:"fast-open,omitempty"`
	SkipCertVerify      bool   `proxy:"skip-cert-verify,omitempty"`
	Fingerprint         string `proxy:"fingerprint,omitempty"`
	CustomCA            string `proxy:"ca,omitempty"`
	CustomCAString      string `proxy:"ca-str,omitempty"`
	ReceiveWindowConn   int    `proxy:"recv-window-conn,omitempty"`
	ReceiveWindow       int    `proxy:"recv-window,omitempty"`
	DisableMTUDiscovery bool   `proxy:"disable-mtu-discovery,omitempty"`
}

// DialContext implements C.ProxyAdapter
func (t *Tuic) DialContext(ctx context.Context, metadata *C.Metadata, opts ...dialer.Option) (C.Conn, error) {
	opts = t.Base.DialOptions(opts...)
	dialFn := func(ctx context.Context) (net.PacketConn, net.Addr, error) {
		return t.dialFn(ctx, t, opts...)
	}
	conn, err := t.getClient(false, opts...).DialContext(ctx, metadata, dialFn)
	if errors.Is(err, tuic.TooManyOpenStreams) {
		conn, err = t.newClient(false, opts...).DialContext(ctx, metadata, dialFn)
	}
	if err != nil {
		return nil, err
	}
	return NewConn(conn, t), err
}

// ListenPacketContext implements C.ProxyAdapter
func (t *Tuic) ListenPacketContext(ctx context.Context, metadata *C.Metadata, opts ...dialer.Option) (_ C.PacketConn, err error) {
	opts = t.Base.DialOptions(opts...)
	dialFn := func(ctx context.Context) (net.PacketConn, net.Addr, error) {
		return t.dialFn(ctx, t, opts...)
	}
	pc, err := t.getClient(true, opts...).ListenPacketContext(ctx, metadata, dialFn)
	if errors.Is(err, tuic.TooManyOpenStreams) {
		pc, err = t.newClient(false, opts...).ListenPacketContext(ctx, metadata, dialFn)
	}
	if err != nil {
		return nil, err
	}
	return newPacketConn(pc, t), nil
}

func NewTuic(option TuicOption) (*Tuic, error) {
	addr := net.JoinHostPort(option.Server, strconv.Itoa(option.Port))
	serverName := option.Server

	tlsConfig := &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: option.SkipCertVerify,
		MinVersion:         tls.VersionTLS13,
	}

	var bs []byte
	var err error
	if len(option.CustomCA) > 0 {
		bs, err = os.ReadFile(option.CustomCA)
		if err != nil {
			return nil, fmt.Errorf("tuic %s load ca error: %w", addr, err)
		}
	} else if option.CustomCAString != "" {
		bs = []byte(option.CustomCAString)
	}

	if len(bs) > 0 {
		block, _ := pem.Decode(bs)
		if block == nil {
			return nil, fmt.Errorf("CA cert is not PEM")
		}

		fpBytes := sha256.Sum256(block.Bytes)
		if len(option.Fingerprint) == 0 {
			option.Fingerprint = hex.EncodeToString(fpBytes[:])
		}
	}

	if len(option.Fingerprint) != 0 {
		var err error
		tlsConfig, err = tlsC.GetSpecifiedFingerprintTLSConfig(tlsConfig, option.Fingerprint)
		if err != nil {
			return nil, err
		}
	} else {
		tlsConfig = tlsC.GetGlobalFingerprintTLCConfig(tlsConfig)
	}

	if len(option.ALPN) > 0 {
		tlsConfig.NextProtos = option.ALPN
	} else {
		tlsConfig.NextProtos = []string{"h3"}
	}

	if option.RequestTimeout == 0 {
		option.RequestTimeout = 8000
	}

	if option.HeartbeatInterval <= 0 {
		option.HeartbeatInterval = 10000
	}

	if option.UdpRelayMode != "quic" {
		option.UdpRelayMode = "native"
	}

	if option.MaxUdpRelayPacketSize == 0 {
		option.MaxUdpRelayPacketSize = 1500
	}

	quicConfig := &quic.Config{
		InitialStreamReceiveWindow:     uint64(option.ReceiveWindowConn),
		MaxStreamReceiveWindow:         uint64(option.ReceiveWindowConn),
		InitialConnectionReceiveWindow: uint64(option.ReceiveWindow),
		MaxConnectionReceiveWindow:     uint64(option.ReceiveWindow),
		KeepAlivePeriod:                time.Duration(option.HeartbeatInterval) * time.Millisecond,
		DisablePathMTUDiscovery:        option.DisableMTUDiscovery,
		EnableDatagrams:                true,
	}
	if option.ReceiveWindowConn == 0 {
		quicConfig.InitialStreamReceiveWindow = DefaultStreamReceiveWindow / 10
		quicConfig.MaxStreamReceiveWindow = DefaultStreamReceiveWindow
	}
	if option.ReceiveWindow == 0 {
		quicConfig.InitialConnectionReceiveWindow = DefaultConnectionReceiveWindow / 10
		quicConfig.MaxConnectionReceiveWindow = DefaultConnectionReceiveWindow
	}

	if len(option.Ip) > 0 {
		addr = net.JoinHostPort(option.Ip, strconv.Itoa(option.Port))
	}
	host := option.Server
	if option.DisableSni {
		host = ""
		tlsConfig.ServerName = ""
	}
	tkn := tuic.GenTKN(option.Token)

	t := &Tuic{
		Base: &Base{
			name:   option.Name,
			addr:   addr,
			tp:     C.Tuic,
			udp:    true,
			iface:  option.Interface,
			prefer: C.NewDNSPrefer(option.IPVersion),
		},
	}

	type dialResult struct {
		pc   net.PacketConn
		addr net.Addr
		err  error
	}
	dialResultMap := make(map[any]dialResult)
	dialResultMutex := &sync.Mutex{}
	tcpClients := list.New[*tuic.Client]()
	tcpClientsMutex := &sync.Mutex{}
	udpClients := list.New[*tuic.Client]()
	udpClientsMutex := &sync.Mutex{}
	t.dialFn = func(ctx context.Context, t *Tuic, opts ...dialer.Option) (pc net.PacketConn, addr net.Addr, err error) {
		var o any = *dialer.ApplyOptions(opts...)

		dialResultMutex.Lock()
		dr, ok := dialResultMap[o]
		dialResultMutex.Unlock()
		if ok {
			return dr.pc, dr.addr, dr.err
		}

		pc, err = dialer.ListenPacket(ctx, "udp", "", opts...)
		if err != nil {
			return nil, nil, err
		}
		addr, err = resolveUDPAddrWithPrefer(ctx, "udp", t.addr, t.prefer)
		if err != nil {
			return nil, nil, err
		}

		dr.pc, dr.addr, dr.err = pc, addr, err

		dialResultMutex.Lock()
		dialResultMap[o] = dr
		dialResultMutex.Unlock()
		return pc, addr, err
	}
	closeFn := func(t *Tuic) {
		dialResultMutex.Lock()
		defer dialResultMutex.Unlock()
		for key := range dialResultMap {
			pc := dialResultMap[key].pc
			if pc != nil {
				_ = pc.Close()
			}
			delete(dialResultMap, key)
		}
	}
	t.newClient = func(udp bool, opts ...dialer.Option) *tuic.Client {
		clients := tcpClients
		clientsMutex := tcpClientsMutex
		if udp {
			clients = udpClients
			clientsMutex = udpClientsMutex
		}

		var o any = *dialer.ApplyOptions(opts...)

		clientsMutex.Lock()
		defer clientsMutex.Unlock()

		client := &tuic.Client{
			TlsConfig:             tlsConfig,
			QuicConfig:            quicConfig,
			Host:                  host,
			Token:                 tkn,
			UdpRelayMode:          option.UdpRelayMode,
			CongestionController:  option.CongestionController,
			ReduceRtt:             option.ReduceRtt,
			RequestTimeout:        option.RequestTimeout,
			MaxUdpRelayPacketSize: option.MaxUdpRelayPacketSize,
			FastOpen:              option.FastOpen,
			Inference:             t,
			Key:                   o,
			LastVisited:           time.Now(),
			UDP:                   udp,
		}
		clients.PushFront(client)
		runtime.SetFinalizer(client, closeTuicClient)
		return client
	}
	t.getClient = func(udp bool, opts ...dialer.Option) *tuic.Client {
		clients := tcpClients
		clientsMutex := tcpClientsMutex
		if udp {
			clients = udpClients
			clientsMutex = udpClientsMutex
		}

		var o any = *dialer.ApplyOptions(opts...)
		var bestClient *tuic.Client

		func() {
			clientsMutex.Lock()
			defer clientsMutex.Unlock()
			for it := clients.Front(); it != nil; {
				client := it.Value
				if client == nil {
					next := it.Next()
					clients.Remove(it)
					it = next
					continue
				}
				if client.Key == o {
					if bestClient == nil {
						bestClient = client
					} else {
						if client.OpenStreams.Load() < bestClient.OpenStreams.Load() {
							bestClient = client
						}
					}
				}
				if client.OpenStreams.Load() == 0 && time.Now().Sub(client.LastVisited) > 30*time.Minute {
					next := it.Next()
					clients.Remove(it)
					it = next
					continue
				}
				it = it.Next()
			}
		}()

		if bestClient == nil {
			return t.newClient(udp, opts...)
		} else {
			return bestClient
		}
	}
	runtime.SetFinalizer(t, closeFn)
	return t, nil
}

func closeTuicClient(client *tuic.Client) {
	client.Close(tuic.ClientClosed)
}
