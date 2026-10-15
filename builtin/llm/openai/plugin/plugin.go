package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/builtin/llm/openai"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
)

const PluginID = "qi/llm/openai-compatible"

type Plugin struct {
	Catalog            []modelcatalog.Entry `json:"catalog"`
	Route              string               `json:"route"`
	Provider           string               `json:"provider"`
	Endpoint           string               `json:"endpoint"`
	APIKeyEnv          string               `json:"api_key_env"`
	Headers            map[string]string    `json:"headers"`
	ContextWindow      int                  `json:"context_window"`
	Parameters         json.RawMessage      `json:"parameters"`
	Defaults           json.RawMessage      `json:"defaults"`
	MaxTokensParameter string               `json:"max_tokens_parameter"`
	ReasoningHistory   string               `json:"reasoning_history"`
	TimeoutSeconds     int                  `json:"timeout_seconds"`

	APIKey     string       `json:"-"`
	HTTPClient *http.Client `json:"-"`
}

func (p *Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Requires: []gordis.ServiceSpec{llm.RuntimeKey.Spec()},
		New: func() gordis.Plugin {
			return &Plugin{APIKey: p.APIKey, HTTPClient: p.HTTPClient}
		},
	}
}

func (p *Plugin) Validate() error {
	if _, err := p.DescribeModels(); err != nil {
		return err
	}
	provider := p.Provider
	if provider == "" {
		provider = "openai"
	}
	if err := llm.ValidateDefaults(p.Defaults, provider); err != nil {
		return err
	}
	config, err := p.adapterConfig("")
	if err != nil {
		return err
	}
	_, err = openai.New(config)
	return err
}

func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	if p.Route == "" {
		p.Route = "chat"
	}
	if p.Provider == "" {
		p.Provider = "openai"
	}
	apiKey := p.APIKey
	if p.APIKeyEnv != "" {
		apiKey = os.Getenv(p.APIKeyEnv)
		if apiKey == "" {
			return errors.New("OpenAI-compatible API key environment variable is empty")
		}
	}
	config, err := p.adapterConfig(apiKey)
	if err != nil {
		return err
	}
	adapter, err := openai.New(config)
	if err != nil {
		return err
	}
	description, err := p.DescribeModels()
	if err != nil {
		return err
	}
	runtime, err := scope.Get(llm.RuntimeKey)
	if err != nil {
		return err
	}
	return runtime.Bind(scope).RegisterProvider(llm.AdapterRegistration{
		Route: p.Route, Provider: p.Provider, Defaults: p.Defaults, Adapter: adapter,
	}, description.Models)
}

func (p *Plugin) adapterConfig(apiKey string) (openai.Config, error) {
	if p.TimeoutSeconds < 0 || int64(p.TimeoutSeconds) > int64(1<<63-1)/int64(time.Second) {
		return openai.Config{}, errors.New("OpenAI-compatible timeout_seconds is out of range")
	}
	return openai.Config{
		Provider: p.Provider, Endpoint: p.Endpoint, APIKey: apiKey, Headers: p.Headers,
		HTTPClient: p.HTTPClient, ContextWindow: p.ContextWindow, Parameters: p.Parameters,
		MaxTokensParameter: p.MaxTokensParameter, ReasoningHistory: p.ReasoningHistory,
		Timeout: time.Duration(p.TimeoutSeconds) * time.Second,
	}, nil
}

func (p *Plugin) DescribeModels() (modelcatalog.Description, error) {
	route, provider := p.Route, p.Provider
	if route == "" {
		route = "chat"
	}
	if provider == "" {
		provider = "openai"
	}
	return modelcatalog.Provider(route, provider, nil, p.Catalog)
}
