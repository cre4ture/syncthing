package utils

import "sync"

type Protected[T any] struct {
	t   *T
	mut sync.Mutex
}

func NewProtected[T any](value T) *Protected[T] {
	return &Protected[T]{
		t:   &value,
		mut: sync.Mutex{},
	}
}

func (p *Protected[T]) Lock() *T {
	p.mut.Lock()
	return p.t
}

func (p *Protected[T]) Unlock() {
	p.mut.Unlock()
}
