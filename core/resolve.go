package core

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// HostResolver resolves a backend host name to an IP address and caches the
// result for a short while, so DNS-based backends (and backends whose address
// changes) keep working without restarting the proxy. IP literals are returned
// as-is and never looked up.
type HostResolver struct {
	host string
	ttl  time.Duration

	mu      sync.Mutex
	ip      net.IP
	expires time.Time
}

// NewHostResolver creates a resolver for host. A ttl <= 0 defaults to one minute.
func NewHostResolver(host string, ttl time.Duration) *HostResolver {
	if ttl <= 0 {
		ttl = time.Minute
	}
	r := &HostResolver{host: host, ttl: ttl}
	if ip := net.ParseIP(host); ip != nil {
		r.ip = ip
		r.expires = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return r
}

// Host returns the configured host string.
func (r *HostResolver) Host() string { return r.host }

// IP returns the current address of the host, resolving it when the cache has
// expired. IPv4 addresses are preferred; IPv6 is used when nothing else exists.
// When a refresh fails but a previous answer exists, the stale answer is reused.
func (r *HostResolver) IP(ctx context.Context) (net.IP, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if r.ip != nil && now.Before(r.expires) {
		return r.ip, nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, r.host)
	if err != nil || len(addrs) == 0 {
		if r.ip != nil {
			// Keep serving the previous answer for another TTL.
			r.expires = now.Add(r.ttl)
			return r.ip, nil
		}
		if err == nil {
			err = fmt.Errorf("no addresses for %q", r.host)
		}
		return nil, err
	}

	var chosen net.IP
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			chosen = v4
			break
		}
	}
	if chosen == nil {
		chosen = addrs[0].IP
	}
	r.ip = chosen
	r.expires = now.Add(r.ttl)
	return chosen, nil
}
