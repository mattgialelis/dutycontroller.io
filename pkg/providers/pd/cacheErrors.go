package pd

import "fmt"

type CacheNotRegistered struct {
	CacheName string
}

func (e *CacheNotRegistered) Error() string {
	return fmt.Sprintf("%s cache is not registered", e.CacheName)
}

type CacheEmpty struct {
	CacheName string
}

func (e *CacheEmpty) Error() string {
	return fmt.Sprintf("%s cache is empty", e.CacheName)
}
