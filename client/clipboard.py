"""Copy text to the Windows clipboard.

Uses the Win32 API through ctypes rather than tkinter: tkinter is deliberately excluded
from the frozen build (it is large and otherwise unused), and pulling it back in just to
copy a short string would add megabytes to what people download.

Fails quietly and reports it — a clipboard that does not work should never take the
game down with it.
"""

from __future__ import annotations

import ctypes
import sys

CF_UNICODETEXT = 13
GMEM_MOVEABLE = 0x0002


def copy(text: str) -> bool:
    """Put text on the clipboard. Returns whether it worked."""
    if sys.platform != "win32":
        return False

    try:
        user32 = ctypes.windll.user32
        kernel32 = ctypes.windll.kernel32

        if not user32.OpenClipboard(None):
            return False
        try:
            user32.EmptyClipboard()

            # Clipboard memory must be a moveable global that the system takes
            # ownership of; the buffer includes the terminating null.
            buf = ctypes.create_unicode_buffer(text)
            size = ctypes.sizeof(buf)

            handle = kernel32.GlobalAlloc(GMEM_MOVEABLE, size)
            if not handle:
                return False

            locked = kernel32.GlobalLock(handle)
            if not locked:
                kernel32.GlobalFree(handle)
                return False
            try:
                ctypes.memmove(locked, buf, size)
            finally:
                kernel32.GlobalUnlock(handle)

            if not user32.SetClipboardData(CF_UNICODETEXT, handle):
                kernel32.GlobalFree(handle)
                return False

            # On success the system owns the handle; freeing it here would be a
            # double free.
            return True
        finally:
            user32.CloseClipboard()

    except Exception:
        return False
