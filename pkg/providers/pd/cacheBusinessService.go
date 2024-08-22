package pd

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/PagerDuty/go-pagerduty"
)

type BusinessServiceCache struct {
	pd     *Pagerduty
	client *pagerduty.Client
	mu     sync.Mutex
	cache  []*pagerduty.BusinessService
}

func NewBusinessServiceCache(pd *Pagerduty) *BusinessServiceCache {
	return &BusinessServiceCache{
		pd:     pd,
		client: pd.client,
	}
}

func (bsc *BusinessServiceCache) Refresh() error {
	log.Println("BusinessServiceCache: Attempting to acquire mutex")
	mutexAcquired := make(chan bool, 1)
	go func() {
		bsc.mu.Lock()
		mutexAcquired <- true
	}()

	select {
	case <-mutexAcquired:
		log.Println("BusinessServiceCache: Mutex acquired")
	case <-time.After(5 * time.Second):
		log.Println("BusinessServiceCache: Failed to acquire mutex after 5 seconds")
		return fmt.Errorf("failed to acquire mutex for business service cache refresh")
	}

	defer func() {
		bsc.mu.Unlock()
		log.Println("BusinessServiceCache: Mutex released")
	}()

	log.Println("BusinessServiceCache: Starting to fetch business services from PagerDuty")
	start := time.Now()

	businessServices, err := bsc.client.ListBusinessServicesPaginated(
		context.Background(),
		pagerduty.ListBusinessServiceOptions{},
	)
	duration := time.Since(start)

	if err != nil {
		log.Printf("BusinessServiceCache: Failed to fetch business services from PagerDuty: %v (duration: %v)", err, duration)
		return fmt.Errorf("failed to refresh PagerDuty business service cache: %w", err)
	}

	log.Printf("BusinessServiceCache: Fetched %d business services (duration: %v)", len(businessServices), duration)

	// Update the cache with the new business services
	bsc.cache = businessServices

	log.Println("BusinessServiceCache: All business services fetched and cache updated")
	return nil
}

func (pd *Pagerduty) GetBusinessServiceCache() (*BusinessServiceCache, error) {
	businessServiceCache, ok := pd.caches["BusinessServiceCache"].(*BusinessServiceCache)
	if !ok {
		return nil, &CacheNotRegistered{CacheName: "BusinessServiceCache"}
	}

	businessServiceCache.mu.Lock()
	defer businessServiceCache.mu.Unlock()

	if len(businessServiceCache.cache) == 0 {
		return nil, &CacheEmpty{CacheName: "BusinessServiceCache"}
	}

	return businessServiceCache, nil
}

// Force a refresh of the business service cache
func (p *Pagerduty) ForceRefreshBusinessServiceCache() error {
	businessServiceCache, err := p.GetBusinessServiceCache()
	if err != nil {
		return fmt.Errorf("failed to get business service cache: %w", err)
	}

	err = businessServiceCache.Refresh()
	if err != nil {
		return fmt.Errorf("failed to refresh business service cache: %w", err)
	}

	return nil
}

// GetBusinessServiceByName returns the business service ID by name
func (pd *Pagerduty) GetBusinessServiceByName(name string) (string, bool, error) {
	businessServiceCache, err := pd.GetBusinessServiceCache()
	if err != nil {
		return "", false, fmt.Errorf("failed to get business service cache: %w", err)
	}

	businessServiceCache.mu.Lock()
	defer businessServiceCache.mu.Unlock()

	for _, bs := range businessServiceCache.cache {
		if bs.Name == name {
			return bs.ID, true, nil
		}
	}

	return "", false, nil
}
