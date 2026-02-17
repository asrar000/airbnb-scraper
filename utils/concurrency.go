package utils

import (
	"sync"
	"time"
)

// WorkerPool manages a pool of goroutines with rate limiting.
type WorkerPool struct {
	maxWorkers  int
	rateLimitMs int
	semaphore   chan struct{}
	wg          sync.WaitGroup
	mu          sync.Mutex
	lastRequest time.Time
}

// NewWorkerPool creates a WorkerPool with the given concurrency and rate limit.
func NewWorkerPool(maxWorkers, rateLimitMs int) *WorkerPool {
	return &WorkerPool{
		maxWorkers:  maxWorkers,
		rateLimitMs: rateLimitMs,
		semaphore:   make(chan struct{}, maxWorkers),
		lastRequest: time.Now(),
	}
}

// Submit enqueues a job for execution in the pool.
func (wp *WorkerPool) Submit(job func()) {
	wp.wg.Add(1)
	wp.semaphore <- struct{}{}

	go func() {
		defer wp.wg.Done()
		defer func() { <-wp.semaphore }()

		wp.enforceRateLimit()
		job()
	}()
}

// Wait blocks until all submitted jobs have completed.
func (wp *WorkerPool) Wait() {
	wp.wg.Wait()
}

func (wp *WorkerPool) enforceRateLimit() {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	minInterval := time.Duration(wp.rateLimitMs) * time.Millisecond
	elapsed := time.Since(wp.lastRequest)
	if elapsed < minInterval {
		time.Sleep(minInterval - elapsed)
	}
	wp.lastRequest = time.Now()
}

// URLSet is a thread-safe set for tracking visited URLs.
type URLSet struct {
	mu   sync.RWMutex
	seen map[string]struct{}
}

// NewURLSet creates an empty URLSet.
func NewURLSet() *URLSet {
	return &URLSet{seen: make(map[string]struct{})}
}

// Add returns true if the URL was newly added, false if already present.
func (s *URLSet) Add(url string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.seen[url]; exists {
		return false
	}
	s.seen[url] = struct{}{}
	return true
}

// Contains returns true if the URL has already been visited.
func (s *URLSet) Contains(url string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.seen[url]
	return exists
}

// Size returns the number of unique URLs tracked.
func (s *URLSet) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.seen)
}
