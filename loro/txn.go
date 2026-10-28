package loro

import (
	"github.com/Deln0r/loro-go/encoding/change"
)

// txn is the open transaction: the local ops made since the last commit,
// which become one change. Its id, lamport, deps and the timestamp floor are
// taken when its first op is made, from the document as it is then; loro
// takes them when the transaction starts (txn.rs:398), which is right after
// the previous commit or import, and nothing between then and the first op
// can move them.
type txn struct {
	startCounter, nextCounter int64
	startLamport, nextLamport int64
	deps                      []ID
	latestTimestamp           int64
	ops                       []Op
	prov                      []opProv
}

// begin opens the transaction if none is open.
func (d *Document) begin() *txn {
	if d.tx != nil {
		return d.tx
	}
	l := d.log
	next := l.vv[d.peer]
	deps := l.fr.ids()
	lam := l.nextLamport(deps)
	if d.knocked(rulePendingIsolated) {
		lam = max(lam, l.pendingLamportEnd())
	}
	d.tx = &txn{
		startCounter: next, nextCounter: next,
		startLamport: lam, nextLamport: lam,
		deps:            deps,
		latestTimestamp: l.greatestTimestamp(deps),
	}
	return d.tx
}

// apply makes one local op: it takes the next id and lamport, goes into the
// state at once, and joins the transaction's ops, folded into the op before
// it where loro's RleVec would fold it (txn.rs:811 apply_local_op).
func (d *Document) apply(st *docState, op Op, p opProv) error {
	t := d.begin()
	op.Peer = d.peer
	op.Counter = t.nextCounter
	op.Lamport = t.nextLamport
	if err := st.applyLocal(op); err != nil {
		return err
	}
	t.nextCounter += op.Len
	t.nextLamport += op.Len
	c := storedChange{ch: Change{Ops: t.ops}, prov: t.prov}
	c.pushOp(op, p, d)
	t.ops, t.prov = c.ch.Ops, c.prov
	return nil
}

// commit closes the open transaction as one change, if it holds any op. The
// change's timestamp is the requested one, or now when timestamps are
// recorded and 0 when not, raised to the latest timestamp of the changes the
// transaction depends on (txn.rs:523): timestamps never go back.
func (d *Document) commit(msg string, ts int64, hasTS bool) {
	t := d.tx
	d.tx = nil
	if t == nil || len(t.ops) == 0 {
		return
	}
	if !hasTS {
		ts = 0
		if d.recordTimestamp {
			ts = d.now()
		}
	}
	if !d.knocked(ruleTimestampFloor) {
		ts = max(t.latestTimestamp, ts)
	}
	c := &storedChange{
		ch: Change{
			ID:        ID{Peer: d.peer, Counter: t.startCounter},
			Lamport:   t.startLamport,
			Timestamp: ts,
			Deps:      t.deps,
			CommitMsg: msg,
			Ops:       t.ops,
		},
		prov: t.prov,
	}
	d.log.insertLocal(c, d.mergeInterval, d)
}

// canFold is Op::is_mergable (op.rs:144): b continues a's ids in the same
// container and their contents fold. Text inserts fold when b starts where a
// ends and their bytes are adjacent in one arena buffer; list inserts when b
// starts where a ends and a's values may grow; deletes by
// DeleteSpanWithId::is_mergable. Nothing else folds.
func canFold(a provOp, b Op, pb opProv, d *Document) bool {
	if keyOf(a.op) != keyOf(b) || a.op.Counter+a.op.Len != b.Counter || a.op.VKind != b.VKind {
		return false
	}
	switch {
	case a.op.VKind == change.VKStr && a.op.Kind == change.CText:
		if d.knocked(ruleFoldLocalOnly) {
			return a.op.Pos+a.op.Len == b.Pos
		}
		return a.prov.kind == provText && pb.kind == provText && a.prov.gen == pb.gen &&
			a.prov.end == pb.start && a.op.Pos+a.op.Len == b.Pos
	case a.op.VKind == change.VKLoroValue && (a.op.Kind == change.CList || a.op.Kind == change.CMovableList):
		if d.knocked(ruleFoldLocalOnly) {
			return a.op.Pos+a.op.Len == b.Pos
		}
		return a.prov.kind == provList && a.op.Pos+a.op.Len == b.Pos
	case a.op.VKind == change.VKDeleteSeq:
		x, ok1 := delSpanOf(a.op)
		y, ok2 := delSpanOf(b)
		return ok1 && ok2 && x.mergeable(y)
	}
	return false
}

// foldOps folds b into a; canFold must hold.
func foldOps(a, b provOp) (Op, opProv) {
	op := a.op
	switch op.VKind {
	case change.VKStr:
		op.Value = a.op.Value.(string) + b.op.Value.(string)
		a.prov.end = b.prov.end
	case change.VKLoroValue:
		x, _ := a.op.Value.([]any)
		y, _ := b.op.Value.([]any)
		op.Value = append(append(make([]any, 0, len(x)+len(y)), x...), y...)
	case change.VKDeleteSeq:
		x, _ := delSpanOf(a.op)
		y, _ := delSpanOf(b.op)
		x.merge(y)
		op.Pos = x.pos
		op.Value = DeleteSpan{Peer: x.peer, Counter: x.ctr, Len: x.slen}
	}
	op.Len += b.op.Len
	return op, a.prov
}

// delSpan is loro's DeleteSpanWithId (container/list/list_op.rs:160): a
// delete at pos of signed length slen, its targets the ids from ctr of peer,
// ctr being the lowest whichever way the delete ran. A negative slen is a
// backwards delete recorded at the position of its first, rightmost target.
type delSpan struct {
	pos, slen int64
	peer      uint64
	ctr       int64
}

func delSpanOf(op Op) (delSpan, bool) {
	ds, ok := op.Value.(DeleteSpan)
	if !ok {
		return delSpan{}, false
	}
	return delSpan{pos: op.Pos, slen: ds.Len, peer: ds.Peer, ctr: ds.Counter}, true
}

func (s delSpan) bidirectional() bool { return s.slen == 1 || s.slen == -1 }

func (s delSpan) length() int64 {
	if s.slen < 0 {
		return -s.slen
	}
	return s.slen
}

func (s delSpan) direction() int64 {
	if s.slen > 0 {
		return 1
	}
	return -1
}

func (s delSpan) start() int64 {
	if s.slen > 0 {
		return s.pos
	}
	return s.pos + 1 + s.slen
}

func (s delSpan) end() int64 {
	if s.slen > 0 {
		return s.pos + s.slen
	}
	return s.pos + 1
}

func (s delSpan) nextPos() int64 {
	if s.slen > 0 {
		return s.start()
	}
	return s.start() - 1
}

func (s delSpan) prevPos() int64 {
	if s.slen > 0 {
		return s.pos
	}
	return s.end()
}

func (s delSpan) idIs(peer uint64, ctr int64) bool { return s.peer == peer && s.ctr == ctr }

// mergeable is DeleteSpanWithId::is_mergable (list_op.rs:217): two deletes
// fold when the second removes the element next to the first in the
// direction the deletes run, positions and target ids both continuing.
func (s delSpan) mergeable(r delSpan) bool {
	switch {
	case s.bidirectional() && r.bidirectional():
		return (s.pos == r.pos && r.idIs(s.peer, s.ctr+1)) ||
			(s.pos == r.pos+1 && s.idIs(r.peer, r.ctr+1))
	case s.bidirectional():
		if s.pos != r.prevPos() {
			return false
		}
		if r.slen > 0 {
			return r.idIs(s.peer, s.ctr+1)
		}
		return s.idIs(r.peer, r.ctr+r.length())
	case r.bidirectional():
		if s.nextPos() != r.pos {
			return false
		}
		if s.slen > 0 {
			return r.idIs(s.peer, s.ctr+s.length())
		}
		return s.idIs(r.peer, r.ctr+1)
	default:
		if s.nextPos() != r.pos || s.direction() != r.direction() {
			return false
		}
		if s.slen > 0 {
			return r.idIs(s.peer, s.ctr+s.length())
		}
		return s.idIs(r.peer, r.ctr+r.length())
	}
}

// merge is DeleteSpanWithId::merge (list_op.rs:266): the lowest target id,
// and DeleteSpan::merge for the span, which stays at the first delete's
// position.
func (s *delSpan) merge(r delSpan) {
	s.ctr = min(s.ctr, r.ctr)
	switch {
	case s.bidirectional() && r.bidirectional():
		if s.pos == r.pos {
			s.slen = 2
		} else {
			s.slen = -2
		}
	case s.bidirectional():
		s.slen = r.slen + r.direction()
	case r.bidirectional():
		s.slen += s.direction()
	default:
		s.slen += r.slen
	}
}
