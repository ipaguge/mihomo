package dialer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/Dreamacro/clash/component/resolver"
)

var (
	dialMux                      sync.Mutex
	actualSingleStackDialContext = serialSingleStackDialContext
	actualDualStackDialContext   = serialDualStackDialContext
	tcpConcurrent                = false
	DisableIPv6                  = false
	ErrorInvalidedNetworkStack   = errors.New("invalided network stack")
	ErrorDisableIPv6             = errors.New("IPv6 is disabled, dialer cancel")
	ErrorConnTimeout             = errors.New("connect timeout")
)

func applyOptions(options ...Option) *option {
	opt := &option{
		interfaceName: DefaultInterface.Load(),
		routingMark:   int(DefaultRoutingMark.Load()),
	}

	for _, o := range DefaultOptions {
		o(opt)
	}

	for _, o := range options {
		o(opt)
	}

	return opt
}

func DialContext(ctx context.Context, network, address string, options ...Option) (net.Conn, error) {
	opt := applyOptions(options...)

	if opt.network == 4 || opt.network == 6 {
		if strings.Contains(network, "tcp") {
			network = "tcp"
		} else {
			network = "udp"
		}

		network = fmt.Sprintf("%s%d", network, opt.network)
	}

	switch network {
	case "tcp4", "tcp6", "udp4", "udp6":
		return actualSingleStackDialContext(ctx, network, address, opt)
	case "tcp", "udp":
		return actualDualStackDialContext(ctx, network, address, opt)
	default:
		return nil, ErrorInvalidedNetworkStack
	}
}

func ListenPacket(ctx context.Context, network, address string, options ...Option) (net.PacketConn, error) {
	cfg := applyOptions(options...)

	lc := &net.ListenConfig{}
	if cfg.interfaceName != "" {
		addr, err := bindIfaceToListenConfig(cfg.interfaceName, lc, network, address)
		if err != nil {
			return nil, err
		}
		address = addr
	}
	if cfg.addrReuse {
		addrReuseToListenConfig(lc)
	}
	if cfg.routingMark != 0 {
		bindMarkToListenConfig(cfg.routingMark, lc, network, address)
	}

	return lc.ListenPacket(ctx, network, address)
}

func SetDial(concurrent bool) {
	dialMux.Lock()
	tcpConcurrent = concurrent
	if concurrent {
		actualSingleStackDialContext = concurrentSingleStackDialContext
		actualDualStackDialContext = concurrentDualStackDialContext
	} else {
		actualSingleStackDialContext = serialSingleStackDialContext
		actualDualStackDialContext = serialDualStackDialContext
	}

	dialMux.Unlock()
}

func GetDial() bool {
	return tcpConcurrent
}

func dialContext(ctx context.Context, network string, destination netip.Addr, port string, opt *option) (net.Conn, error) {
	dialer := &net.Dialer{}
	if opt.interfaceName != "" {
		if err := bindIfaceToDialer(opt.interfaceName, dialer, network, destination); err != nil {
			return nil, err
		}
	}
	if opt.routingMark != 0 {
		bindMarkToDialer(opt.routingMark, dialer, network, destination)
	}

	if DisableIPv6 && destination.Is6() {
		return nil, ErrorDisableIPv6
	}

	address := net.JoinHostPort(destination.String(), port)
	if opt.tfo {
		return dialTFO(ctx, *dialer, network, address)
	}
	return dialer.DialContext(ctx, network, address)
}

func serialSingleStackDialContext(ctx context.Context, network string, address string, opt *option) (net.Conn, error) {
	ips, port, err := parseAddr(ctx, network, address, opt.resolver)
	if err != nil {
		return nil, err
	}
	return serialDialContext(ctx, network, ips, port, opt)
}

func serialDualStackDialContext(ctx context.Context, network, address string, opt *option) (net.Conn, error) {
	ips, port, err := parseAddr(ctx, network, address, opt.resolver)
	if err != nil {
		return nil, err
	}
	return dualStackDial(
		func() (net.Conn, error) { return serialDialContext(ctx, network, ips, port, opt) },
		func() (net.Conn, error) { return serialDialContext(ctx, network, ips, port, opt) },
		opt.prefer == 4)
}

func concurrentSingleStackDialContext(ctx context.Context, network string, address string, opt *option) (net.Conn, error) {
	ips, port, err := parseAddr(ctx, network, address, opt.resolver)
	if err != nil {
		return nil, err
	}

	if conn, err := parallelDialContext(ctx, network, ips, port, opt); err != nil {
		return nil, err
	} else {
		return conn, nil
	}
}

func concurrentDualStackDialContext(ctx context.Context, network, address string, opt *option) (net.Conn, error) {
	ips, port, err := parseAddr(ctx, network, address, opt.resolver)
	if err != nil {
		return nil, err
	}
	if opt.prefer != 4 && opt.prefer != 6 {
		return parallelDialContext(ctx, network, ips, port, opt)
	}
	ipv4s, ipv6s := sortationAddr(ips)
	return dualStackDial(
		func() (net.Conn, error) { return parallelDialContext(ctx, network, ipv4s, port, opt) },
		func() (net.Conn, error) { return parallelDialContext(ctx, network, ipv6s, port, opt) },
		opt.prefer == 4)
}

type Dialer struct {
	Opt option
}

func (d Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return DialContext(ctx, network, address, WithOption(d.Opt))
}

func (d Dialer) ListenPacket(ctx context.Context, network, address string, rAddrPort netip.AddrPort) (net.PacketConn, error) {
	return ListenPacket(ctx, ParseNetwork(network, rAddrPort.Addr()), address, WithOption(d.Opt))
}

func NewDialer(options ...Option) Dialer {
	opt := applyOptions(options...)
	return Dialer{Opt: *opt}
}

func dualStackDial(
	ipv4DialFn func() (net.Conn, error),
	ipv6DialFn func() (net.Conn, error),
	preferIPv4 bool) (net.Conn, error) {
	results := make(chan dialResult)
	returned := make(chan struct{})
	defer close(returned)
	racer := func(dial func() (net.Conn, error), isPrimary bool) {
		result := dialResult{isPrimary: isPrimary}
		defer func() {
			select {
			case results <- result:
			case <-returned:
				if result.Conn != nil {
					_ = result.Conn.Close()
				}
			}
		}()
		result.Conn, result.error = dial()
	}
	go racer(ipv4DialFn, preferIPv4)
	go racer(ipv6DialFn, !preferIPv4)
	var fallbackErr dialResult
	var primaryErr dialResult
	for res := range results {
		if res.error == nil {
			if res.isPrimary {
				return res.Conn, nil
			}
			fallbackErr = res
		}
		if res.isPrimary {
			primaryErr = res
		} else {
			fallbackErr = res
		}
	}
	if primaryErr.error != nil {
		return nil, primaryErr.error
	}
	return nil, fallbackErr.error
}

func parallelDialContext(ctx context.Context, network string, ips []netip.Addr, port string, opt *option) (net.Conn, error) {
	results := make(chan dialResult)
	returned := make(chan struct{})
	defer close(returned)
	tcpRacer := func(ctx context.Context, ip netip.Addr, port string) {
		result := dialResult{isPrimary: true}
		defer func() {
			select {
			case results <- result:
			case <-returned:
				if result.Conn != nil {
					_ = result.Conn.Close()
				}
			}
		}()
		result.ip = ip
		result.Conn, result.error = dialContext(ctx, network, ip, port, opt)
	}

	for _, ip := range ips {
		go tcpRacer(ctx, ip, port)
	}
	var err error
	for {
		select {
		case <-ctx.Done():
			if err != nil {
				return nil, err
			}
			if ctx.Err() == context.DeadlineExceeded {
				return nil, ErrorConnTimeout
			}
			return nil, ctx.Err()
		case res := <-results:
			if res.error == nil {
				return res.Conn, nil
			}
			err = res.error
		}
	}
}

func serialDialContext(ctx context.Context, network string, ips []netip.Addr, port string, opt *option) (net.Conn, error) {
	var (
		conn net.Conn
		err  error
		errs []error
	)
	for _, ip := range ips {
		if conn, err = dialContext(ctx, network, ip, port, opt); err == nil {
			return conn, nil
		} else {
			errs = append(errs, err)
		}
	}
	return nil, errors.Join(errs...)
}

type dialResult struct {
	ip netip.Addr
	net.Conn
	error
	isPrimary bool
}

func parseAddr(ctx context.Context,  network,address string, preferResolver resolver.Resolver) ([]netip.Addr, string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, "-1", err
	}

	var ips []netip.Addr
	switch network {
	case "tcp4", "udp4":
		if preferResolver == nil {
			ips, err = resolver.LookupIPv4ProxyServerHost(ctx, host)
		} else {
			ips, err = resolver.LookupIPv4WithResolver(ctx, host, preferResolver)
		}
	case "tcp6", "udp6":
		if preferResolver == nil {
			ips, err = resolver.LookupIPv6ProxyServerHost(ctx, host)
		} else {
			ips, err = resolver.LookupIPv6WithResolver(ctx, host, preferResolver)
		}
	default:
		if preferResolver == nil {
			ips, err = resolver.LookupIP(ctx, host)
		} else {
			ips, err = resolver.LookupIPWithResolver(ctx, host, preferResolver)
		}
	}
	if err != nil {
		return nil, "-1", fmt.Errorf("dns resolve failed: %w", err)
	}
	return ips, port, nil
}

func sortationAddr(ips []netip.Addr) (ipv4s, ipv6s []netip.Addr) {
	for _, v := range ips {
		if v.Is4() || v.Is4In6() {
			ipv4s = append(ipv4s, v)
		} else {
			ipv6s = append(ipv6s, v)
		}
	}
	return
}
