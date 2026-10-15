package llm

import (
	"errors"
	"math"
)

// ResolvedRetryPolicy is provider-owned data. The kernel neither supplies
// defaults nor executes this policy; request-error listeners decide whether to retry.
type ResolvedRetryPolicy struct {
	Mode           string   `json:"mode"`
	MaxRetries     *int64   `json:"maxRetries,omitempty"`
	RetryableCodes []string `json:"retryableCodes,omitempty"`
	InitialDelayMs float64  `json:"initialDelayMs"`
	MaxDelayMs     float64  `json:"maxDelayMs"`
	JitterRatio    float64  `json:"jitterRatio"`
}

func (p *ResolvedRetryPolicy) Clone() *ResolvedRetryPolicy {
	if p == nil {
		return nil
	}

	v := *p
	v.RetryableCodes = append([]string(nil), p.RetryableCodes...)
	if p.MaxRetries != nil {
		n := *p.MaxRetries
		v.MaxRetries = &n
	}

	return &v
}

func (p *ResolvedRetryPolicy) Validate() error {
	if p == nil {
		return nil
	}

	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	if !finite(p.InitialDelayMs) ||
		!finite(p.MaxDelayMs) ||
		p.InitialDelayMs <= 0 ||
		p.MaxDelayMs < p.InitialDelayMs ||
		p.MaxDelayMs > 2147483647 ||
		!finite(p.JitterRatio) ||
		p.JitterRatio < 0 ||
		p.JitterRatio > 1 {
		return errors.New("invalid resolved retry backoff")
	}

	switch p.Mode {
	case "always":
		if p.MaxRetries != nil || p.RetryableCodes != nil {
			return errors.New("always retry policy has normal-only fields")
		}
	case "normal":
		if p.MaxRetries == nil || *p.MaxRetries < 0 || *p.MaxRetries > 9007199254740991 || len(p.RetryableCodes) == 0 {
			return errors.New("invalid resolved normal retry policy")
		}
		seen := map[string]bool{}
		for _, code := range p.RetryableCodes {
			if code == "" || seen[code] {
				return errors.New("invalid retryable codes")
			}

			seen[code] = true
		}
	default:
		return errors.New("retry policy mode must be normal or always")
	}

	return nil
}
