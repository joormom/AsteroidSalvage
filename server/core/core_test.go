package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newWorld(t *testing.T) *World {
	t.Helper()
	w, err := NewWorld(1024)
	if err != nil {
		t.Fatalf("NewWorld: %v", err)
	}
	t.Cleanup(w.Close)
	return w
}

// The finding that justifies having run this spike at all.
//
// lagrange and EnTT disagree about what "no entity" is, in opposite directions: lagrange's
// LG_ENTITY_INVALID is 0, while EnTT hands out 0 as its *first* entity and reserves
// 0xFFFFFFFF. The Go server assumes lagrange's convention in a dozen places, so carrying
// it into EnTT unchanged would make the first entity ever created read as absent — a
// silent bug that would look like a game rule misfiring.
//
// Both halves are pinned: that EnTT's null is what we think it is, and that zero really is
// a usable entity, because the second is the assumption a reader is likely to "fix".
func TestNullEntityMatchesEnTT(t *testing.T) {
	if got := nullEntityFromCpp(); got != NullEntity {
		t.Errorf("EnTT reports null entity %#x, but NullEntity is %#x", got, NullEntity)
	}
	if NullEntity == 0 {
		t.Error("NullEntity is 0, which is lagrange's convention, not EnTT's")
	}
}

func TestZeroIsAValidEntity(t *testing.T) {
	w := newWorld(t)

	first := w.Spawn(7, 8, 9, 42)
	if first != 0 {
		t.Skipf("EnTT's first entity is %v, not 0 — the hazard this pins has moved", first)
	}

	// A real, addressable entity that happens to be numbered zero.
	w.ApplyDamage([]Damage{{Entity: first, Amount: 12}})
	snap := w.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot has %d entities, want 1", len(snap))
	}
	if snap[0].Entity != 0 || snap[0].Health != 30 {
		t.Errorf("entity 0 = %+v, want entity 0 at health 30 — zero must be addressable",
			snap[0])
	}
}

// The baseline: entities and components survive the round trip at all.
func TestRegistryRoundTrip(t *testing.T) {
	w := newWorld(t)

	a := w.Spawn(1, 2, 3, 100)
	b := w.Spawn(4, 5, 6, 50)
	if a == NullEntity || b == NullEntity || a == b {
		t.Fatalf("spawned %v and %v, want two distinct valid entities", a, b)
	}
	if got := w.Count(); got != 2 {
		t.Fatalf("Count = %d, want 2", got)
	}

	byEntity := map[uint32]Body{}
	for _, s := range w.Snapshot() {
		byEntity[s.Entity] = s
	}
	if got, want := byEntity[a], (Body{Entity: a, X: 1, Y: 2, Z: 3, Health: 100}); got != want {
		t.Errorf("entity a = %+v, want %+v", got, want)
	}
	if got, want := byEntity[b], (Body{Entity: b, X: 4, Y: 5, Z: 6, Health: 50}); got != want {
		t.Errorf("entity b = %+v, want %+v", got, want)
	}

	w.Kill(a)
	if got := w.Count(); got != 1 {
		t.Errorf("Count = %d after killing one of two, want 1", got)
	}
}

// The bulk write half of the boundary, including the case the real sim hits constantly:
// a batch naming an entity that died earlier in the same tick.
func TestBulkWriteSkipsDeadEntities(t *testing.T) {
	w := newWorld(t)

	live := w.Spawn(0, 0, 0, 100)
	dead := w.Spawn(0, 0, 0, 100)
	w.Kill(dead)

	w.ApplyDamage([]Damage{
		{Entity: live, Amount: 30},
		{Entity: dead, Amount: 30},
		{Entity: 999999, Amount: 30}, // never existed
	})

	snap := w.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot has %d entities, want 1", len(snap))
	}
	if snap[0].Health != 70 {
		t.Errorf("health = %v after 30 damage to a 100 hull, want 70", snap[0].Health)
	}
}

// A snapshot buffer smaller than the world must truncate rather than write past its end.
func TestSnapshotRespectsBufferSize(t *testing.T) {
	w, err := NewWorld(4)
	if err != nil {
		t.Fatalf("NewWorld: %v", err)
	}
	t.Cleanup(w.Close)

	for i := 0; i < 10; i++ {
		w.Spawn(float32(i), 0, 0, 100)
	}
	if got := len(w.Snapshot()); got != 4 {
		t.Errorf("snapshot returned %d entities into a 4-slot buffer, want 4", got)
	}
	if got := w.Count(); got != 10 {
		t.Errorf("Count = %d, want 10 — truncating the snapshot must not drop entities", got)
	}
}

// Go's runtime does not participate in C++ unwinding, so an exception reaching the
// boundary is a process kill with no usable stack rather than an error anybody can debug.
// This asserts the guards hold; if they do not, this test does not fail, it crashes.
func TestExceptionsCannotEscapeIntoGo(t *testing.T) {
	w := newWorld(t)
	if got := w.ProvokeThrow(); got != -1 {
		t.Errorf("ProvokeThrow = %d, want -1 (a contained std::exception)", got)
	}
	// And the world is still usable afterwards, which is the difference between catching
	// an exception and merely surviving one.
	if e := w.Spawn(1, 1, 1, 10); e == NullEntity {
		t.Error("registry unusable after a contained throw")
	}
}

// THE ONE THAT MATTERS.
//
// Adding any C++ to a cgo build normally makes the binary depend on libstdc++-6.dll,
// libgcc_s_seh-1.dll and libwinpthread-1.dll. Windows does not ship them and
// build_dist.py does not copy them, so the failure does not appear here — it appears on a
// player's machine, as a binary that will not start. The -static-* LDFLAGS in core.go are
// what prevent it, and this is what keeps them honest.
func TestBinaryStaysStandalone(t *testing.T) {
	if _, err := exec.LookPath("objdump"); err != nil {
		t.Skip("objdump not on PATH; cannot inspect imports")
	}

	// Compile *this package's own test binary* and inspect that.
	//
	// The obvious target — `go build .` on the server — is worthless here: nothing in main
	// imports this package, so Go never compiles a line of C++ and the check passes while
	// proving nothing. This binary is the smallest one that genuinely links EnTT and
	// libstdc++, which is the thing under test. Once stage 1 lands and sim depends on the
	// core for real, this should switch to building the server proper.
	exe := filepath.Join(t.TempDir(), "spike.exe")
	build := exec.Command("go", "test", "-c", "-o", exe, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go test -c: %v\n%s", err, out)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("built binary missing: %v", err)
	}

	out, err := exec.Command("objdump", "-p", exe).Output()
	if err != nil {
		t.Fatalf("objdump: %v", err)
	}

	// Anything outside the Windows-provided set has to be shipped alongside the exe.
	forbidden := []string{"libstdc++", "libgcc_s", "libwinpthread"}
	lower := strings.ToLower(string(out))
	for _, dll := range forbidden {
		if strings.Contains(lower, dll) {
			t.Errorf("server.exe imports %s — it will not start on a machine without "+
				"MinGW installed. Check the -static-libstdc++ / -static-libgcc LDFLAGS "+
				"in core.go", dll)
		}
	}

	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "DLL Name:") {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(
			strings.TrimPrefix(strings.TrimSpace(line), "DLL Name:")))
		switch {
		case name == "kernel32.dll",
			strings.HasPrefix(name, "api-ms-win-"),
			name == "msvcrt.dll",
			name == "advapi32.dll",
			name == "ws2_32.dll",
			name == "ntdll.dll",
			name == "user32.dll",
			name == "bcrypt.dll",
			name == "crypt32.dll",
			name == "iphlpapi.dll",
			name == "secur32.dll",
			name == "userenv.dll",
			name == "winmm.dll",
			name == "shell32.dll",
			name == "ole32.dll",
			name == "oleaut32.dll",
			name == "psapi.dll",
			name == "dbghelp.dll":
			// Ships with Windows.
		default:
			t.Errorf("server.exe imports %q, which is not a Windows-provided DLL; "+
				"either it must be packaged by build_dist.py or linked statically", name)
		}
	}
}
