// Copyright 2025 Tobias Hintze
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package reverse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/paraopsde/go-x/pkg/util"
	"go.uber.org/zap"
)

const udpBufferSize = 1500

type Proxy struct {
	UpstreamEndpoint string
	ListenAddress    string

	muUpstreamContext sync.Mutex
	upstreamContext   context.Context
	upstreamCancel    context.CancelFunc

	muUpstreamAddr sync.Mutex
	upstreamAddr   *net.UDPAddr

	muDownstreamSock sync.Mutex
	downstreamSock   *net.UDPConn

	muDownstream sync.Mutex
	byDownstream map[AddrKey]*ProxyConn
}

type ProxyConn struct {
	DownstreamAddress   *net.UDPAddr
	Upstream            *net.UDPConn
	DownstreamSendQueue chan Packet
}

type Packet struct {
	Addr *net.UDPAddr
	Data []byte
}

type AddrKey struct {
	IP   [4]byte
	Port int
}

var (
	upstreamWatchdogTimeout = 60 * time.Second
	upstreamResolveInterval = 60 * time.Second

	errContextDone     = errors.New("context done")
	errWatchdogTimeout = errors.New("watchdog timeout")
	errShortWrite      = errors.New("short write")
)

func KeyFromAddr(addr *net.UDPAddr) AddrKey {
	ak := AddrKey{
		Port: addr.Port,
	}
	copy(ak.IP[:], addr.IP)
	return ak
}

type ProxyOpts struct {
	UpstreamEndpoint string
	ListenAddress    string
}

func NewProxy(opts ProxyOpts) *Proxy {
	return &Proxy{
		UpstreamEndpoint: opts.UpstreamEndpoint,
		ListenAddress:    opts.ListenAddress,
		byDownstream:     make(map[AddrKey]*ProxyConn),
	}
}

func (p *Proxy) Run(ctx context.Context) error {
	log := util.CtxLogOrPanic(ctx)

	p.setUpstreamContext(ctx)

	errs := make(chan error)
	downstreamSendQueue := make(chan Packet, 16)
	go func() {
		err := p.processDownstreamSendQueue(ctx, downstreamSendQueue)
		log.Warn("failure processing downstream send queue", zap.Error(err))
		errs <- err
	}()

	go func() {
		for {
			err := p.listenAndServeDownstream(ctx, downstreamSendQueue)
			log.Warn("listener failed. restarting...", zap.Error(err))
			time.Sleep(1 * time.Second)
		}
	}()

	// re-resolve upstream endpoint
	go func() {
		for {
			err := p.resolveUpstream(ctx)
			if err != nil {
				log.Warn("failed to resolve upstream address", zap.String("addr", p.UpstreamEndpoint), zap.Error(err))
			}
			time.Sleep(upstreamResolveInterval)
		}
	}()

	for err := range errs {
		p.cancelUpstreams()
		return fmt.Errorf("downstream error: %w", err)
	}

	return nil
}

func (p *Proxy) listen(ctx context.Context) (*net.UDPConn, error) {
	log := util.CtxLogOrPanic(ctx)
	srvAddr, err := net.ResolveUDPAddr("udp", p.ListenAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve UDP listening address '%s': %w", p.ListenAddress, err)
	}

	sock, err := net.ListenUDP("udp", srvAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP downstream socket '%s': %w", p.ListenAddress, err)
	}
	log.Info("started listening", zap.String("listen-addr", p.ListenAddress))
	return sock, nil
}

func (p *Proxy) resolveUpstream(ctx context.Context) error {
	log := util.CtxLogOrPanic(ctx)
	upstreamAddr, err := net.ResolveUDPAddr("udp", p.UpstreamEndpoint)
	if err != nil {
		return fmt.Errorf("failed to resolve upstream UDP address: %w", err)
	}
	p.muUpstreamAddr.Lock()
	defer p.muUpstreamAddr.Unlock()
	if p.upstreamAddr != nil && p.upstreamAddr.String() != upstreamAddr.String() {
		log.Info("upstream address updated",
			zap.String("old", p.upstreamAddr.String()),
			zap.String("new", upstreamAddr.String()))
		p.setUpstreamContext(ctx)
	}
	p.upstreamAddr = upstreamAddr
	return nil
}

func (p *Proxy) processDownstreamSendQueue(ctx context.Context, sendQueue chan Packet) error {
	log := util.CtxLogOrPanic(ctx)

	sock := p.getDownstreamSock()
	for sock == nil {
		time.Sleep(1 * time.Second)
		sock = p.getDownstreamSock()
	}
	refreshSockTicker := time.NewTicker(5 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return errContextDone
		case <-refreshSockTicker.C:
			sock = p.getDownstreamSock()
		case packet, ok := <-sendQueue:
			if !ok {
				return errors.New("send queue closed")
			}
			written, err := sock.WriteToUDP(packet.Data, packet.Addr)
			if err != nil {
				log.Info("failed to send packet to downstream", zap.Error(err))
				time.Sleep(1 * time.Second)
				continue
			}
			if written != len(packet.Data) {
				log.Warn("short response write to downstream",
					zap.Int("written", written),
					zap.Int("expected", len(packet.Data)))
				continue
			}
			log.Debug("packet sent to downstream", zap.String("addr", packet.Addr.String()), zap.Int("len", written))
		}
	}
}

func (p *Proxy) listenAndServeDownstream(ctx context.Context, downstreamSendQueue chan Packet) error {
	log := util.CtxLogOrPanic(ctx)

	downstreamSock, err := p.listen(ctx)
	if err != nil {
		return fmt.Errorf("failed to listen on UDP port: %w", err)
	}
	defer downstreamSock.Close()
	p.setDownstreamSock(downstreamSock)

	buf := make([]byte, udpBufferSize)
	for {
		n, addr, err := downstreamSock.ReadFromUDP(buf)
		if err != nil {
			return fmt.Errorf("failed to read from udp: %w", err)
		}

		downstreamKey := KeyFromAddr(addr)
		proxyConn, ok := p.getProxyConn(downstreamKey)
		if !ok {
			upstream, errDial := net.DialUDP("udp", nil, p.getUpstreamAddr())
			if errDial != nil {
				return fmt.Errorf("failed to dial upstream: %w", errDial)
			}
			log.Info("dialed new upstream connection",
				zap.Int("downstream-conn-count", p.proxyConnCount()),
				zap.Any("upstream-addr", p.upstreamAddr),
				zap.Any("upstream-local", upstream.LocalAddr()),
				zap.Any("downstream", downstreamKey))
			proxyConn = &ProxyConn{
				Upstream:            upstream,
				DownstreamAddress:   addr,
				DownstreamSendQueue: downstreamSendQueue,
			}
			p.setProxyConn(downstreamKey, proxyConn)
			//nolint:contextcheck
			go func(ctx context.Context) {
				errUpstream := proxyConn.readUpstream(ctx)
				p.deleteProxyConn(downstreamKey)
				log.Info("upstream connection closed",
					zap.Error(errUpstream),
					zap.Int("downstream-conn-count", p.proxyConnCount()))
			}(p.getUpstreamContext())
		}

		log.Debug("received packet from downstream", zap.String("addr", addr.String()), zap.Int("len", n))

		var written int
		written, err = proxyConn.Upstream.Write(buf[:n])
		if err != nil {
			return fmt.Errorf("failed to write to upstream: %w", err)
		}
		if written != n {
			return errShortWrite
		}

		log.Debug("forwarded packet to upstream",
			zap.String("upstream-remote-addr", proxyConn.Upstream.RemoteAddr().String()),
			zap.String("upstream-local-addr", proxyConn.Upstream.LocalAddr().String()),
			zap.Int("len", written))

	}
}

func (p *ProxyConn) readUpstream(ctx context.Context) error {
	log := util.CtxLogOrPanic(ctx)

	watchdog := time.NewTimer(upstreamWatchdogTimeout)

	buf := make([]byte, udpBufferSize)
	for {
		select {
		case <-ctx.Done():
			return errContextDone
		case <-watchdog.C:
			return errWatchdogTimeout
		default:
		}
		if err := p.Upstream.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			return fmt.Errorf("failed to set read deadline: %w", err)
		}
		n, addr, err := p.Upstream.ReadFromUDP(buf)
		if n > 0 {
			log.Debug("received packet from upstream", zap.String("addr", addr.String()), zap.Int("len", n))
			watchdog.Reset(upstreamWatchdogTimeout)
			packet := Packet{
				Addr: p.DownstreamAddress,
				Data: buf[:n],
			}
			select {
			case p.DownstreamSendQueue <- packet:
			case <-ctx.Done():
				return fmt.Errorf("%w: downstream send queue", errContextDone)
			case <-watchdog.C:
				return fmt.Errorf("%w downstream send queue", errWatchdogTimeout)
			}
		}

		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			log.Debug("read timeout from upstream")
			continue
		}

		if err != nil {
			log.Error("failed to read from UDP socket", zap.Error(err))
			return err
		}
	}
}
func (p *Proxy) setDownstreamSock(sock *net.UDPConn) {
	p.muDownstreamSock.Lock()
	defer p.muDownstreamSock.Unlock()
	p.downstreamSock = sock
}
func (p *Proxy) getDownstreamSock() *net.UDPConn {
	p.muDownstreamSock.Lock()
	defer p.muDownstreamSock.Unlock()
	return p.downstreamSock
}

// replace "upstream context". the context is shared among all
// proxy connections. when the context is canceled, all proxy
// connections will be closed.
// this is useful for when the upstream address changes.
func (p *Proxy) setUpstreamContext(ctx context.Context) {
	log := util.CtxLogOrPanic(ctx)

	p.muUpstreamContext.Lock()
	defer p.muUpstreamContext.Unlock()

	newCtx, cancel := context.WithCancel(ctx)
	newCtx = util.CtxWithLog(newCtx, log)

	oldCancel := p.upstreamCancel
	p.upstreamContext = newCtx
	p.upstreamCancel = cancel

	if oldCancel != nil {
		oldCancel()
	}
}
func (p *Proxy) cancelUpstreams() {
	p.muUpstreamContext.Lock()
	defer p.muUpstreamContext.Unlock()
	if p.upstreamCancel != nil {
		p.upstreamCancel()
	}
}
func (p *Proxy) getUpstreamContext() context.Context {
	p.muUpstreamContext.Lock()
	defer p.muUpstreamContext.Unlock()
	return p.upstreamContext
}

func (p *Proxy) proxyConnCount() int {
	p.muDownstream.Lock()
	defer p.muDownstream.Unlock()
	return len(p.byDownstream)
}
func (p *Proxy) setProxyConn(downstreamKey AddrKey, proxyConn *ProxyConn) {
	p.muDownstream.Lock()
	defer p.muDownstream.Unlock()
	p.byDownstream[downstreamKey] = proxyConn
}

func (p *Proxy) getProxyConn(downstreamKey AddrKey) (*ProxyConn, bool) {
	p.muDownstream.Lock()
	defer p.muDownstream.Unlock()
	proxyConn, ok := p.byDownstream[downstreamKey]
	return proxyConn, ok
}

func (p *Proxy) deleteProxyConn(downstreamKey AddrKey) {
	p.muDownstream.Lock()
	defer p.muDownstream.Unlock()
	delete(p.byDownstream, downstreamKey)
}

func (p *Proxy) getUpstreamAddr() *net.UDPAddr {
	p.muUpstreamAddr.Lock()
	defer p.muUpstreamAddr.Unlock()
	return p.upstreamAddr
}
