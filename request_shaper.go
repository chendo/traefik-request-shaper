package traefik_request_shaper

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net"
	"time"
	"log"

	ptypes "github.com/traefik/paerser/types"
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
	Period          ptypes.Duration          `yaml:"period"`
	MaxDelay				ptypes.Duration					 `yaml:"max_delay"`
	Burst           int64                    `yaml:"burst"`
	Exclusion       *Exclusion               `yaml:"exclusion"`
}

func CreateConfig() *Config {
	return &Config{
		Average: 0,
		Period: 0,
		Burst: 0,
	}
}

// rateLimiter implements rate limiting and traffic shaping with a set of token buckets;
// one for each traffic source. The same parameters are applied to all the buckets.
type requestShaper struct {
	name  string
	rate  rate.Limit // reqs/s
	burst int64
	// maxDelay is the maximum duration we're willing to wait for a bucket reservation to become effective, in nanoseconds.
	// For now it is somewhat arbitrarily set to 1/(2*rate).
	maxDelay time.Duration
	// each rate limiter for a given source is stored in the buckets ttlmap.
	// To keep this ttlmap constrained in size,
	// each ratelimiter is "garbage collected" when it is considered expired.
	// It is considered expired after it hasn't been used for ttl seconds.
	ttl           int
	next          http.Handler

	buckets *TtlMap // actual buckets, keyed by source.
}

// New returns a rate limiter middleware.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {



	buckets, err := NewConcurrent(maxSources)
	if err != nil {
		return nil, err
	}

	burst := config.Burst
	if burst < 1 {
		burst = 1
	}

	period := time.Duration(config.Period)
	if period < 0 {
		return nil, fmt.Errorf("negative value not valid for period: %v", period)
	}
	if period == 0 {
		period = time.Second
	}

	// if config.Average == 0, in that case,
	// the value of maxDelay does not matter since the reservation will (buggily) give us a delay of 0 anyway.
	var maxDelay time.Duration
	var rtl float64
	if config.Average > 0 {
		rtl = float64(config.Average*int64(time.Second)) / float64(period)
		// maxDelay does not scale well for rates below 1,
		// so we just cap it to the corresponding value, i.e. 0.5s, in order to keep the effective rate predictable.
		// One alternative would be to switch to a no-reservation mode (Allow() method) whenever we are in such a low rate regime.
		if rtl < 1 {
			maxDelay = 500 * time.Millisecond
		} else {
			maxDelay = time.Second / (time.Duration(rtl) * 2)
		}

		maxDelay = 5 * time.Second
		log.Print(fmt.Errorf("max delay: %f", maxDelay))
	}

	// Make the ttl inversely proportional to how often a rate limiter is supposed to see any activity (when maxed out),
	// for low rate limiters.
	// Otherwise just make it a second for all the high rate limiters.
	// Add an extra second in both cases for continuity between the two cases.
	ttl := 1
	if rtl >= 1 {
		ttl++
	} else if rtl > 0 {
		ttl += int(1 / rtl)
	}

	return &requestShaper{
		name:          name,
		rate:          rate.Limit(rtl),
		burst:         burst,
		maxDelay:      maxDelay,
		next:          next,
		buckets:       buckets,
		ttl:           ttl,
	}, nil
}


func (rs *requestShaper) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	source, _, err := net.SplitHostPort(r.RemoteAddr)

	if err != nil {
		http.Error(w, "could not extract source of request", http.StatusInternalServerError)
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

		http.Error(w, "could not insert/update bucket", http.StatusInternalServerError)
		return
	}

	// time/rate is bugged, since a rate.Limiter with a 0 Limit not only allows a Reservation to take place,
	// but also gives a 0 delay below (because of a division by zero, followed by a multiplication that flips into the negatives),
	// regardless of the current load.
	// However, for now we take advantage of this behavior to provide the no-limit ratelimiter when config.Average is 0.
	res := bucket.Reserve()
	if !res.OK() {
		http.Error(w, "No bursty traffic allowed", http.StatusTooManyRequests)
		return
	}

	delay := res.Delay()
	if delay > rs.maxDelay {
		res.Cancel()
		rs.serveDelayError(w, delay)
		return
	}

	time.Sleep(delay)
	rs.next.ServeHTTP(w, r)
}

func (rs *requestShaper) serveDelayError(w http.ResponseWriter, delay time.Duration) {
	w.WriteHeader(http.StatusTooManyRequests)

	if _, err := w.Write([]byte(http.StatusText(http.StatusTooManyRequests))); err != nil {

	}
}