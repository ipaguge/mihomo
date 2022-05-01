package sniffer

import (
	"bytes"
	"errors"
	C "github.com/Dreamacro/clash/constant"
	"net"
	"strconv"
	"strings"
)

var (
	// refer to https://pkg.go.dev/net/http@master#pkg-constants
	methods          = [...]string{"get", "post", "head", "put", "delete", "options", "connect", "patch", "trace"}
	errNotHTTPMethod = errors.New("not an HTTP method")
)

type version byte

const (
	HTTP1 version = iota
	HTTP2
)

type HTTPSniffer struct {
	version version
	host    string
}

func (http *HTTPSniffer) Protocol() string {
	switch http.version {
	case HTTP1:
		return "http1"
	case HTTP2:
		return "http2"
	default:
		return "unknown"
	}
}

func (http *HTTPSniffer) SupportNetwork() C.NetWork {
	return C.TCP
}

func (http *HTTPSniffer) SniffTCP(bytes []byte) (string, error) {
	domain, err := SniffHTTP(bytes)
	if err == nil {
		return *domain, nil
	} else {
		return "", err
	}
}

func beginWithHTTPMethod(b []byte) error {
	for _, m := range &methods {
		if len(b) >= len(m) && strings.EqualFold(string(b[:len(m)]), m) {
			return nil
		}

		if len(b) < len(m) {
			return ErrNoClue
		}
	}
	return errNotHTTPMethod
}

func SniffHTTP(b []byte) (*string, error) {
	if err := beginWithHTTPMethod(b); err != nil {
		return nil, err
	}

	_ = &HTTPSniffer{
		version: HTTP1,
	}

	headers := bytes.Split(b, []byte{'\n'})
	for i := 1; i < len(headers); i++ {
		header := headers[i]
		if len(header) == 0 {
			break
		}
		parts := bytes.SplitN(header, []byte{':'}, 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(string(parts[0]))
		if key == "host" {
			port := 80
			rawHost := strings.ToLower(string(bytes.TrimSpace(parts[1])))
			host, rawPort, err := net.SplitHostPort(rawHost)
			if err != nil {
				if addrError, ok := err.(*net.AddrError); ok && strings.Contains(addrError.Err, "missing port") {
					host = rawHost
				} else {
					return nil, err
				}
			} else if len(rawPort) > 0 {
				intPort, err := strconv.ParseUint(rawPort, 0, 16)
				if err != nil {
					return nil, err
				}
				port = int(intPort)
			}
			host = net.JoinHostPort(host, strconv.Itoa(port))
			return &host, nil
		}
	}
	return nil, ErrNoClue
}
