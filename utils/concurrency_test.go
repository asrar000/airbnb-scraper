package utils

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestURLSetNoDuplicates(t *testing.T) {
	s := NewURLSet()

	added := s.Add("https://example.com/1")
	if !added {
		t.Error("first Add should return true")
	}

	added = s.Add("https://example.com/1")
	if added {
		t.Error("second Add of same URL should return false")
	}

	if s.Size() != 1 {
		t.Errorf("size: got %d, want 1", s.Size())
	}
}

func TestURLSetConcurrency(t *testing.T) {
	s := NewURLSet()
	var added int64

	pool := NewWorkerPool(10, 0)
	for i := 0; i < 100; i++ {
		url := "https://example.com/same"
		pool.Submit(func() {
			if s.Add(url) {
				atomic.AddInt64(&added, 1)
			}
		})
	}
	pool.Wait()

	if added != 1 {
		t.Errorf("expected exactly 1 successful add, got %d", added)
	}
}

func TestWorkerPoolRateLimit(t *testing.T) {
	rateLimitMs := 100
	pool := NewWorkerPool(1, rateLimitMs)

	var timestamps []time.Time
	mu := make(chan struct{}, 1)
	mu <- struct{}{}

	for i := 0; i < 3; i++ {
		pool.Submit(func() {
			<-mu
			timestamps = append(timestamps, time.Now())
			mu <- struct{}{}
		})
	}
	pool.Wait()

	for i := 1; i < len(timestamps); i++ {
		gap := timestamps[i].Sub(timestamps[i-1])
		min := time.Duration(rateLimitMs) * time.Millisecond
		if gap < min {
			t.Errorf("gap between job %d and %d: %v < minimum %v", i-1, i, gap, min)
		}
	}
}
