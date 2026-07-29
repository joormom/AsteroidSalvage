"""WebSocket client for the game server.

Runs the socket on a background thread so Panda3D's task loop never blocks on network
I/O. Incoming messages are parked in small queues that the render thread drains once per
frame; nothing else crosses the thread boundary.
"""

from __future__ import annotations

import os
import queue
import sys
import threading
import time

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402

from websockets.sync.client import connect  # noqa: E402
from websockets.exceptions import ConnectionClosed  # noqa: E402


class NetClient:
    """Threaded protocol client.

    All public methods are safe to call from the render thread.
    """

    def __init__(self, url: str, name: str, team_pref: int = 0xFF) -> None:
        self.url = url
        self.name = name
        self.team_pref = team_pref

        self._conn = None
        self._thread: threading.Thread | None = None
        self._send_lock = threading.Lock()
        self._stop = threading.Event()

        # Snapshots are coalesced by the interpolator, so a short queue is fine; if the
        # render thread stalls we would rather drop stale world states than grow a
        # backlog and play the past.
        self._snapshots: queue.Queue[proto.Snapshot] = queue.Queue(maxsize=8)
        self._events: queue.Queue[proto.Event] = queue.Queue(maxsize=256)
        # Shots are pure VFX with a lifetime of a few frames, so a backlog is worse than
        # a drop — an old beam drawn late points at nothing.
        self._shots: queue.Queue[proto.Shot] = queue.Queue(maxsize=128)

        self._welcome: proto.Welcome | None = None
        self._teams: list[proto.TeamState] = []
        self._match: proto.MatchState | None = None
        self._player: proto.PlayerState | None = None
        self._offers: list[proto.ShopOffer] = []
        self._state_lock = threading.Lock()

        self.connected = threading.Event()
        self.error: str | None = None
        self._seq = 0

    # --- lifecycle ---------------------------------------------------------

    def start(self) -> None:
        self._thread = threading.Thread(target=self._run, name="netclient", daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        with self._send_lock:
            if self._conn is not None:
                try:
                    self._conn.close()
                except Exception:
                    pass

    def wait_until_ready(self, timeout: float = 5.0) -> bool:
        """Block until the server's Welcome arrives."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.welcome is not None:
                return True
            if self.error is not None:
                return False
            time.sleep(0.01)
        return False

    # --- background thread -------------------------------------------------

    def _run(self) -> None:
        try:
            # ping_interval=None disables *our* keepalive.
            #
            # sprocket's gws transport has an empty OnPing handler
            # (transports/gws/gws.go:73), so the server never answers a client ping.
            # The websockets default (ping every 20 s, give up after 20 s without a
            # pong) therefore tore the connection down after exactly 40 seconds of
            # perfectly healthy play.
            #
            # Liveness is still covered: the server pings every 54 s and drops clients
            # that do not pong within 60 s, and websockets answers those pings
            # automatically. A server that dies outright still closes the socket, which
            # recv reports normally.
            with connect(self.url, max_size=1 << 20, ping_interval=None) as conn:
                self._conn = conn
                self.connected.set()
                conn.send(proto.encode_hello(self.name, self.team_pref))

                while not self._stop.is_set():
                    try:
                        data = conn.recv(timeout=1.0)
                    except TimeoutError:
                        continue
                    if isinstance(data, str):
                        continue  # protocol is binary-only
                    self._dispatch(data)

        except ConnectionClosed as exc:
            if not self._stop.is_set():
                # Report the close code and reason rather than assuming the server hung
                # up: a keepalive timeout closed from *this* side for 40 seconds and the
                # blanket "closed by server" message sent the search in the wrong
                # direction entirely.
                self.error = f"connection closed ({exc})"
        except Exception as exc:  # noqa: BLE001 - surfaced to the user in the HUD
            self.error = f"{type(exc).__name__}: {exc}"
        finally:
            self._conn = None

    def _dispatch(self, data: bytes) -> None:
        if not data:
            return
        tag, body = data[0], data[1:]

        if tag == proto.MSG_SNAPSHOT:
            try:
                snap = proto.decode_snapshot(body)
            except (ValueError, Exception):
                return
            # Drop the oldest rather than block the socket thread.
            if self._snapshots.full():
                try:
                    self._snapshots.get_nowait()
                except queue.Empty:
                    pass
            self._snapshots.put_nowait(snap)

        elif tag == proto.MSG_WELCOME:
            with self._state_lock:
                self._welcome = proto.decode_welcome(body)

        elif tag == proto.MSG_EVENT:
            if not self._events.full():
                self._events.put_nowait(proto.decode_event(body))

        elif tag == proto.MSG_TEAM_STATE:
            with self._state_lock:
                self._teams = proto.decode_team_state(body)
                # Refresh the shared team-to-colour mapping here, at the one point every
                # client learns it. Hulls, beams, station paint and the end-of-match clip
                # all read through proto.team_color, so none of them need to know that a
                # crew changed colour.
                proto.set_team_palette(self._teams)

        elif tag == proto.MSG_MATCH_STATE:
            with self._state_lock:
                self._match = proto.decode_match_state(body)

        elif tag == proto.MSG_PLAYER_STATE:
            with self._state_lock:
                self._player = proto.decode_player_state(body)

        elif tag == proto.MSG_SHOP_OFFERS:
            with self._state_lock:
                self._offers = proto.decode_shop_offers(body)

        elif tag == proto.MSG_SHOTS:
            for shot in proto.decode_shots(body):
                if self._shots.full():
                    try:
                        self._shots.get_nowait()
                    except queue.Empty:
                        pass
                self._shots.put_nowait(shot)

    # --- render-thread API -------------------------------------------------

    @property
    def welcome(self) -> proto.Welcome | None:
        with self._state_lock:
            return self._welcome

    @property
    def teams(self) -> list[proto.TeamState]:
        with self._state_lock:
            return list(self._teams)

    @property
    def match(self) -> proto.MatchState | None:
        with self._state_lock:
            return self._match

    @property
    def player(self) -> proto.PlayerState | None:
        with self._state_lock:
            return self._player

    @property
    def offers(self) -> list[proto.ShopOffer]:
        with self._state_lock:
            return list(self._offers)

    def _send(self, payload: bytes) -> None:
        conn = self._conn
        if conn is None:
            return
        try:
            with self._send_lock:
                conn.send(payload)
        except Exception:
            pass  # the reader thread owns error reporting

    def buy_upgrade(self, upgrade_id: int) -> None:
        """Request a purchase. The server rejects anything outside the intermission."""
        self._send(proto.encode_buy_upgrade(upgrade_id))

    def buy_offer(self, slot: int) -> None:
        """Buy one of this intermission's randomised offers, by slot."""
        self._send(proto.encode_buy_offer(slot))

    def set_team_name(self, name: str) -> None:
        self._send(proto.encode_set_team_name(name))

    def set_team_color(self, color: int) -> None:
        self._send(proto.encode_set_team_color(color))

    def take_snapshots(self) -> list[proto.Snapshot]:
        out = []
        while True:
            try:
                out.append(self._snapshots.get_nowait())
            except queue.Empty:
                return out

    def take_events(self) -> list[proto.Event]:
        out = []
        while True:
            try:
                out.append(self._events.get_nowait())
            except queue.Empty:
                return out

    def take_shots(self) -> list[proto.Shot]:
        out = []
        while True:
            try:
                out.append(self._shots.get_nowait())
            except queue.Empty:
                return out

    def send_input(self, **kwargs) -> None:
        """Send one input frame. Silently drops if the socket is down."""
        conn = self._conn
        if conn is None:
            return
        self._seq += 1
        payload = proto.encode_input(self._seq, **kwargs)
        try:
            with self._send_lock:
                conn.send(payload)
        except Exception:
            pass  # the reader thread owns error reporting
