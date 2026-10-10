package app

import "time"

type options struct {
	now func() time.Time
}

// Option configures a service. This "functional options" pattern keeps
// constructors stable as optional settings are added.
type Option func(*options)

// WithClock overrides time.Now, mainly so tests can control expiry.
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

func buildOptions(opts []Option) options {
	o := options{now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}
