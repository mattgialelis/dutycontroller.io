package pd

import (
	"fmt"
	"log"
	"sync"
	"time"
)

type CacheRefresher interface {
	Refresh() error
}

func (pd *Pagerduty) RegisterCache(name string, cache CacheRefresher, interval time.Duration) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	pd.caches[name] = cache
	pd.refreshInterval[name] = interval
}

func (pd *Pagerduty) RefreshCaches(stopCh <-chan struct{}, wg *sync.WaitGroup) {
	for name, cache := range pd.caches {
		interval := pd.refreshInterval[name]
		wg.Add(1)
		go pd.refreshCache(name, cache, interval, stopCh, wg)
	}
}

func (pd *Pagerduty) refreshCache(name string, cache CacheRefresher, interval time.Duration, stopCh <-chan struct{}, wg *sync.WaitGroup) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Refresh the cache immediately when the function is started
	err := cache.Refresh()
	if err != nil {
		log.Printf("Error refreshing %s cache: %v", name, err)
	}
	wg.Done()

	for {
		select {
		case <-ticker.C:
			err := cache.Refresh()
			if err != nil {
				log.Printf("Error refreshing %s cache: %v", name, err)
			}
		case <-stopCh:
			log.Printf("Stopping refresh of %s cache", name)
			return
		}
	}
}

func (pd *Pagerduty) initialCacheRefresh() error {
	log.Println("Starting initial cache refresh")
	var wg sync.WaitGroup
	errCh := make(chan error, len(pd.caches))

	log.Printf("Refreshing %d caches", len(pd.caches))
	for name, cache := range pd.caches {
		wg.Add(1)
		go func(name string, cache CacheRefresher) {
			defer wg.Done()
			log.Printf("Starting refresh for cache: %s", name)
			start := time.Now()
			err := cache.Refresh()
			duration := time.Since(start)
			if err != nil {
				log.Printf("Error refreshing cache %s: %v (duration: %v)", name, err, duration)
				errCh <- fmt.Errorf("failed to refresh %s cache: %w", name, err)
			} else {
				log.Printf("Successfully refreshed cache %s (duration: %v)", name, duration)
			}
		}(name, cache)
	}

	log.Println("Waiting for all cache refreshes to complete")
	wg.Wait()
	log.Println("All cache refreshes completed")
	close(errCh)

	// Collect all errors
	var errors []error
	for err := range errCh {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		log.Printf("Initial cache refresh failed with %d errors", len(errors))
		return fmt.Errorf("initial cache refresh failed: %v", errors)
	}

	log.Println("Initial cache refresh completed successfully")
	return nil
}
