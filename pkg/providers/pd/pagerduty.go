package pd

import (
	"fmt"
	"sync"
	"time"

	"github.com/PagerDuty/go-pagerduty"
)

type Pagerduty struct {
	client          *pagerduty.Client
	apiKey          string
	caches          map[string]CacheRefresher
	refreshInterval map[string]time.Duration
	mu              sync.Mutex
}

// NewPagerduty creates a new Pagerduty client
// Input:
//
//	authToken:  PagerDuty API token
//	refreshInterval:  Interval to refresh service the cache
//
// Returns:
//
//	Pagerduty:  Pagerduty client
//	error:  Error
func NewPagerduty(authToken string, refreshInterval int) (*Pagerduty, error) {
	if authToken == "" {
		return nil, fmt.Errorf("PAGERDUTY_TOKEN environment variable not set")
	}

	client := pagerduty.NewClient(authToken)

	pd := &Pagerduty{
		client:          client,
		apiKey:          authToken,
		caches:          make(map[string]CacheRefresher),
		refreshInterval: make(map[string]time.Duration),
	}

	serviceCache := NewServiceCache(pd)
	businessServiceCache := NewBusinessServiceCache(pd)
	pd.RegisterCache("ServiceCache", serviceCache, time.Duration(refreshInterval)*time.Second)
	pd.RegisterCache("BusinessServiceCache", businessServiceCache, time.Duration(refreshInterval)*time.Second)

	//// Perform initial cache refresh and wait for it to complete
	err := pd.initialCacheRefresh()
	if err != nil {
		return nil, fmt.Errorf("failed to perform initial cache refresh: %w", err)
	}

	// Start the background refresh process
	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	go pd.RefreshCaches(stopCh, &wg)

	return pd, nil
}
