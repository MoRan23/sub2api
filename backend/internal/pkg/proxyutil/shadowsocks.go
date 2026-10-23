package proxyutil

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	shadowsocks "github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	M "github.com/sagernet/sing/common/metadata"
)

// ValidateShadowsocksCredentials never includes credentials in errors. SS2022
// keys must have the exact SIP022 size, without implicitly hashing longer keys.
func ValidateShadowsocksCredentials(method, password string) error {
	var keySize int
	switch method {
	case "aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "chacha20-ietf-poly1305":
		if password == "" {
			return errors.New("shadowsocks password is required")
		}
		return nil
	case "2022-blake3-aes-128-gcm":
		keySize = 16
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		keySize = 32
	default:
		return errors.New("unsupported Shadowsocks encryption method")
	}
	keys := strings.Split(password, ":")
	if len(keys) != 1 {
		return errors.New("shadowsocks 2022 identity key chains are not supported")
	}
	for _, encoded := range keys {
		key, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || len(key) != keySize || strings.ContainsAny(encoded, "\r\n") {
			return fmt.Errorf("shadowsocks 2022 requires base64 keys of exactly %d bytes", keySize)
		}
	}
	return nil
}

// NormalizeShadowsocksURL parses SIP002 userinfo and returns a normalized copy.
// Unsupported transport options are rejected rather than silently ignored.
// The optional fragment is only a display label, not part of the connection.
func NormalizeShadowsocksURL(proxyURL *url.URL) (*url.URL, error) {
	if proxyURL == nil || !strings.EqualFold(proxyURL.Scheme, "ss") {
		return nil, errors.New("invalid Shadowsocks proxy URL")
	}
	if proxyURL.Opaque != "" || proxyURL.OmitHost || proxyURL.RawQuery != "" || proxyURL.ForceQuery || (proxyURL.Path != "" && proxyURL.Path != "/") {
		return nil, errors.New("shadowsocks plugins, query parameters and custom paths are not supported")
	}
	if proxyURL.Hostname() == "" || strings.ContainsAny(proxyURL.Hostname(), " \t\r\n") {
		return nil, errors.New("shadowsocks proxy URL missing host")
	}
	port, err := strconv.Atoi(proxyURL.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("shadowsocks proxy requires a port between 1 and 65535")
	}
	if _, _, err := net.SplitHostPort(proxyURL.Host); err != nil {
		return nil, errors.New("invalid Shadowsocks proxy address")
	}
	if proxyURL.User == nil {
		return nil, errors.New("shadowsocks encryption method and password are required")
	}
	method := proxyURL.User.Username()
	password, plain := proxyURL.User.Password()
	if !plain {
		decoder := base64.RawURLEncoding.Strict()
		if strings.HasSuffix(method, "=") {
			decoder = base64.URLEncoding.Strict()
		}
		decoded, err := decoder.DecodeString(method)
		if err != nil {
			return nil, errors.New("invalid Shadowsocks base64 userinfo")
		}
		var found bool
		method, password, found = strings.Cut(string(decoded), ":")
		if !found {
			return nil, errors.New("invalid Shadowsocks userinfo")
		}
		if strings.HasPrefix(method, "2022-") {
			return nil, errors.New("shadowsocks 2022 userinfo must not be base64 encoded")
		}
	}
	if err := ValidateShadowsocksCredentials(method, password); err != nil {
		return nil, err
	}
	result := *proxyURL
	result.Scheme = "ss"
	result.User = url.UserPassword(method, password)
	result.Path, result.RawPath, result.Fragment, result.RawFragment = "", "", "", ""
	return &result, nil
}

// NewShadowsocksDialer creates a reusable native TCP dialer. Target hostnames
// travel in the encrypted request; only the proxy hostname is resolved locally.
// The timeout covers TCP connection and the initial encrypted request write.
// Returned connections belong to their caller (including the HTTP idle pool),
// and are no longer tied to the initiating request's context.
func NewShadowsocksDialer(proxyURL *url.URL) (func(context.Context, string, string) (net.Conn, error), error) {
	parsed, err := NormalizeShadowsocksURL(proxyURL)
	if err != nil {
		return nil, err
	}
	password, _ := parsed.User.Password()
	var method shadowsocks.Method
	if strings.HasPrefix(parsed.User.Username(), "2022-") {
		method, err = shadowaead_2022.NewWithPassword(parsed.User.Username(), password, nil)
	} else {
		method, err = shadowaead.New(parsed.User.Username(), nil, password)
	}
	if err != nil {
		return nil, errors.New("failed to initialize Shadowsocks encryption")
	}
	proxyAddress := parsed.Host
	forward := &net.Dialer{Timeout: socks5DialTimeout, KeepAlive: socks5DialKeepAlive}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, errors.New("shadowsocks proxy only supports TCP")
		}
		host, portString, err := net.SplitHostPort(address)
		port, portErr := strconv.ParseUint(portString, 10, 16)
		if err != nil || portErr != nil || port == 0 || host == "" || len(host) > 255 {
			return nil, errors.New("invalid Shadowsocks destination")
		}
		destination := M.ParseSocksaddrHostPort(host, uint16(port))
		if !destination.IsValid() || destination.Addr.Zone() != "" {
			return nil, errors.New("invalid Shadowsocks destination")
		}
		dialCtx, cancel := context.WithTimeout(ctx, socks5DialTimeout)
		defer cancel()
		// tcp4/tcp6 describe the target, not the proxy's address family.
		raw, err := forward.DialContext(dialCtx, "tcp", proxyAddress)
		if err != nil {
			return nil, fmt.Errorf("connect Shadowsocks proxy: %w", err)
		}
		return finishShadowsocksHandshake(dialCtx, raw, method, destination)
	}, nil
}

func finishShadowsocksHandshake(ctx context.Context, raw net.Conn, method shadowsocks.Method, destination M.Socksaddr) (net.Conn, error) {
	deadline, _ := ctx.Deadline()
	if err := raw.SetDeadline(deadline); err != nil {
		_ = raw.Close()
		return nil, err
	}
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = raw.Close()
		close(closed)
	})
	conn, err := method.DialConn(raw, destination)
	if !stop() {
		<-closed
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("shadowsocks handshake: %w", err)
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}
