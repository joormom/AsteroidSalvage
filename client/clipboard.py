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
from ctypes import c_size_t, c_uint, c_void_p

CF_UNICODETEXT = 13
GMEM_MOVEABLE = 0x0002

_bound = False


def _bind(user32, kernel32) -> None:
    """Declare the handle-returning calls as pointer-width.

    ctypes assumes a C `int` return for anything it is not told about, so on 64-bit
    Windows the HANDLE from GlobalAlloc and the LPVOID from GlobalLock came back
    truncated to their low 32 bits. Copying then wrote through a bogus pointer and
    SetClipboardData was handed a handle the system did not recognise, so the copy
    silently did nothing — the failure is invisible without these declarations, which is
    what made it look like the button was not wired up at all.
    """
    global _bound
    if _bound:
        return

    kernel32.GlobalAlloc.restype = c_void_p
    kernel32.GlobalAlloc.argtypes = [c_uint, c_size_t]
    kernel32.GlobalLock.restype = c_void_p
    kernel32.GlobalLock.argtypes = [c_void_p]
    kernel32.GlobalUnlock.argtypes = [c_void_p]
    kernel32.GlobalFree.restype = c_void_p
    kernel32.GlobalFree.argtypes = [c_void_p]

    user32.OpenClipboard.argtypes = [c_void_p]
    user32.SetClipboardData.restype = c_void_p
    user32.SetClipboardData.argtypes = [c_uint, c_void_p]

    _bound = True


def copy(text: str) -> bool:
    """Put text on the clipboard. Returns whether it worked."""
    if sys.platform != "win32":
        return False

    try:
        user32 = ctypes.windll.user32
        kernel32 = ctypes.windll.kernel32
        _bind(user32, kernel32)

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

            if not user32.SetClipboardData(CF_UNICODETEXT, c_void_p(handle)):
                kernel32.GlobalFree(handle)
                return False

            # On success the system owns the handle; freeing it here would be a
            # double free.
            return True
        finally:
            user32.CloseClipboard()

    except Exception:
        return False
