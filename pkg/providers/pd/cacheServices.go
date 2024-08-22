package pd

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/PagerDuty/go-pagerduty"
)

type ServiceCache struct {
	pd     *Pagerduty
	client *pagerduty.Client
	mu     sync.Mutex
	cache  []pagerduty.Service
}

func NewServiceCache(pd *Pagerduty) *ServiceCache {
	return &ServiceCache{
		pd:     pd,
		client: pd.client,
	}
}

func (sc *ServiceCache) Refresh() error {
	log.Println("ServiceCache: Attempting to acquire mutex")
	mutexAcquired := make(chan bool, 1)
	go func() {
		sc.mu.Lock()
		mutexAcquired <- true
	}()

	select {
	case <-mutexAcquired:
		log.Println("ServiceCache: Mutex acquired")
	case <-time.After(5 * time.Second):
		log.Println("ServiceCache: Failed to acquire mutex after 5 seconds")
		return fmt.Errorf("failed to acquire mutex for service cache refresh")
	}

	defer func() {
		sc.mu.Unlock()
		log.Println("ServiceCache: Mutex released")
	}()

	log.Println("ServiceCache: Starting to fetch services from PagerDuty")
	start := time.Now()

	services, err := sc.client.ListServicesPaginated(
		context.Background(),
		pagerduty.ListServiceOptions{},
	)
	duration := time.Since(start)

	if err != nil {
		log.Printf("ServiceCache: Failed to fetch services from PagerDuty: %v (duration: %v)", err, duration)
		return fmt.Errorf("failed to refresh PagerDuty service cache: %w", err)
	}

	log.Printf("ServiceCache: Fetched %d services (duration: %v)", len(services), duration)

	// Update the cache with the new services
	sc.cache = services

	log.Println("ServiceCache: All services fetched and cache updated")
	return nil
}

func (pd *Pagerduty) GetServiceCache() (*ServiceCache, error) {

	serviceCache, ok := pd.caches["ServiceCache"].(*ServiceCache)
	if !ok {
		return nil, &CacheNotRegistered{CacheName: "ServiceCache"}
	}

	serviceCache.mu.Lock()
	defer serviceCache.mu.Unlock()

	if len(serviceCache.cache) == 0 {
		return nil, &CacheEmpty{CacheName: "ServiceCache"}
	}

	return serviceCache, nil
}

// force a refresh of the service cache
func (p *Pagerduty) ForceRefreshServiceCache() error {
	serviceCache, err := p.GetServiceCache()
	if err != nil {
		return fmt.Errorf("failed to get service cache: %w", err)
	}

	err = serviceCache.Refresh()
	if err != nil {
		return fmt.Errorf("failed to refresh service cache: %w", err)
	}

	return nil
}
