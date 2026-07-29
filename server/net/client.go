package net

import (
	"sync"

	"asteroidsalvage/sim"

	"github.com/lxzan/gws"
)

// Client is a headless protocol client. It backs the load-test bots and the netcode
// tests; the Python client implements the same protocol independently.
//
// Callbacks fire on the read goroutine, so they must not block.
type Client struct {
	conn *gws.Conn

	mu       sync.Mutex
	welcome  Welcome
	haveWelc bool

	OnWelcome    func(Welcome)
	OnSnapshot   func(Snapshot)
	OnEvent      func(sim.Event)
	OnTeamState  func([]sim.Team)
	OnDebugStats func(DebugStats)
	// OnDisconnect is not named OnClose because that identifier is taken by the
	// gws.Event method below.
	OnDisconnect func(error)

	snapReuse []SnapshotBody
}

// Dial connects to a server. url is e.g. ws://localhost:8080/ws (or /debug for the
// dev console). The caller must start ReadLoop.
func Dial(url string) (*Client, error) {
	c := &Client{snapReuse: make([]SnapshotBody, 0, 2048)}

	conn, _, err := gws.NewClient(c, &gws.ClientOption{
		Addr:               url,
		ReadMaxPayloadSize: 1 << 20,
	})
	if err != nil {
		return nil, err
	}
	c.conn = conn
	return c, nil
}

// ReadLoop pumps incoming messages until the connection closes. Run it in a goroutine.
func (c *Client) ReadLoop() { c.conn.ReadLoop() }

// Close shuts the connection down.
func (c *Client) Close() error { return c.conn.WriteClose(1000, nil) }

// Welcome returns the server's Welcome once received.
func (c *Client) Welcome() (Welcome, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.welcome, c.haveWelc
}

// SendHello introduces the client. teamPref 0xFF means auto-assign.
func (c *Client) SendHello(name string, teamPref uint8) error {
	return c.send(EncodeHello(name, teamPref))
}

// SendInput sends one input frame.
func (c *Client) SendInput(in sim.Input) error { return c.send(EncodeInput(in)) }

// SendDebugSet retunes a parameter. Only honoured on a /debug connection in dev mode.
func (c *Client) SendDebugSet(param uint16, value float32) error {
	return c.send(EncodeDebugSet(param, value))
}

func (c *Client) send(b []byte) error {
	return c.conn.WriteMessage(gws.OpcodeBinary, b)
}

/*============================================================================
 * gws.Event
 *===========================================================================*/

func (c *Client) OnOpen(*gws.Conn)             {}
func (c *Client) OnPing(s *gws.Conn, p []byte) { _ = s.WritePong(p) }
func (c *Client) OnPong(*gws.Conn, []byte)     {}

func (c *Client) OnClose(_ *gws.Conn, err error) {
	if c.OnDisconnect != nil {
		c.OnDisconnect(err)
	}
}

func (c *Client) OnMessage(_ *gws.Conn, msg *gws.Message) {
	defer msg.Close()

	data := msg.Bytes()
	if len(data) < 1 {
		return
	}
	tag, body := data[0], data[1:]

	switch tag {
	case MsgWelcome:
		w, err := DecodeWelcome(body)
		if err != nil {
			return
		}
		c.mu.Lock()
		c.welcome, c.haveWelc = w, true
		c.mu.Unlock()
		if c.OnWelcome != nil {
			c.OnWelcome(w)
		}

	case MsgSnapshot:
		if c.OnSnapshot == nil {
			return
		}
		snap, err := DecodeSnapshot(body, c.snapReuse)
		if err != nil {
			return
		}
		c.snapReuse = snap.Bodies
		c.OnSnapshot(snap)

	case MsgEvent:
		if c.OnEvent == nil {
			return
		}
		if e, err := DecodeEvent(body); err == nil {
			c.OnEvent(e)
		}

	case MsgTeamState:
		if c.OnTeamState == nil {
			return
		}
		if teams, err := DecodeTeamState(body); err == nil {
			c.OnTeamState(teams)
		}

	case MsgDebugStats:
		if c.OnDebugStats == nil {
			return
		}
		if st, err := DecodeDebugStats(body); err == nil {
			c.OnDebugStats(st)
		}
	}
}
