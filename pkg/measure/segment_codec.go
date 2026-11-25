package measure

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/klauspost/compress/zstd"
)

// The bytes of a segment: a header in the clear, then the body compressed.
//
//	magic "FMSG", format version (1 byte), engine version (uvarint),
//	registry digest (uvarint length, bytes), size of the body (uvarint),
//	the body, compressed with zstd at one fixed level.
//
// The body is a string table and then the tables and streams of the segment,
// all as varints, with local indexes. See segData.
const (
	segMagic  = "FMSG"
	segFormat = 1
	// segMaxBody bounds the size of an uncompressed body, and segMaxRatio how
	// much a body may grow when it is uncompressed. A segment of a week of one
	// repository is far below both.
	segMaxBody  = 256 << 20
	segMaxRatio = 512
)

// segData is what a segment holds, in the form it is written and read in.
type segData struct {
	engine int
	digest string
	repo   string
	week   int64
	facts  segFacts
	agents []string
	flows  []string
	items  []segItem
	obs    []segStream[segObs]
	stays  []segStream[segStay]
	runs   []segRun
	seen   []segSeen
	figs   figs
}

// segFacts are the facts of a repository that are not a count.
type segFacts struct {
	private, ciWait, viaRelease bool
	from, synced                time.Time
	notes                       []string
}

type segItem struct {
	kind, key, url, title string
	class                 int8
	at                    time.Time
}

type segStream[T any] struct {
	name string
	list []T
}

type segObs struct {
	at          time.Time
	v           float64
	class       int8
	agent, item int32 // local index, -1 for none
	mode        uint8
}

type segStay struct {
	from, to    time.Time
	class       int8
	agent, item int32
	mode        uint8
}

type segRun struct {
	at     time.Time
	flow   int32
	failed bool
}

type segSeen struct {
	kind string
	at   int64
	src  string
}

type enc struct {
	buf  bytes.Buffer
	tmp  [binary.MaxVarintLen64]byte
	strs map[string]uint64
	list []string
}

func (e *enc) u(v uint64) { e.buf.Write(e.tmp[:binary.PutUvarint(e.tmp[:], v)]) }
func (e *enc) i(v int64)  { e.buf.Write(e.tmp[:binary.PutVarint(e.tmp[:], v)]) }
func (e *enc) b(v bool) {
	if v {
		e.buf.WriteByte(1)
	} else {
		e.buf.WriteByte(0)
	}
}
func (e *enc) s(v string) {
	i, ok := e.strs[v]
	if !ok {
		i = uint64(len(e.list))
		e.strs[v] = i
		e.list = append(e.list, v)
	}
	e.u(i)
}
func (e *enc) t(v time.Time) {
	if v.IsZero() {
		e.buf.WriteByte(0)
		return
	}
	e.buf.WriteByte(1)
	e.i(v.Unix())
	e.u(uint64(v.Nanosecond()))
}
func (e *enc) f(v float64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
	e.buf.Write(b[:])
}

// encode writes the segment. The same data gives the same bytes.
func (d *segData) encode() ([]byte, error) {
	e := &enc{strs: map[string]uint64{}}
	e.s(d.repo)
	e.i(d.week)
	e.b(d.facts.private)
	e.b(d.facts.ciWait)
	e.b(d.facts.viaRelease)
	e.t(d.facts.from)
	e.t(d.facts.synced)
	e.u(uint64(len(d.facts.notes)))
	for _, n := range d.facts.notes {
		e.s(n)
	}
	e.u(uint64(len(d.agents)))
	for _, a := range d.agents {
		e.s(a)
	}
	e.u(uint64(len(d.flows)))
	for _, f := range d.flows {
		e.s(f)
	}
	e.u(uint64(len(d.items)))
	for _, it := range d.items {
		e.s(it.kind)
		e.s(it.key)
		e.s(it.url)
		e.s(it.title)
		e.i(int64(it.class))
		e.t(it.at)
	}
	e.u(uint64(len(d.obs)))
	for _, st := range d.obs {
		e.s(st.name)
		e.u(uint64(len(st.list)))
		for _, o := range st.list {
			e.t(o.at)
			e.f(o.v)
			e.i(int64(o.class))
			e.i(int64(o.agent))
			e.i(int64(o.item))
			e.u(uint64(o.mode))
		}
	}
	e.u(uint64(len(d.stays)))
	for _, st := range d.stays {
		e.s(st.name)
		e.u(uint64(len(st.list)))
		for _, o := range st.list {
			e.t(o.from)
			e.t(o.to)
			e.i(int64(o.class))
			e.i(int64(o.agent))
			e.i(int64(o.item))
			e.u(uint64(o.mode))
		}
	}
	e.u(uint64(len(d.runs)))
	for _, r := range d.runs {
		e.t(r.at)
		e.u(uint64(r.flow))
		e.b(r.failed)
	}
	e.u(uint64(len(d.seen)))
	for _, s := range d.seen {
		e.s(s.kind)
		e.i(s.at)
		e.s(s.src)
	}
	for _, n := range []int{d.figs.changes, d.figs.merged, d.figs.cut, d.figs.ciRuns, d.figs.ciJoined,
		d.figs.prodDeploys, d.figs.ships, d.figs.shipsByCommit, d.figs.verif} {
		e.u(uint64(n))
	}
	envs := sortedKeys(d.figs.envs)
	e.u(uint64(len(envs)))
	for _, v := range envs {
		e.s(v)
	}

	// The string table comes first in the body, and the rest after it.
	var body bytes.Buffer
	head := &enc{}
	head.u(uint64(len(e.list)))
	for _, s := range e.list {
		head.u(uint64(len(s)))
		head.buf.WriteString(s)
	}
	body.Write(head.buf.Bytes())
	body.Write(e.buf.Bytes())
	if body.Len() > segMaxBody {
		return nil, fmt.Errorf("segment of %s is %d bytes, over the limit of %d", d.repo, body.Len(), segMaxBody)
	}

	zw, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1), zstd.WithZeroFrames(true))
	if err != nil {
		return nil, err
	}
	defer zw.Close()
	packed := zw.EncodeAll(body.Bytes(), nil)

	out := &enc{}
	out.buf.WriteString(segMagic)
	out.buf.WriteByte(segFormat)
	out.u(uint64(d.engine))
	out.u(uint64(len(d.digest)))
	out.buf.WriteString(d.digest)
	out.u(uint64(body.Len()))
	out.buf.Write(packed)
	return out.buf.Bytes(), nil
}

type dec struct {
	b     []byte
	err   error
	str   []string
	elems uint64
}

func (r *dec) fail(msg string) {
	if r.err == nil {
		r.err = errors.New(msg)
	}
}

func (r *dec) u() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.fail("truncated or malformed number")
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *dec) i() int64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Varint(r.b)
	if n <= 0 {
		r.fail("truncated or malformed number")
		return 0
	}
	r.b = r.b[n:]
	return v
}

// segMaxElems caps the records of one segment (items, observations, stays,
// runs and seen records together), so the memory a segment makes is bounded
// by a fixed number and not only by the size of its bytes.
var segMaxElems uint64 = 1 << 20

// count reads a number of elements that each take at least min bytes, so a
// count that the bytes left cannot hold is refused before anything is made.
// Records are also counted against segMaxElems.
func (r *dec) count(min int) int {
	v := r.u()
	if r.err != nil {
		return 0
	}
	if v > uint64(len(r.b))/uint64(min) {
		r.fail("a count is larger than the segment")
		return 0
	}
	if min > 1 {
		r.elems += v
		if r.elems > segMaxElems {
			r.fail("the segment holds too many records")
			return 0
		}
	}
	return int(v)
}

// small reads a number that must fit in an int32.
func (r *dec) small() int32 {
	v := r.i()
	if v < math.MinInt32 || v > math.MaxInt32 {
		r.fail("a number is out of range")
		return 0
	}
	return int32(v)
}

func (r *dec) bool() bool {
	if r.err != nil {
		return false
	}
	if len(r.b) == 0 {
		r.fail("truncated")
		return false
	}
	v := r.b[0]
	r.b = r.b[1:]
	if v > 1 {
		r.fail("a flag is neither 0 nor 1")
	}
	return v == 1
}

func (r *dec) time() time.Time {
	if !r.bool() {
		return time.Time{}
	}
	sec := r.i()
	ns := r.u()
	if ns >= 1e9 {
		r.fail("a time has too many nanoseconds")
		return time.Time{}
	}
	return time.Unix(sec, int64(ns)).UTC()
}

func (r *dec) float() float64 {
	if r.err != nil {
		return 0
	}
	if len(r.b) < 8 {
		r.fail("truncated")
		return 0
	}
	v := math.Float64frombits(binary.LittleEndian.Uint64(r.b))
	r.b = r.b[8:]
	if math.IsNaN(v) || math.IsInf(v, 0) {
		r.fail("a value is not a finite number")
		return 0
	}
	return v
}

func (r *dec) s() string {
	i := r.u()
	if r.err != nil {
		return ""
	}
	if i >= uint64(len(r.str)) {
		r.fail("a string index is out of range")
		return ""
	}
	return r.str[i]
}

// index reads a local index that is -1 or below n.
func (r *dec) index(n int) int32 {
	v := r.small()
	if v < -1 || int(v) >= n {
		r.fail("an index is out of range")
		return -1
	}
	return v
}

func (r *dec) class() int8 {
	v := r.i()
	if v < 0 || v > int64(classNone) {
		r.fail("a class is out of range")
		return 0
	}
	return int8(v)
}

func (r *dec) mode() uint8 {
	v := r.u()
	if v > uint64(modeHuman) {
		r.fail("a review mode is out of range")
		return 0
	}
	return uint8(v)
}

// header reads what is in the clear: the engine, the registry digest, and the
// packed body with the size it must unpack to.
func readHeader(b []byte) (engine int, digest string, size uint64, packed []byte, err error) {
	if len(b) < len(segMagic)+1 || string(b[:len(segMagic)]) != segMagic {
		return 0, "", 0, nil, errors.New("not a segment")
	}
	if f := b[len(segMagic)]; f != segFormat {
		return 0, "", 0, nil, fmt.Errorf("segment format %d is not the format %d this library reads", f, segFormat)
	}
	r := &dec{b: b[len(segMagic)+1:]}
	eng := r.u()
	n := r.u()
	if r.err == nil && n > 128 {
		r.fail("the registry digest is too long")
	}
	if r.err == nil && n > uint64(len(r.b)) {
		r.fail("truncated")
	}
	if r.err != nil {
		return 0, "", 0, nil, fmt.Errorf("segment header: %w", r.err)
	}
	digest = string(r.b[:n])
	r.b = r.b[n:]
	size = r.u()
	if r.err != nil {
		return 0, "", 0, nil, fmt.Errorf("segment header: %w", r.err)
	}
	if eng > math.MaxInt32 {
		return 0, "", 0, nil, errors.New("segment header: the engine version is out of range")
	}
	return int(eng), digest, size, r.b, nil
}

// decodeSegment reads a segment. It is safe on any input.
func decodeSegment(b []byte) (*segData, error) {
	engine, digest, size, packed, err := readHeader(b)
	if err != nil {
		return nil, err
	}
	if size > segMaxBody || size > uint64(len(packed))*segMaxRatio+1<<20 {
		return nil, fmt.Errorf("segment says its body is %d bytes unpacked, which is more than is allowed", size)
	}
	zr, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(segMaxBody), zstd.WithDecoderMaxWindow(64<<20))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	body, err := zr.DecodeAll(packed, make([]byte, 0, size))
	if err != nil {
		return nil, fmt.Errorf("segment body: %w", err)
	}
	if uint64(len(body)) != size {
		return nil, errors.New("segment body is not the size its header says")
	}
	r := &dec{b: body}
	n := r.count(1)
	r.str = make([]string, 0, n)
	for i := 0; i < n && r.err == nil; i++ {
		l := r.u()
		if l > uint64(len(r.b)) {
			r.fail("a string is longer than the segment")
			break
		}
		r.str = append(r.str, string(r.b[:l]))
		r.b = r.b[l:]
	}
	d := &segData{engine: engine, digest: digest}
	d.repo = r.s()
	d.week = r.i()
	d.facts.private, d.facts.ciWait, d.facts.viaRelease = r.bool(), r.bool(), r.bool()
	d.facts.from, d.facts.synced = r.time(), r.time()
	for i, n := 0, r.count(1); i < n; i++ {
		d.facts.notes = append(d.facts.notes, r.s())
	}
	for i, n := 0, r.count(1); i < n; i++ {
		d.agents = append(d.agents, r.s())
	}
	for i, n := 0, r.count(1); i < n; i++ {
		d.flows = append(d.flows, r.s())
	}
	for i, n := 0, r.count(6); i < n; i++ {
		d.items = append(d.items, segItem{kind: r.s(), key: r.s(), url: r.s(), title: r.s(), class: r.class(), at: r.time()})
	}
	for i, n := 0, r.count(1); i < n; i++ {
		st := segStream[segObs]{name: r.s()}
		for j, m := 0, r.count(13); j < m; j++ {
			st.list = append(st.list, segObs{at: r.time(), v: r.float(), class: r.class(),
				agent: r.index(len(d.agents)), item: r.index(len(d.items)), mode: r.mode()})
		}
		d.obs = append(d.obs, st)
	}
	for i, n := 0, r.count(1); i < n; i++ {
		st := segStream[segStay]{name: r.s()}
		for j, m := 0, r.count(6); j < m; j++ {
			st.list = append(st.list, segStay{from: r.time(), to: r.time(), class: r.class(),
				agent: r.index(len(d.agents)), item: r.index(len(d.items)), mode: r.mode()})
		}
		d.stays = append(d.stays, st)
	}
	for i, n := 0, r.count(3); i < n; i++ {
		x := segRun{at: r.time()}
		if f := r.u(); f < uint64(len(d.flows)) {
			x.flow = int32(f)
		} else {
			r.fail("a workflow index is out of range")
		}
		x.failed = r.bool()
		d.runs = append(d.runs, x)
	}
	for i, n := 0, r.count(3); i < n; i++ {
		d.seen = append(d.seen, segSeen{kind: r.s(), at: r.i(), src: r.s()})
	}
	for _, p := range []*int{&d.figs.changes, &d.figs.merged, &d.figs.cut, &d.figs.ciRuns, &d.figs.ciJoined,
		&d.figs.prodDeploys, &d.figs.ships, &d.figs.shipsByCommit, &d.figs.verif} {
		v := r.u()
		if v > math.MaxInt32 {
			r.fail("a figure is out of range")
		}
		*p = int(v)
	}
	if n := r.count(1); n > 0 {
		d.figs.envs = map[string]bool{}
		for i := 0; i < n; i++ {
			d.figs.envs[r.s()] = true
		}
	}
	if r.err != nil {
		return nil, fmt.Errorf("segment body: %w", r.err)
	}
	if len(r.b) != 0 {
		return nil, errors.New("segment body: bytes left over")
	}
	return d, nil
}
