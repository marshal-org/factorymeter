package measure

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// pack makes a segment of a body, with a header that says the body is size
// bytes unpacked.
func pack(t *testing.T, body []byte, size uint64) []byte {
	t.Helper()
	zw, _ := zstd.NewWriter(nil)
	var out bytes.Buffer
	out.WriteString(segMagic)
	out.WriteByte(segFormat)
	var tmp [binary.MaxVarintLen64]byte
	out.Write(tmp[:binary.PutUvarint(tmp[:], uint64(EngineVersion))])
	out.Write(tmp[:binary.PutUvarint(tmp[:], 0)])
	out.Write(tmp[:binary.PutUvarint(tmp[:], size)])
	out.Write(zw.EncodeAll(body, nil))
	return out.Bytes()
}

func uv(v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	return tmp[:binary.PutUvarint(tmp[:], v)]
}

func TestSegmentHostileBytes(t *testing.T) {
	segs, _ := oneRepoSegments(t)
	good := segs[0]
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	check := func(name string, b []byte) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s: panic %v", name, r)
			}
		}()
		if l, err := Merge(b); err == nil && l == nil {
			t.Errorf("%s: no line and no error", name)
		}
	}
	mustFail := func(name string, b []byte) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s: panic %v", name, r)
			}
		}()
		if _, err := Merge(b); err == nil {
			t.Errorf("%s: merged", name)
		}
	}
	for n := 0; n < len(good); n++ {
		mustFail("truncated", good[:n])
	}
	for i := range good {
		bad := append([]byte(nil), good...)
		bad[i] ^= 0xff
		check("flipped", bad) // a flip may land in a value that stays valid
	}
	// A string table that says it has a huge number of strings.
	mustFail("huge string count", pack(t, uv(1<<40), 8))
	// A count of elements larger than the bytes left.
	body := append(uv(1), 1, 'x')      // one string
	body = append(body, uv(0)...)      // repo
	body = append(body, 0)             // week
	body = append(body, 0, 0, 0, 0, 0) // flags and times
	body = append(body, uv(1<<40)...)  // notes: far more than fit
	mustFail("huge note count", pack(t, body, uint64(len(body))))
	// An index outside its table.
	body = append(uv(1), 1, 'x')
	body = append(body, uv(7)...)
	mustFail("string index out of range", pack(t, body, uint64(len(body))))
	// A header that says more than is allowed, and one that says more than the
	// packed bytes can hold.
	mustFail("body too large", pack(t, []byte{0}, segMaxBody+1))
	mustFail("ratio", pack(t, []byte{0}, 100<<20))
	// A body that is not the size it says.
	mustFail("wrong size", pack(t, []byte{0, 0, 0}, 9))
	// A compression bomb: 200 MiB of zeros in a few hundred bytes.
	bomb := pack(t, make([]byte, 200<<20), 8)
	mustFail("bomb", bomb)
	mustFail("empty", nil)

	runtime.GC()
	runtime.ReadMemStats(&after)
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 512<<20 {
		t.Errorf("hostile segments allocated %d MiB", grown>>20)
	}
}
