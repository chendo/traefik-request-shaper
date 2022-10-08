package traefik_request_shaper

import (
	"context"
	"fmt"
	"net/http"
	"net"
	"time"
	"log"

	"golang.org/x/time/rate"
)

const (
	typeName   = "RateLimiterType"
	maxSources = 65536
)

type Exclusion struct {
	SourceRange []string            `json:"sourceRange,omitempty" toml:"sourceRange,omitempty" yaml:"sourceRange,omitempty"`
}

type Config struct {
	Average         int64                    `yaml:"average"`
	Period          string          `yaml:"period"`
	MaxDelay				string					 `yaml:"maxDelay"`
	ExceedWait      string					 `yaml:"exceedWait"` 
	Burst           int64                    `yaml:"burst"`
	TTL   					string					 `yaml:"ttl"` // how long before we expire entries
	Exclusion       *Exclusion               `yaml:"exclusion"`
}

func CreateConfig() *Config {
	return &Config{
		Average: 0,
		MaxDelay: "5s",
		ExceedWait: "0s",
		Period: "1s",
		TTL: "60s",
		Burst: 0,
	}
}

// rateLimiter implements rate limiting and traffic shaping with a set of token buckets;
// one for each traffic source. The same parameters are applied to all the buckets.
type requestShaper struct {
	name  string
	rate  rate.Limit // reqs/s
	burst int64
	
	maxDelay time.Duration // maxDelay is the maximum duration we're willing let the user to wait before we return an error
	exceedWait time.Duration // how long we make the client wait when they exceed maxDelay
	// each rate limiter for a given source is stored in the buckets ttlmap.
	// To keep this ttlmap constrained in size,
	// each ratelimiter is "garbage collected" when it is considered expired.
	// It is considered expired after it hasn't been used for ttl seconds.
	ttl           int
	next          http.Handler

	buckets *TtlMap // actual buckets, keyed by source.
}

// New returns a request shaper middleware.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	buckets, err := NewConcurrent(maxSources)
	if err != nil {
		return nil, err
	}

	burst := config.Burst
	if burst < 1 {
		burst = 1
	}

	period, err := time.ParseDuration(config.Period)
	if err != nil {
		return nil, fmt.Errorf("could not parse period: %v, err: %s", period, err)
	}
	if period < 0 {
		return nil, fmt.Errorf("negative value not valid for period: %v", period)
	}
	if period == 0 {
		period = time.Second
	}

	ttl, err := time.ParseDuration(config.TTL)
	if err != nil {
		return nil, fmt.Errorf("could not parse ttl: %v", period)
	}
	if ttl < 0 {
		return nil, fmt.Errorf("negative value not valid for ttl: %v", period)
	}
	if ttl == 0 {
		// ttl of zero would effectively be useless
		ttl = time.Second
	}

	exceedWait, err := time.ParseDuration(config.ExceedWait)
	if err != nil {
		return nil, fmt.Errorf("could not parse exceedWait: %v", period)
	}
	// if config.Average == 0, in that case,
	// the value of maxDelay does not matter since the reservation will (buggily) give us a delay of 0 anyway.
	var maxDelay time.Duration
	var rtl float64
	if config.Average > 0 {
		rtl = float64(config.Average*int64(time.Second)) / float64(period)

		maxDelay, err = time.ParseDuration(config.MaxDelay)
		if maxDelay == 0 || err != nil {
			// maxDelay does not scale well for rates below 1,
			// so we just cap it to the corresponding value, i.e. 0.5s, in order to keep the effective rate predictable.
			// One alternative would be to switch to a no-reservation mode (Allow() method) whenever we are in such a low rate regime.
			if rtl < 1 {
				maxDelay = 500 * time.Millisecond
			} else {
				maxDelay = time.Second / (time.Duration(rtl) * 2)
			}
		}
	}


	return &requestShaper{
		name:          name,
		rate:          rate.Limit(rtl),
		burst:         burst,
		maxDelay:      maxDelay,
		exceedWait:    exceedWait,
		next:          next,
		buckets:       buckets,
		ttl:           int(ttl),
	}, nil
}


func (rs *requestShaper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// if rate is 0, perform no shaping
	if rs.rate == 0 {
		rs.next.ServeHTTP(w, r)
		return
	}

	source, _, err := net.SplitHostPort(r.RemoteAddr)

	if err != nil {
		http.Error(w, "could not extract source of request", http.StatusInternalServerError)
		log.Printf("Error extracting IP: %s", err)
		return
	}

	var bucket *rate.Limiter
	if rlSource, exists := rs.buckets.Get(source); exists {
		bucket = rlSource
	} else {
		bucket = rate.NewLimiter(rs.rate, int(rs.burst))
	}

	// We Set even in the case where the source already exists,
	// because we want to update the expiryTime everytime we get the source,
	// as the expiryTime is supposed to reflect the activity (or lack thereof) on that source.
	if err := rs.buckets.Set(source, bucket, rs.ttl); err != nil {
		log.Printf("Error updating bucket: %s", err)
		http.Error(w, "could not insert/update bucket", http.StatusInternalServerError)
		return
	}

	res := bucket.Reserve()
	if !res.OK() {
		http.Error(w, "No bursty traffic allowed", http.StatusTooManyRequests)
		return
	}

	delay := res.Delay()
	if delay > rs.maxDelay {
		// sleep to mitigate attacks
		res.Cancel()
		time.Sleep(rs.exceedWait)
		rs.serveDelayError(w, delay)
		return
	}

	time.Sleep(delay)
	rs.next.ServeHTTP(w, r)
}

func (rs *requestShaper) serveDelayError(w http.ResponseWriter, delay time.Duration) {
	w.WriteHeader(http.StatusTooManyRequests)

	if _, err := w.Write([]byte(http.StatusText(http.StatusTooManyRequests))); err != nil {
		log.Printf("Error serving delay error: %s", err)
	}
}