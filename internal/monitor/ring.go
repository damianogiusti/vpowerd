package monitor

// ring is a fixed capacity FIFO. Memory is allocated once, so a daemon that
// runs for months keeps a flat footprint.
type ring[T any] struct {
	items []T
	next  int
	full  bool
}

func newRing[T any](capacity int) *ring[T] {
	return &ring[T]{items: make([]T, capacity)}
}

func (r *ring[T]) push(v T) {
	r.items[r.next] = v
	r.next = (r.next + 1) % len(r.items)
	if r.next == 0 {
		r.full = true
	}
}

// snapshot returns a copy, oldest first.
func (r *ring[T]) snapshot() []T {
	if !r.full {
		return append([]T(nil), r.items[:r.next]...)
	}
	out := make([]T, 0, len(r.items))
	out = append(out, r.items[r.next:]...)
	return append(out, r.items[:r.next]...)
}
