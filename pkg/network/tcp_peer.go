package network

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/nspcc-dev/neo-go/pkg/io"
	"github.com/nspcc-dev/neo-go/pkg/network/payload"
	"go.uber.org/zap"
)

type handShakeStage uint8

const (
	versionSent handShakeStage = 1 << iota
	versionReceived
	verAckSent
	verAckReceived

	requestQueueSize   = 32
	p2pMsgQueueSize    = 16
	hpRequestQueueSize = 4
)

var (
	errGone           = errors.New("the peer is gone already")
	errStateMismatch  = errors.New("tried to send protocol message before handshake completed")
	errPingPong       = errors.New("ping/pong timeout")
	errUnexpectedPong = errors.New("pong message wasn't expected")
)

// TCPPeer represents a connected remote node in the
// network over TCP.
type TCPPeer struct {
	// underlying TCP connection.
	conn net.Conn
	// The server this peer belongs to.
	server *Server
	// The version of the peer.
	version *payload.Version
	// Index of the last block.
	lastBlockIndex uint32

	lock      sync.RWMutex
	finale    sync.Once
	handShake handShakeStage

	done     chan struct{}
	sendQ    chan []byte
	p2pSendQ chan []byte
	hpSendQ  chan []byte

	wg sync.WaitGroup

	// number of sent pings.
	pingSent  int
	pingTimer *time.Timer
}

// NewTCPPeer returns a TCPPeer structure based on the given connection.
func NewTCPPeer(conn net.Conn, s *Server) *TCPPeer {
	return &TCPPeer{
		conn:     conn,
		server:   s,
		done:     make(chan struct{}),
		sendQ:    make(chan []byte, requestQueueSize),
		p2pSendQ: make(chan []byte, p2pMsgQueueSize),
		hpSendQ:  make(chan []byte, hpRequestQueueSize),
	}
}

// putPacketIntoQueue puts given message into the given queue if the peer has
// done handshaking.
func (p *TCPPeer) putPacketIntoQueue(queue chan<- []byte, msg []byte) error {
	if !p.Handshaked() {
		return errStateMismatch
	}
	select {
	case queue <- msg:
	case <-p.done:
		return errGone
	}
	return nil
}

// EnqueuePacket implements the Peer interface.
func (p *TCPPeer) EnqueuePacket(msg []byte) error {
	return p.putPacketIntoQueue(p.sendQ, msg)
}

// putMessageIntoQueue serializes given Message and puts it into given queue if
// the peer has done handshaking.
func (p *TCPPeer) putMsgIntoQueue(queue chan<- []byte, msg *Message) error {
	b, err := msg.Bytes()
	if err != nil {
		return err
	}
	return p.putPacketIntoQueue(queue, b)
}

// EnqueueMessage is a temporary wrapper that sends a message via
// EnqueuePacket if there is no error in serializing it.
func (p *TCPPeer) EnqueueMessage(msg *Message) error {
	return p.putMsgIntoQueue(p.sendQ, msg)
}

// EnqueueP2PPacket implements the Peer interface.
func (p *TCPPeer) EnqueueP2PPacket(msg []byte) error {
	return p.putPacketIntoQueue(p.p2pSendQ, msg)
}

// EnqueueP2PMessage implements the Peer interface.
func (p *TCPPeer) EnqueueP2PMessage(msg *Message) error {
	return p.putMsgIntoQueue(p.p2pSendQ, msg)
}

// EnqueueHPPacket implements the Peer interface. It the peer is not yet
// handshaked it's a noop.
func (p *TCPPeer) EnqueueHPPacket(msg []byte) error {
	return p.putPacketIntoQueue(p.hpSendQ, msg)
}

func (p *TCPPeer) writeMsg(msg *Message) error {
	b, err := msg.Bytes()
	if err != nil {
		return err
	}

	_, err = p.conn.Write(b)

	return err
}

// handleConn handles the read side of the connection, it should be started as
// a goroutine right after the new peer setup.
func (p *TCPPeer) handleConn() {
	var err error

	p.server.register <- p

	go p.handleQueues()
	// When a new peer is connected we send out our version immediately.
	err = p.SendVersion()
	if err == nil {
		r := io.NewBinReaderFromIO(p.conn)
		for {
			msg := &Message{}
			err = msg.Decode(r)

			if err == payload.ErrTooManyHeaders {
				p.server.log.Warn("not all headers were processed")
				r.Err = nil
			} else if err != nil {
				break
			}
			if err = p.server.handleMessage(p, msg); err != nil {
				if p.Handshaked() {
					err = fmt.Errorf("handling %s message: %v", msg.CommandType(), err)
				}
				break
			}
		}
	}
	p.Disconnect(err)
}

// handleQueues is a goroutine that is started automatically to handle
// send queues.
func (p *TCPPeer) handleQueues() {
	var err error
	// p2psend queue shares its time with send queue in around
	// ((p2pSkipDivisor - 1) * 2 + 1)/1 ratio, ratio because the third
	// select can still choose p2psend over send.
	var p2pSkipCounter uint32
	const p2pSkipDivisor = 4

	for {
		var msg []byte

		// This one is to give priority to the hp queue
		select {
		case <-p.done:
			return
		case msg = <-p.hpSendQ:
		default:
		}

		// Skip this select every p2pSkipDivisor iteration.
		if msg == nil && p2pSkipCounter%p2pSkipDivisor != 0 {
			// Then look at the p2p queue.
			select {
			case <-p.done:
				return
			case msg = <-p.hpSendQ:
			case msg = <-p.p2pSendQ:
			default:
			}
		}
		// If there is no message in HP or P2P queues, block until one
		// appears in any of the queues.
		if msg == nil {
			select {
			case <-p.done:
				return
			case msg = <-p.hpSendQ:
			case msg = <-p.p2pSendQ:
			case msg = <-p.sendQ:
			}
		}
		_, err = p.conn.Write(msg)
		if err != nil {
			break
		}
		p2pSkipCounter++
	}
	p.Disconnect(err)
}

// StartProtocol starts a long running background loop that interacts
// every ProtoTickInterval with the peer. It's only good to run after the
// handshake.
func (p *TCPPeer) StartProtocol() {
	var err error

	p.server.log.Info("started protocol",
		zap.Stringer("addr", p.RemoteAddr()),
		zap.ByteString("userAgent", p.Version().UserAgent),
		zap.Uint32("startHeight", p.Version().StartHeight),
		zap.Uint32("id", p.Version().Nonce))

	p.server.discovery.RegisterGoodAddr(p.PeerAddr().String())
	if p.server.chain.HeaderHeight() < p.LastBlockIndex() {
		err = p.server.requestHeaders(p)
		if err != nil {
			p.Disconnect(err)
			return
		}
	}

	timer := time.NewTimer(p.server.ProtoTickInterval)
	for {
		select {
		case <-p.done:
			return
		case <-timer.C:
			// Try to sync in headers and block with the peer if his block height is higher then ours.
			if p.LastBlockIndex() > p.server.chain.BlockHeight() {
				err = p.server.requestBlocks(p)
			}
			if err == nil {
				err = p.server.requestStateRoot(p)
			}
			if err == nil {
				timer.Reset(p.server.ProtoTickInterval)
			}
		}
		if err != nil {
			timer.Stop()
			p.Disconnect(err)
			return
		}
	}
}

// Handshaked returns status of the handshake, whether it's completed or not.
func (p *TCPPeer) Handshaked() bool {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return p.handShake == (verAckReceived | verAckSent | versionReceived | versionSent)
}

// SendVersion checks for the handshake state and sends a message to the peer.
func (p *TCPPeer) SendVersion() error {
	msg := p.server.getVersionMsg()
	p.lock.Lock()
	defer p.lock.Unlock()
	if p.handShake&versionSent != 0 {
		return errors.New("invalid handshake: already sent Version")
	}
	err := p.writeMsg(msg)
	if err == nil {
		p.handShake |= versionSent
	}
	return err
}

// HandleVersion checks for the handshake state and version message contents.
func (p *TCPPeer) HandleVersion(version *payload.Version) error {
	p.lock.Lock()
	defer p.lock.Unlock()
	if p.handShake&versionReceived != 0 {
		return errors.New("invalid handshake: already received Version")
	}
	p.version = version
	p.lastBlockIndex = version.StartHeight
	p.handShake |= versionReceived
	return nil
}

// SendVersionAck checks for the handshake state and sends a message to the peer.
func (p *TCPPeer) SendVersionAck(msg *Message) error {
	p.lock.Lock()
	defer p.lock.Unlock()
	if p.handShake&versionReceived == 0 {
		return errors.New("invalid handshake: tried to send VersionAck, but no version received yet")
	}
	if p.handShake&versionSent == 0 {
		return errors.New("invalid handshake: tried to send VersionAck, but didn't send Version yet")
	}
	if p.handShake&verAckSent != 0 {
		return errors.New("invalid handshake: already sent VersionAck")
	}
	err := p.writeMsg(msg)
	if err == nil {
		p.handShake |= verAckSent
	}
	return err
}

// HandleVersionAck checks handshake sequence correctness when VerAck message
// is received.
func (p *TCPPeer) HandleVersionAck() error {
	p.lock.Lock()
	defer p.lock.Unlock()
	if p.handShake&versionSent == 0 {
		return errors.New("invalid handshake: received VersionAck, but no version sent yet")
	}
	if p.handShake&versionReceived == 0 {
		return errors.New("invalid handshake: received VersionAck, but no version received yet")
	}
	if p.handShake&verAckReceived != 0 {
		return errors.New("invalid handshake: already received VersionAck")
	}
	p.handShake |= verAckReceived
	return nil
}

// RemoteAddr implements the Peer interface.
func (p *TCPPeer) RemoteAddr() net.Addr {
	return p.conn.RemoteAddr()
}

// PeerAddr implements the Peer interface.
func (p *TCPPeer) PeerAddr() net.Addr {
	remote := p.conn.RemoteAddr()
	// The network can be non-tcp in unit tests.
	if p.version == nil || remote.Network() != "tcp" {
		return p.RemoteAddr()
	}
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil {
		return p.RemoteAddr()
	}
	addrString := net.JoinHostPort(host, strconv.Itoa(int(p.version.Port)))
	tcpAddr, err := net.ResolveTCPAddr("tcp", addrString)
	if err != nil {
		return p.RemoteAddr()
	}
	return tcpAddr
}

// Disconnect will fill the peer's done channel with the given error.
func (p *TCPPeer) Disconnect(err error) {
	p.finale.Do(func() {
		close(p.done)
		p.conn.Close()
		p.server.unregister <- peerDrop{p, err}
	})
}

// Version implements the Peer interface.
func (p *TCPPeer) Version() *payload.Version {
	return p.version
}

// LastBlockIndex returns last block index.
func (p *TCPPeer) LastBlockIndex() uint32 {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return p.lastBlockIndex
}

// SendPing sends a ping message to the peer and does appropriate accounting of
// outstanding pings and timeouts.
func (p *TCPPeer) SendPing(msg *Message) error {
	if !p.Handshaked() {
		return errStateMismatch
	}
	p.lock.Lock()
	p.pingSent++
	if p.pingTimer == nil {
		p.pingTimer = time.AfterFunc(p.server.PingTimeout, func() {
			p.Disconnect(errPingPong)
		})
	}
	p.lock.Unlock()
	return p.EnqueueMessage(msg)
}

// HandlePong handles a pong message received from the peer and does appropriate
// accounting of outstanding pings and timeouts.
func (p *TCPPeer) HandlePong(pong *payload.Ping) error {
	p.lock.Lock()
	defer p.lock.Unlock()
	if p.pingTimer != nil && !p.pingTimer.Stop() {
		return errPingPong
	}
	p.pingTimer = nil
	p.pingSent--
	if p.pingSent < 0 {
		return errUnexpectedPong
	}
	p.lastBlockIndex = pong.LastBlockIndex
	return nil
}
