package loro

import "math/bits"

// rustSortUnstable sorts v as Rust's slice::sort_unstable_by sorts a slice of
// elements larger than 96 bytes, such as loro's Change, which is how loro
// orders a blob's changes by lamport before it applies them
// (fast_snapshot.rs:412 decode_updates, change_store.rs:525). The sort is not
// stable, and which of two changes of equal lamport is applied first decides
// where their peers land in the hash maps of the version vector and the
// frontiers, so the order they come out in is part of what loro writes.
//
// It follows core::slice::sort::unstable of the Rust standard library that
// built loro-crdt 1.17.1's WebAssembly (rustc 1.98.1, commit 48a229ce, named
// in the binary's panic paths): insertion sort up to 20 elements; past that
// ipnsort, which returns a slice that is already one ascending or strictly
// descending run as it is (reversed), and otherwise runs a quicksort that
// picks its pivot by median of three (recursively from 64 elements), splits
// with the branchy cyclic Hoare partition kept for elements of more than 96
// bytes, sorts slices of at most 16 elements by insertion, puts the elements
// equal to an earlier pivot aside, and turns to heapsort after 2*log2(n)
// bad pivots.
func rustSortUnstable[T any](v []T, less func(a, b T) bool) {
	if len(v) < 2 {
		return
	}
	if len(v) <= 20 {
		insertionSortShiftLeft(v, 1, less)
		return
	}
	run, descending := existingRun(v, less)
	if run == len(v) {
		if descending {
			for i, j := 0, len(v)-1; i < j; i, j = i+1, j-1 {
				v[i], v[j] = v[j], v[i]
			}
		}
		return
	}
	limit := 2 * (bits.Len(uint(len(v)|1)) - 1)
	rustQuicksort(v, nil, limit, less)
}

// insertionSortShiftLeft is insertion_sort_shift_left: v[:offset] is sorted,
// and each next element moves left past every element greater than it, so
// equal elements keep their order.
func insertionSortShiftLeft[T any](v []T, offset int, less func(a, b T) bool) {
	for i := offset; i < len(v); i++ {
		if !less(v[i], v[i-1]) {
			continue
		}
		tmp := v[i]
		j := i
		for j > 0 && less(tmp, v[j-1]) {
			v[j] = v[j-1]
			j--
		}
		v[j] = tmp
	}
}

// existingRun is find_existing_run: the length of the run that starts the
// slice, and whether it is strictly descending.
func existingRun[T any](v []T, less func(a, b T) bool) (int, bool) {
	n := 2
	descending := less(v[1], v[0])
	if descending {
		for n < len(v) && less(v[n], v[n-1]) {
			n++
		}
	} else {
		for n < len(v) && !less(v[n], v[n-1]) {
			n++
		}
	}
	return n, descending
}

// rustQuicksort is unstable::quicksort::quicksort. ancestor is the pivot of
// the partition v lies right of, nil for the whole slice.
func rustQuicksort[T any](v []T, ancestor *T, limit int, less func(a, b T) bool) {
	for {
		if len(v) <= 16 {
			insertionSortShiftLeft(v, 1, less)
			return
		}
		if limit == 0 {
			rustHeapsort(v, less)
			return
		}
		limit--
		p := choosePivot(v, less)
		if ancestor != nil && !less(*ancestor, v[p]) {
			// The pivot equals the one before it, so it is the smallest
			// element: the elements equal to it go left and are done.
			lt := rustPartition(v, p, func(a, b T) bool { return !less(b, a) })
			v = v[lt+1:]
			ancestor = nil
			continue
		}
		lt := rustPartition(v, p, less)
		pivot := v[lt]
		rustQuicksort(v[:lt], ancestor, limit, less)
		v = v[lt+1:]
		ancestor = &pivot
	}
}

// choosePivot is shared::pivot::choose_pivot: the median of three samples
// at 0, 4/8 and 7/8 of the slice, each a median of three itself from 64
// elements on.
func choosePivot[T any](v []T, less func(a, b T) bool) int {
	n8 := len(v) / 8
	a, b, c := 0, n8*4, n8*7
	if len(v) < 64 {
		return median3(v, a, b, c, less)
	}
	return median3Rec(v, a, b, c, n8, less)
}

func median3Rec[T any](v []T, a, b, c, n int, less func(a, b T) bool) int {
	if n*8 >= 64 {
		n8 := n / 8
		a = median3Rec(v, a, a+n8*4, a+n8*7, n8, less)
		b = median3Rec(v, b, b+n8*4, b+n8*7, n8, less)
		c = median3Rec(v, c, c+n8*4, c+n8*7, n8, less)
	}
	return median3(v, a, b, c, less)
}

func median3[T any](v []T, a, b, c int, less func(a, b T) bool) int {
	x := less(v[a], v[b])
	y := less(v[a], v[c])
	if x == y {
		if less(v[b], v[c]) != x {
			return c
		}
		return b
	}
	return a
}

// rustPartition is quicksort::partition: the pivot is swapped to the front,
// the rest split by partition_hoare_branchy_cyclic, and the pivot swapped
// to where the elements less than it end. It returns that place.
func rustPartition[T any](v []T, pivot int, less func(a, b T) bool) int {
	v[0], v[pivot] = v[pivot], v[0]
	lt := hoareCyclic(v[1:], v[0], less)
	v[0], v[lt] = v[lt], v[0]
	return lt
}

// hoareCyclic is partition_hoare_branchy_cyclic: from the left the first
// element not less than the pivot, from the right the last one less than
// it, swapped through a gap that moves each element once.
func hoareCyclic[T any](v []T, pivot T, less func(a, b T) bool) int {
	left, right := 0, len(v)
	var gapVal T
	gapPos, gap := 0, false
	for {
		for left < right && less(v[left], pivot) {
			left++
		}
		for {
			right--
			if left >= right || less(v[right], pivot) {
				break
			}
		}
		if left >= right {
			break
		}
		if !gap {
			gap, gapVal = true, v[left]
		} else {
			v[gapPos] = v[left]
		}
		gapPos = right
		v[left] = v[right]
		left++
	}
	if gap {
		v[gapPos] = gapVal
	}
	return left
}

// rustHeapsort is unstable::heapsort::heapsort.
func rustHeapsort[T any](v []T, less func(a, b T) bool) {
	n := len(v)
	for i := n + n/2 - 1; i >= 0; i-- {
		node := 0
		if i >= n {
			node = i - n
		} else {
			v[0], v[i] = v[i], v[0]
		}
		h := v[:min(i, n)]
		for {
			child := 2*node + 1
			if child >= len(h) {
				break
			}
			if child+1 < len(h) && less(h[child], h[child+1]) {
				child++
			}
			if !less(h[node], h[child]) {
				break
			}
			h[node], h[child] = h[child], h[node]
			node = child
		}
	}
}

// sortByLamport sorts a blob's changes by lamport as loro does
// (sort_unstable_by_key(|x| x.lamport)), with Rust's unstable sort.
func sortByLamport(changes []*storedChange, _ *Document) {
	rustSortUnstable(changes, func(a, b *storedChange) bool { return a.ch.Lamport < b.ch.Lamport })
}
