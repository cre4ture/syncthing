// Copyright (C) 2014 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package utils

import (
	"context"
	"sort"
	"time"

	"github.com/syncthing/syncthing/lib/rand"
	"github.com/syncthing/syncthing/lib/sync"
)

type SortableChannel[T any] struct {
	queued      []T
	closed      bool
	cond        *sync.TimeoutCond
	readerCh    chan T
	readerChMut sync.Mutex
}

func NewSortableChannel[T any]() *SortableChannel[T] {
	return &SortableChannel[T]{
		queued:      make([]T, 0, 2),
		closed:      false,
		cond:        sync.NewTimeoutCond(sync.NewMutex()),
		readerCh:    nil,
		readerChMut: sync.NewMutex(),
	}
}

func (q *SortableChannel[T]) PushIfNew(entry T, cmpFn func(T, T) int) bool {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	for i := range q.queued {
		if cmpFn(q.queued[i], entry) == 0 {
			return false
		}
	}
	q.queued = append(q.queued, entry)
	q.cond.Broadcast()
	return true
}

func (q *SortableChannel[T]) Push(entry T) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	q.queued = append(q.queued, entry)
	q.cond.Broadcast()
}

func (q *SortableChannel[T]) popIntern(out *T) (bool, error) {

	if len(q.queued) == 0 {
		if q.closed {
			return false, context.Canceled
		}
		return false, nil
	}

	*out = q.queued[0]
	q.queued = q.queued[1:]

	return true, nil
}

func (q *SortableChannel[T]) Pop() (out T, ok bool) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	var err error
	ok, err = q.popIntern(&out)
	if err != nil {
		return out, false
	}

	return out, ok
}

func (q *SortableChannel[T]) TryPopWithTimeout(out *T, duration time.Duration) (bool, error) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	for {
		ok, err := q.popIntern(out)
		if err != nil {
			return false, err
		}

		if ok {
			return true, nil
		}

		waiter := q.cond.SetupWait(duration)
		if waiter.Wait() {
			continue
		} else {
			return false, nil
		}
	}
}

func (q *SortableChannel[T]) ReaderChannel() <-chan T {
	q.readerChMut.Lock()
	defer q.readerChMut.Unlock()

	if q.readerCh == nil {
		q.readerCh = make(chan T)

		go func() {
			for {
				var data T
				ok, err := q.TryPopWithTimeout(&data, time.Minute)
				if err != nil {
					close(q.readerCh)
					return
				}

				if ok {
					q.readerCh <- data
				}
			}
		}()
	}

	return q.readerCh
}

func (q *SortableChannel[T]) BringToFront(entry T, cmpFn func(T, T) int) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	for i, cur := range q.queued {
		if cmpFn(cur, entry) == 0 {
			if i > 0 {
				// Shift the elements before the selected element one step to
				// the right, overwriting the selected element
				copy(q.queued[1:i+1], q.queued[0:])
				// Put the selected element at the front
				q.queued[0] = cur
			}
			return
		}
	}
}

func (q *SortableChannel[T]) Remove(entry T, cmpFn func(T, T) int) *T {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	for i := range q.queued {
		toCheck := q.queued[i]
		if cmpFn(toCheck, entry) == 0 {
			copy(q.queued[i:], q.queued[i+1:])
			q.queued = q.queued[:len(q.queued)-1]
			return &toCheck
		}
	}

	return nil
}

// Jobs returns a paginated list of file currently being pulled and files queued
// to be pulled. It also returns how many items were skipped.
func (q *SortableChannel[T]) GetPage(toSkip, pageSize uint) ([]T, uint) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	qlen := uint(len(q.queued))

	if qlen <= toSkip {
		return nil, qlen
	}

	listAmount := qlen - toSkip
	if listAmount > pageSize {
		listAmount = pageSize
	}

	list := q.queued[toSkip : toSkip+listAmount]
	return list, toSkip
}

func (q *SortableChannel[T]) Shuffle() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	rand.Shuffle(q.queued)
}

func (q *SortableChannel[T]) LenQueued() int {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	return len(q.queued)
}

func (q *SortableChannel[T]) Close() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	q.closed = true
	q.cond.Broadcast()
}

func (q *SortableChannel[T]) Sort(cmpFn func(T, T) int) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	sort.Sort(Sorter[T]{q.queued, cmpFn})
}

type Sorter[T any] struct {
	data  []T
	cmpFn func(T, T) int
}

func (q Sorter[T]) Len() int           { return len(q.data) }
func (q Sorter[T]) Less(a, b int) bool { return q.cmpFn(q.data[a], q.data[b]) < 0 }
func (q Sorter[T]) Swap(a, b int)      { q.data[a], q.data[b] = q.data[b], q.data[a] }
