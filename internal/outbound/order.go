package outbound

// order acks a consumer's events in sequence while they are handled concurrently: the cursor moves past an event only
// once it and every event before it are done, so a crash never skips one still being sent.
type order struct {
	pending []int64 // in the order the bus sent them
	done    map[int64]bool
}

func (o *order) start(seq int64) { o.pending = append(o.pending, seq) }

// finish marks seq done and returns the sequence number the cursor may now move to, or 0.
func (o *order) finish(seq int64) int64 {
	if o.done == nil {
		o.done = map[int64]bool{}
	}
	o.done[seq] = true
	var ack int64
	for len(o.pending) > 0 && o.done[o.pending[0]] {
		ack = o.pending[0]
		delete(o.done, ack)
		o.pending = o.pending[1:]
	}
	return ack
}
