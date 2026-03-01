package events

import (
	"sync"
)

type Message struct {
	Type string
	Body any
}

type Bus struct {
	mu   sync.RWMutex
	subs map[chan Message]struct{}
}

func NewBus() *Bus {
	return &Bus{subs: make(map[chan Message]struct{})}
}

func (b *Bus) Subscribe(buffer int) chan Message {
	if buffer <= 0 {
		buffer = 16
	}
	ch := make(chan Message, buffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Bus) Unsubscribe(ch chan Message) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
	b.mu.Unlock()
}

func (b *Bus) Publish(msg Message) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}
