"""Procedurally generated sound and music.

The project ships no assets, and that constraint already paid off once — Panda3D's
bundled models were not carried into the frozen build and broke the packaged game. So
rather than add .wav files, the audio is synthesised here on first run and cached.

Written with the stdlib `wave` and `array` modules, not numpy: numpy is not in the
frozen build's dependency list (it is only used by the dev console) and pulling it in
purely to make some sine waves would add tens of megabytes to what people download.

Generation takes well under a second and is cached in the profile directory, so it only
happens on first launch or after an upgrade.
"""

from __future__ import annotations

import array
import math
import os
import random
import sys
import wave

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.dirname(__file__))

import playerprofile  # noqa: E402

RATE = 22050  # plenty for these sounds and a quarter the size of 44.1k stereo

# Bumped when the synths change, so upgrading regenerates rather than reusing stale
# cached audio.
CACHE_VERSION = 3


def cache_dir() -> str:
    return os.path.join(playerprofile.profile_dir(), f"sfx_v{CACHE_VERSION}")


# --- synthesis helpers ------------------------------------------------------


def _write_wav(path: str, samples: list[float]) -> None:
    """Write mono 16-bit PCM, clipped to avoid wrap-around distortion."""
    data = array.array("h")
    for s in samples:
        v = int(max(-1.0, min(1.0, s)) * 32000)
        data.append(v)

    with wave.open(path, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(RATE)
        w.writeframes(data.tobytes())


def _env(i: int, n: int, attack: float, release: float) -> float:
    """Attack/release envelope over a sample index. Prevents clicks at the edges."""
    t = i / n
    if t < attack:
        return t / attack
    if t > 1.0 - release:
        return (1.0 - t) / release
    return 1.0


def _sine(freq: float, t: float) -> float:
    return math.sin(2.0 * math.pi * freq * t)


def _fade_loop(samples: list[float], fade: int) -> list[float]:
    """Cross-fade a loop's tail into its head so it repeats without a click."""
    n = len(samples)
    fade = min(fade, n // 4)
    out = list(samples)
    for i in range(fade):
        a = i / fade
        out[i] = samples[i] * a + samples[n - fade + i] * (1.0 - a)
    return out[: n - fade]


# --- the sounds -------------------------------------------------------------


def _gen_boost() -> list[float]:
    """A rising whoosh: filtered noise plus a swept low tone."""
    n = int(RATE * 0.45)
    rng = random.Random(1)
    out = []
    lp = 0.0
    for i in range(n):
        t = i / RATE
        p = i / n
        # Low-pass filtered noise; the cutoff opens as the burn builds.
        noise = rng.uniform(-1.0, 1.0)
        lp += (noise - lp) * (0.04 + 0.30 * p)
        sweep = _sine(90.0 + 260.0 * p, t) * 0.5
        out.append((lp * 0.85 + sweep) * _env(i, n, 0.12, 0.55) * 0.55)
    return out


def _gen_deposit() -> list[float]:
    """A two-note chime — the sound of getting paid."""
    n = int(RATE * 0.75)
    out = [0.0] * n
    # A perfect fifth, second note slightly delayed: reads as "completed" rather than
    # "alert".
    for freq, start, dur in ((784.0, 0.00, 0.45), (1174.7, 0.10, 0.62)):
        s = int(start * RATE)
        d = int(dur * RATE)
        for i in range(d):
            if s + i >= n:
                break
            t = i / RATE
            # A touch of second harmonic keeps it from sounding like a test tone.
            v = _sine(freq, t) * 0.7 + _sine(freq * 2.0, t) * 0.15
            out[s + i] += v * _env(i, d, 0.01, 0.85) * 0.42
    return out


def _gen_beam() -> list[float]:
    """A seamless hum loop for the tractor beam: two detuned tones plus tremolo."""
    dur = 1.0
    n = int(RATE * dur)
    out = []
    for i in range(n):
        t = i / RATE
        # Detuning by 1.5 Hz gives a slow beat that sounds mechanical rather than flat.
        base = _sine(196.0, t) * 0.5 + _sine(197.5, t) * 0.5
        shimmer = _sine(588.0, t) * 0.12
        trem = 0.75 + 0.25 * _sine(6.0, t)
        out.append((base * 0.55 + shimmer) * trem * 0.30)
    return _fade_loop(out, int(RATE * 0.08))


def _gen_ambience() -> list[float]:
    """Low space rumble: slow-moving filtered noise with a deep drone under it."""
    dur = 6.0
    n = int(RATE * dur)
    rng = random.Random(7)
    out = []
    lp1 = lp2 = 0.0
    for i in range(n):
        t = i / RATE
        noise = rng.uniform(-1.0, 1.0)
        # Two cascaded one-pole filters: much darker than a single pass.
        lp1 += (noise - lp1) * 0.010
        lp2 += (lp1 - lp2) * 0.010
        drone = _sine(48.0, t) * 0.28 + _sine(72.3, t) * 0.14
        swell = 0.7 + 0.3 * _sine(0.06, t)
        out.append((lp2 * 7.0 + drone) * swell * 0.22)
    return _fade_loop(out, int(RATE * 0.5))


def _gen_menu_music() -> list[float]:
    """A slow, looping arpeggio over a pad.

    Deliberately sparse and in a minor key — it plays behind menus and needs to survive
    being heard many times without grating.
    """
    bpm = 76.0
    beat = 60.0 / bpm
    # A minor 9th arpeggio, wandering up and back down.
    notes = [220.00, 261.63, 329.63, 440.00, 493.88, 440.00, 329.63, 261.63]
    dur = beat * len(notes)
    n = int(RATE * dur)
    out = [0.0] * n

    # Pad: root and fifth held under the whole phrase.
    for freq in (110.0, 164.81):
        for i in range(n):
            t = i / RATE
            out[i] += _sine(freq, t) * 0.10 * (0.6 + 0.4 * _sine(0.12, t))

    # Plucked arpeggio on top.
    for k, freq in enumerate(notes):
        s = int(k * beat * RATE)
        d = int(beat * 1.6 * RATE)
        for i in range(d):
            if s + i >= n:
                break
            t = i / RATE
            decay = math.exp(-t * 3.2)
            v = _sine(freq, t) * 0.6 + _sine(freq * 2.0, t) * 0.18
            out[s + i] += v * decay * 0.22

    return _fade_loop(out, int(RATE * 0.35))


def _gen_boost_loop() -> list[float]:
    """A seamless engine roar for as long as the key is held.

    Separate from boost.wav, which is the ignition whoosh. Looping the whoosh itself
    would pulse once a second and read as a stutter rather than a sustained burn; an
    ignition followed by a steady roar is what a held throttle actually sounds like.
    """
    dur = 1.0
    n = int(RATE * dur)
    rng = random.Random(23)
    out = []
    lp1 = lp2 = 0.0
    for i in range(n):
        t = i / RATE
        # Cascaded low-pass noise for the body of the roar. Darker than one pass, which
        # keeps it from turning into a hiss under everything else.
        noise = rng.uniform(-1.0, 1.0)
        lp1 += (noise - lp1) * 0.055
        lp2 += (lp1 - lp2) * 0.055
        # A low tone underneath so it has pitch as well as texture, slightly detuned so
        # it beats rather than sitting perfectly still.
        tone = _sine(78.0, t) * 0.32 + _sine(79.7, t) * 0.22 + _sine(156.0, t) * 0.10
        # Fast flutter: the combustion, not a tremolo.
        flutter = 0.88 + 0.12 * _sine(23.0, t)
        out.append((lp2 * 5.5 + tone) * flutter * 0.42)
    return _fade_loop(out, int(RATE * 0.06))


def _gen_laser() -> list[float]:
    """A short downward-swept zap.

    Kept under 200 ms and fairly quiet, because at six shots a second anything longer
    overlaps itself into a drone and anything louder is exhausting within one fight.
    """
    n = int(RATE * 0.16)
    rng = random.Random(11)
    out = []
    hp_prev = 0.0
    hp = 0.0
    for i in range(n):
        t = i / RATE
        p = i / n
        # Falling pitch is what makes a zap read as a discharge rather than a beep.
        freq = 1500.0 * math.exp(-3.4 * p) + 180.0
        tone = _sine(freq, t) * 0.7 + _sine(freq * 1.5, t) * 0.2
        # A crack of high-passed noise on the attack gives it an edge.
        noise = rng.uniform(-1.0, 1.0)
        hp = 0.85 * (hp + noise - hp_prev)
        hp_prev = noise
        crack = hp * math.exp(-26.0 * p) * 0.35
        out.append((tone + crack) * _env(i, n, 0.02, 0.72) * 0.38)
    return out


def _gen_explosion() -> list[float]:
    """A low boom: a noise burst dropped through a closing filter, plus a sub thump."""
    n = int(RATE * 1.1)
    rng = random.Random(13)
    out = []
    lp1 = lp2 = 0.0
    for i in range(n):
        t = i / RATE
        p = i / n
        noise = rng.uniform(-1.0, 1.0)
        # The cutoff closes as it decays, so the boom darkens the way a real one does.
        k = 0.35 * math.exp(-2.6 * p) + 0.010
        lp1 += (noise - lp1) * k
        lp2 += (lp1 - lp2) * k
        body = lp2 * 3.4 * math.exp(-3.0 * p)
        # Sub thump, pitch-dropping — this is the part felt rather than heard.
        sub = _sine(90.0 * math.exp(-5.0 * p) + 28.0, t) * math.exp(-4.5 * p) * 0.55
        out.append((body + sub) * _env(i, n, 0.004, 0.35) * 0.85)
    return out


GENERATORS = {
    "boost.wav": _gen_boost,
    "boostloop.wav": _gen_boost_loop,
    "deposit.wav": _gen_deposit,
    "beam.wav": _gen_beam,
    "ambience.wav": _gen_ambience,
    "menu.wav": _gen_menu_music,
    "laser.wav": _gen_laser,
    "explosion.wav": _gen_explosion,
}


def ensure_sounds() -> str:
    """Generate any missing sound files and return the cache directory.

    Returns "" if the cache cannot be written, which the audio layer treats as "run
    silently" rather than an error — no one should lose the game to a read-only folder.
    """
    d = cache_dir()
    try:
        os.makedirs(d, exist_ok=True)
    except OSError:
        return ""

    for name, gen in GENERATORS.items():
        path = os.path.join(d, name)
        if os.path.exists(path):
            continue
        try:
            _write_wav(path, gen())
        except OSError:
            return ""
    return d
