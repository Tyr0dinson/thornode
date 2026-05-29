package p2p

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/transport"
	tptu "github.com/libp2p/go-libp2p-transport-upgrader"
	maddr "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
	"golang.org/x/net/proxy"
)

var _ transport.Transport = (*onionTransport)(nil)
var _ manet.Conn = (*onionMultiaddrConn)(nil)

type onionTransport struct {
	dialer   proxy.Dialer
	upgrader *tptu.Upgrader
}

func newOnionTransport(socksAddr string, upgrader *tptu.Upgrader) (*onionTransport, error) {
	if upgrader == nil {
		return nil, errors.New("nil upgrader")
	}
	if _, _, err := net.SplitHostPort(socksAddr); err != nil {
		return nil, fmt.Errorf("invalid tor socks address: %w", err)
	}
	dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("fail to create SOCKS5 dialer: %w", err)
	}
	return &onionTransport{
		dialer:   dialer,
		upgrader: upgrader,
	}, nil
}

func (t *onionTransport) Dial(ctx context.Context, raddr maddr.Multiaddr, p peer.ID) (transport.CapableConn, error) {
	target, err := onionDialTarget(raddr)
	if err != nil {
		return nil, err
	}

	conn, err := t.dialContext(ctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	maConn, err := newOnionMultiaddrConn(conn, raddr)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return t.upgrader.UpgradeOutbound(ctx, t, maConn, p)
}

func (t *onionTransport) CanDial(addr maddr.Multiaddr) bool {
	_, err := onionDialTarget(addr)
	return err == nil
}

func (t *onionTransport) Listen(maddr.Multiaddr) (transport.Listener, error) {
	return nil, errors.New("onion transport does not listen; configure Tor HiddenServicePort for inbound connections")
}

func (t *onionTransport) Protocols() []int {
	return []int{maddr.P_ONION3}
}

func (t *onionTransport) Proxy() bool {
	return true
}

func (t *onionTransport) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if dialer, ok := t.dialer.(proxy.ContextDialer); ok {
		return dialer.DialContext(ctx, network, address)
	}

	type dialResult struct {
		conn net.Conn
		err  error
	}
	done := make(chan dialResult, 1)
	go func() {
		conn, err := t.dialer.Dial(network, address)
		done <- dialResult{conn: conn, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-done:
		if result.conn != nil && ctx.Err() != nil {
			_ = result.conn.Close()
			return nil, ctx.Err()
		}
		return result.conn, result.err
	}
}

func onionDialTarget(addr maddr.Multiaddr) (string, error) {
	onionAddr, err := addr.ValueForProtocol(maddr.P_ONION3)
	if err != nil || onionAddr == "" {
		return "", errors.New("multiaddr does not contain onion3 protocol")
	}

	parts := strings.Split(onionAddr, ":")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid onion3 address %q", onionAddr)
	}
	label, port := parts[0], parts[1]
	if !isOnion3Label(label) {
		return "", fmt.Errorf("invalid onion3 label %q", label)
	}
	portNum, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNum == 0 {
		return "", fmt.Errorf("invalid onion3 port %q", port)
	}

	return net.JoinHostPort(label+".onion", port), nil
}

func isOnion3Label(label string) bool {
	if len(label) != 56 {
		return false
	}
	for _, c := range label {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

type onionMultiaddrConn struct {
	net.Conn
	local  maddr.Multiaddr
	remote maddr.Multiaddr
}

func newOnionMultiaddrConn(conn net.Conn, remote maddr.Multiaddr) (manet.Conn, error) {
	local, err := manet.FromNetAddr(conn.LocalAddr())
	if err != nil {
		return nil, fmt.Errorf("fail to convert local address: %w", err)
	}
	return &onionMultiaddrConn{
		Conn:   conn,
		local:  local,
		remote: remote,
	}, nil
}

func (c *onionMultiaddrConn) LocalMultiaddr() maddr.Multiaddr {
	return c.local
}

func (c *onionMultiaddrConn) RemoteMultiaddr() maddr.Multiaddr {
	return c.remote
}
