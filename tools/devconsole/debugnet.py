"""Debug-channel client for the dev console.

Connects to /debug (dev builds only) and carries DebugSet up / DebugStats down. Kept
separate from client/net.py because the console is a different kind of client: it never
joins the match, never sends input, and must keep working while nobody is playing.
"""

from __future__ import annotations

import os
import sys
import threading
import time
from collections import deque

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "shared"))

import asteroid_protocol as proto  # noqa: E402

from websockets.sync.client import connect  # noqa: E402


class DebugClient:
    """Threaded connection to the server's debug channel, with auto-reconnect.

    The console is a tool, not a game client: the server restarts constantly during
    tuning, and having to restart the console every time would defeat its purpose.
    """

    def __init__(self, url: str, history: int = 240) -> None:
        self.url = url
        self._conn = None
        self._send_lock = threading.Lock()
        self._stop = threading.Event()

        self.connected = False
        self.error: str | None = None
        self.attempts = 0

        self.tick_ms_history: deque[float] = deque(maxlen=history)
        self.latest: proto.DebugStats | None = None

    def start(self) -> None:
        threading.Thread(target=self._run, name="debugnet", daemon=True).start()

    def stop(self) -> None:
        self._stop.set()
        with self._send_lock:
            if self._conn is not None:
                try:
                    self._conn.close()
                except Exception:
                    pass

    def _run(self) -> None:
        while not self._stop.is_set():
            self.attempts += 1
            try:
                # ping_interval=None for the same reason as the game client: the server
                # never answers client pings, so our keepalive would kill a healthy
                # connection after 40 seconds. See client/net.py.
                with connect(self.url, max_size=1 << 20, ping_interval=None) as conn:
                    self._conn = conn
                    self.connected = True
                    self.error = None

                    while not self._stop.is_set():
                        try:
                            data = conn.recv(timeout=1.0)
                        except TimeoutError:
                            continue
                        if isinstance(data, (bytes, bytearray)) and data:
                            self._dispatch(bytes(data))

            except Exception as exc:  # noqa: BLE001 - shown in the console UI
                self.error = f"{type(exc).__name__}: {exc}"
            finally:
                self.connected = False
                self._conn = None

            if not self._stop.is_set():
                time.sleep(1.0)

    def _dispatch(self, data: bytes) -> None:
        if data[0] != proto.MSG_DEBUG_STATS:
            return
        stats = proto.decode_debug_stats(data[1:])
        self.latest = stats
        self.tick_ms_history.append(stats.tick_ms)

    def set_param(self, param_id: int, value: float) -> bool:
        conn = self._conn
        if conn is None:
            return False
        try:
            with self._send_lock:
                conn.send(proto.encode_debug_set(param_id, value))
            return True
        except Exception:
            return False
