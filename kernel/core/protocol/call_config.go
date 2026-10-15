package protocol

import (
	"encoding/json"
	"reflect"
)

// CallConfigEquals includes field presence: an absent stop differs from [].
func CallConfigEquals(a, b CallConfig) bool { return reflect.DeepEqual(a, b) }

// HeaderEquals compares a canonical request header, including tool order.
func HeaderEquals(a, b EpochHeader) bool {
	if !CallConfigEquals(a.Config, b.Config) ||
		!reflect.DeepEqual(a.AdapterDefaults, b.AdapterDefaults) ||
		len(a.Tools) != len(b.Tools) {
		return false
	}

	for i := range a.Tools {
		left, e1 := json.Marshal(a.Tools[i])
		right, e2 := json.Marshal(b.Tools[i])
		if e1 != nil || e2 != nil {
			return false
		}

		var x, y any
		if json.Unmarshal(left, &x) != nil || json.Unmarshal(right, &y) != nil || !reflect.DeepEqual(x, y) {
			return false
		}
	}

	return true
}

func (c CallConfig) Clone() CallConfig {
	if c.ReasoningEffort != nil {
		v := *c.ReasoningEffort
		c.ReasoningEffort = &v
	}

	if c.Temperature != nil {
		v := *c.Temperature
		c.Temperature = &v
	}

	if c.MaxTokens != nil {
		v := *c.MaxTokens
		c.MaxTokens = &v
	}

	if c.Stop != nil {
		c.Stop = append([]string{}, c.Stop...)
	}

	return c
}

func (c CallConfig) Validate() error {
	b, err := json.Marshal(RequestHeaderData{Header: EpochHeader{Config: c}, Reason: "initial"})
	if err != nil {
		return err
	}

	return validateHeader(EpochHeader{Config: c}, b)
}

// DecodeCallConfig validates the same JSON shape admitted by request/header.
func DecodeCallConfig(b []byte) (CallConfig, error) {
	var c CallConfig
	if err := sessionObject(b, &c, []string{"provider", "model"}, []string{"reasoningEffort", "temperature", "maxTokens", "stop"}); err != nil {
		return c, err
	}

	envelope, err := json.Marshal(struct {
		Header json.RawMessage `json:"header"`
	}{json.RawMessage(`{"config":` + string(b) + `}`)})
	if err != nil {
		return c, err
	}

	return c, validateHeader(EpochHeader{Config: c}, envelope)
}
