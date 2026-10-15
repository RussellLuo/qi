package llm

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestResolvedProviderPolicyValidation(t *testing.T) {
	n := int64(0)
	valid := &ResolvedRetryPolicy{
		Mode:           "normal",
		MaxRetries:     &n,
		RetryableCodes: []string{"RATE_LIMIT"},
		InitialDelayMs: 10,
		MaxDelayMs:     100,
		JitterRatio:    0.1,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	if err := (*ResolvedRetryPolicy)(nil).Validate(); err != nil {
		t.Fatal(err)
	}

	always := valid.Clone()
	always.Mode = "always"
	always.MaxRetries = nil
	always.RetryableCodes = nil
	if err := always.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, mutate := range []func(*ResolvedRetryPolicy){
		func(p *ResolvedRetryPolicy) {
			p.Mode = "automatic"
		},
		func(p *ResolvedRetryPolicy) {
			p.MaxRetries = nil
		},
		func(p *ResolvedRetryPolicy) {
			*p.MaxRetries = -1
		},
		func(p *ResolvedRetryPolicy) {
			p.RetryableCodes = []string{""}
		},
		func(p *ResolvedRetryPolicy) {
			p.RetryableCodes = []string{"RATE_LIMIT", "RATE_LIMIT"}
		},
		func(p *ResolvedRetryPolicy) {
			p.InitialDelayMs = 0
		},
		func(p *ResolvedRetryPolicy) {
			p.MaxDelayMs = math.Inf(1)
		},
		func(p *ResolvedRetryPolicy) {
			p.MaxDelayMs = 2147483648
		},
		func(p *ResolvedRetryPolicy) {
			p.JitterRatio = math.NaN()
		},
		func(p *ResolvedRetryPolicy) {
			p.JitterRatio = 2
		},
	} {
		p := valid.Clone()
		mutate(p)
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted invalid policy %+v", p)
		}
	}
}

func TestResolvedRetryPolicyCloneAndJSON(t *testing.T) {
	if (*ResolvedRetryPolicy)(nil).Clone() != nil {
		t.Fatal("nil retry policy clone must stay nil")
	}
	n := int64(2)
	policy := &ResolvedRetryPolicy{Mode: "normal", MaxRetries: &n, RetryableCodes: []string{"RATE_LIMIT"}, InitialDelayMs: 10, MaxDelayMs: 100, JitterRatio: 0.1}
	encoded, err := json.Marshal(policy)
	if err != nil || string(encoded) != `{"mode":"normal","maxRetries":2,"retryableCodes":["RATE_LIMIT"],"initialDelayMs":10,"maxDelayMs":100,"jitterRatio":0.1}` {
		t.Fatalf("retry JSON = %s, %v", encoded, err)
	}
	var decoded ResolvedRetryPolicy
	if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(&decoded, policy) {
		t.Fatalf("retry round trip = %+v, %v", decoded, err)
	}
	cloned := policy.Clone()
	*cloned.MaxRetries = 3
	cloned.RetryableCodes[0] = "changed"
	if n != 2 || policy.RetryableCodes[0] != "RATE_LIMIT" {
		t.Fatal("retry clone shares mutable data")
	}
	for _, codes := range [][]string{nil, {}} {
		if (&ResolvedRetryPolicy{RetryableCodes: codes}).Clone().RetryableCodes != nil {
			t.Fatal("empty retry codes clone must be nil")
		}
	}
}
