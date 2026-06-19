package change

import (
	"encoding/binary"

	"github.com/Deln0r/loro-go/encoding/columnar"
	"github.com/Deln0r/loro-go/encoding/postcard"
)

// ChangeHeader is the decoded header blob (blob 1). Columns are raw-concatenated
// and delimited by counts derived from n (NChanges) and sum(DepLens).
type ChangeHeader struct {
	Peers       []uint64 // peer table; Peers[0] is this block's peer
	AtomLens    []uint64 // N-1 values (last atom len derived from counter_len)
	DepOnSelf   []bool   // N
	DepLens     []uint64 // N: non-self dependency count per change
	DepPeerIdxs []uint64 // sum(DepLens): index into Peers
	DepCounters []int64  // sum(DepLens)
	Lamports    []int64  // N-1
}

// DecodeHeader decodes the header blob given the change count n.
func DecodeHeader(blob []byte, n int) (*ChangeHeader, error) {
	r := postcard.NewReader(blob)
	peerNum, err := r.Uvarint()
	if err != nil {
		return nil, err
	}
	if peerNum > uint64(r.Remaining())/8 { // each peer entry is exactly 8 bytes
		return nil, ErrBlock
	}
	h := &ChangeHeader{Peers: make([]uint64, peerNum)}
	for i := range h.Peers {
		b, err := r.Bytes(8)
		if err != nil {
			return nil, err
		}
		h.Peers[i] = binary.LittleEndian.Uint64(b)
	}
	if max0(n-1) > r.Remaining() { // each atom len is a varint of at least 1 byte
		return nil, ErrBlock
	}
	h.AtomLens = make([]uint64, max0(n-1))
	for i := range h.AtomLens {
		if h.AtomLens[i], err = r.Uvarint(); err != nil {
			return nil, err
		}
	}
	if h.DepOnSelf, err = columnar.BoolRleN(r, n); err != nil {
		return nil, err
	}
	if h.DepLens, err = columnar.AnyRleNU64(r, n); err != nil {
		return nil, err
	}
	sumDeps := 0
	for _, d := range h.DepLens {
		// Each dependency contributes at least one byte to the columns read below,
		// so a sum exceeding the unread remainder (or that overflows int) is bogus.
		if d > uint64(r.Remaining()) {
			return nil, ErrBlock
		}
		sumDeps += int(d)
		if sumDeps < 0 || sumDeps > r.Remaining() {
			return nil, ErrBlock
		}
	}
	if h.DepPeerIdxs, err = columnar.AnyRleNU64(r, sumDeps); err != nil {
		return nil, err
	}
	if h.DepCounters, err = columnar.DeltaOfDeltaN(r, sumDeps); err != nil {
		return nil, err
	}
	if h.Lamports, err = columnar.DeltaOfDeltaN(r, max0(n-1)); err != nil {
		return nil, err
	}
	if !r.Empty() {
		return nil, ErrBlock
	}
	return h, nil
}

// EncodeHeader re-emits the header blob (inverse of DecodeHeader).
func EncodeHeader(h *ChangeHeader) []byte {
	out := postcard.AppendUvarint(nil, uint64(len(h.Peers)))
	var b [8]byte
	for _, p := range h.Peers {
		binary.LittleEndian.PutUint64(b[:], p)
		out = append(out, b[:]...)
	}
	for _, a := range h.AtomLens {
		out = postcard.AppendUvarint(out, a)
	}
	out = append(out, columnar.EncodeBoolRle(h.DepOnSelf)...)
	out = append(out, columnar.EncodeAnyRleU64(h.DepLens)...)
	out = append(out, columnar.EncodeAnyRleU64(h.DepPeerIdxs)...)
	out = append(out, columnar.EncodeDeltaOfDelta(h.DepCounters)...)
	out = append(out, columnar.EncodeDeltaOfDelta(h.Lamports)...)
	return out
}

// ChangeMeta is the decoded change_meta blob (blob 2).
type ChangeMeta struct {
	Timestamps []int64  // N (i64)
	CommitMsgs []string // N (empty strings when no message)
}

// DecodeChangeMeta decodes the change_meta blob given the change count n.
func DecodeChangeMeta(blob []byte, n int) (*ChangeMeta, error) {
	r := postcard.NewReader(blob)
	ts, err := columnar.DeltaOfDeltaN(r, n)
	if err != nil {
		return nil, err
	}
	lens, err := columnar.AnyRleNU64(r, n)
	if err != nil {
		return nil, err
	}
	msgs := make([]string, n)
	for i := range msgs {
		s, err := r.Bytes(int(lens[i]))
		if err != nil {
			return nil, err
		}
		msgs[i] = string(s)
	}
	if !r.Empty() {
		return nil, ErrBlock
	}
	// DeltaOfDeltaN returns nil for n==0; normalize to a length-n slice.
	if ts == nil {
		ts = make([]int64, n)
	}
	return &ChangeMeta{Timestamps: ts, CommitMsgs: msgs}, nil
}

// EncodeChangeMeta re-emits the change_meta blob (inverse of DecodeChangeMeta).
func EncodeChangeMeta(m *ChangeMeta) []byte {
	out := columnar.EncodeDeltaOfDelta(m.Timestamps)
	lens := make([]uint64, len(m.CommitMsgs))
	for i, s := range m.CommitMsgs {
		lens[i] = uint64(len(s))
	}
	out = append(out, columnar.EncodeAnyRleU64(lens)...)
	for _, s := range m.CommitMsgs {
		out = append(out, s...)
	}
	return out
}

func max0(x int) int {
	if x < 0 {
		return 0
	}
	return x
}
