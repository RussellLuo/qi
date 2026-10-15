package fixture_test

import (
	"context"
	"encoding/json"
	"testing"

	"example.com/qi-boot-fixture/probe"
	"github.com/RussellLuo/gordis"
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	launchapi "github.com/RussellLuo/qi/kernel/boot/launch"
	profileapi "github.com/RussellLuo/qi/kernel/boot/profile"
	agentapi "github.com/RussellLuo/qi/kernel/core/agent"
	middlewareapi "github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	contractsapi "github.com/RussellLuo/qi/kernel/core/contracts"
	gordisbridgeapi "github.com/RussellLuo/qi/kernel/core/gordisbridge"
	llmapi "github.com/RussellLuo/qi/kernel/core/llm"
	modelcatalogapi "github.com/RussellLuo/qi/kernel/core/modelcatalog"
	promptapi "github.com/RussellLuo/qi/kernel/core/prompt"
	protocolapi "github.com/RussellLuo/qi/kernel/core/protocol"
	sessionapi "github.com/RussellLuo/qi/kernel/core/session"
	toolsapi "github.com/RussellLuo/qi/kernel/core/tools"
)

type parser struct{}

func (parser) Help() string                                       { return "external help" }
func (parser) Parse([]string, bool) (launchapi.Invocation, error) { return invocation{}, nil }

type invocation struct{}

func (invocation) HelpRequested() bool { return false }
func (invocation) Prepare(context.Context, launchapi.Input) (launchapi.Prepared, error) {
	return prepared{}, nil
}

type prepared struct{}

func (prepared) Bind(p []gordis.Plugin) ([]gordis.Plugin, error) { return p, nil }
func (prepared) CheckReady(context.Context) error                { return nil }
func (prepared) WorkingDirectory() string                        { return "/" }

var _ launchapi.Parser = parser{}
var _ launchapi.ReadyCheck = prepared{}
var _ launchapi.WorkingDirectory = prepared{}
var _ contractsapi.ContributionOwner = (*gordisbridgeapi.ScopeOwner)(nil)

// Explicit owner registration remains available beside the Scope Bindings.
var _ func(*gordis.Scope) *gordisbridgeapi.ScopeOwner = gordisbridgeapi.NewScopeOwner
var _ func(*toolsapi.Runtime, contractsapi.ContributionOwner, toolsapi.Definition) (func(), error) = (*toolsapi.Runtime).Register
var _ func(*llmapi.Runtime, contractsapi.ContributionOwner, llmapi.AdapterRegistration) (func(), error) = (*llmapi.Runtime).RegisterAdapter
var _ func(*promptapi.Runtime, contractsapi.ContributionOwner, promptapi.Section) (func(), error) = (*promptapi.Runtime).Register
var _ func(*middlewareapi.Runtime, contractsapi.ContributionOwner, middlewareapi.Middleware) (func(), error) = (*middlewareapi.Runtime).Register
var _ gordis.Key[*toolsapi.Runtime] = toolsapi.RuntimeKey
var _ func(*toolsapi.Runtime, *gordis.Scope) toolsapi.Binding = (*toolsapi.Runtime).Bind
var _ func(toolsapi.Binding, toolsapi.Definition) (func(), error) = toolsapi.Binding.Register
var _ func(toolsapi.Binding, []toolsapi.Definition) (func(), error) = toolsapi.Binding.RegisterBatch
var _ func(toolsapi.Binding, toolsapi.Middleware) (func(), error) = toolsapi.Binding.RegisterMiddleware
var _ gordis.Key[*llmapi.Runtime] = llmapi.RuntimeKey
var _ func(*llmapi.Runtime, *gordis.Scope) llmapi.Binding = (*llmapi.Runtime).Bind
var _ func(llmapi.Binding, llmapi.AdapterRegistration) (func(), error) = llmapi.Binding.RegisterAdapter
var _ func(llmapi.Binding, []modelcatalogapi.Spec) error = llmapi.Binding.RegisterModels
var _ func(llmapi.Binding, modelcatalogapi.Defaults) error = llmapi.Binding.RegisterDefaults
var _ func(llmapi.Binding, llmapi.AdapterRegistration, []modelcatalogapi.Spec) error = llmapi.Binding.RegisterProvider
var _ gordis.Key[*promptapi.Runtime] = promptapi.RuntimeKey
var _ func(*promptapi.Runtime, *gordis.Scope) promptapi.Binding = (*promptapi.Runtime).Bind
var _ func(promptapi.Binding, promptapi.Section) (func(), error) = promptapi.Binding.Register
var _ gordis.Key[*middlewareapi.Runtime] = middlewareapi.RuntimeKey
var _ func(*middlewareapi.Runtime, *gordis.Scope) middlewareapi.Binding = (*middlewareapi.Runtime).Bind
var _ func(middlewareapi.Binding, middlewareapi.Middleware) (func(), error) = middlewareapi.Binding.Register
var _ gordis.Key[*launchapi.Context] = launchapi.LaunchContextKey
var _ gordis.Key[sessionapi.PersistenceProvider] = sessionapi.PersistenceKey
var _ gordis.Key[sessionapi.Store] = sessionapi.StoreKey
var _ gordis.Key[agentapi.Registry] = agentapi.RegistryKey
var _ gordis.Key[agentapi.InitializerRegistry] = agentapi.InitializerKey
var _ gordis.Key[agentapi.AgentEvents] = agentapi.ObserverKey
var _ gordis.Key[agentapi.FactoryProvider] = agentapi.FactoryKey
var _ func(agentapi.FactoryProvider, *gordis.Scope) (agentapi.Factory, error) = agentapi.FactoryProvider.Bind
var _ func(*gordis.Scope, agentapi.FactoryOptions) (agentapi.FactoryProvider, error) = gordisbridgeapi.NewAgentFactoryService

// A third-party implementation uses only the capability's service contract.
type externalFactoryProvider struct{ factory agentapi.Factory }

func (p externalFactoryProvider) Bind(*gordis.Scope) (agentapi.Factory, error) {
	return p.factory, nil
}

var _ agentapi.FactoryProvider = externalFactoryProvider{}

func TestNativePluginAndPublicResultClosure(t *testing.T) {
	extension := probe.Extension()
	extension.Registrations[0].Invocation = parser{}
	extension.Registrations[0].ValidateCandidate = func(profileapi.Profile) error { return nil }
	extension.Registrations[0].Descriptor.DescribeModels = func(json.RawMessage) (modelcatalogapi.Description, error) {
		return modelcatalogapi.Description{Models: []modelcatalogapi.Spec{}, Defaults: &modelcatalogapi.Defaults{}}, nil
	}
	app, err := applicationapi.Compose(extension)
	if err != nil || len(app.Registrations) != 1 {
		t.Fatal(app, err)
	}
	invoke := toolsapi.JSONTool(func(context.Context, toolsapi.Execution) (json.RawMessage, error) {
		return json.RawMessage(`{"text":"ok"}`), nil
	})
	result, err := invoke(context.Background(), toolsapi.Execution{Call: protocolapi.ToolCall{ID: protocolapi.ToolCallID("call"), Name: "echo"}})
	if err != nil || result.Validate() != nil {
		t.Fatal(result, err)
	}
	result.AdditionalContexts = []protocolapi.Message{{ID: protocolapi.MessageID("context"), Role: protocolapi.RoleUser,
		Source: protocolapi.MessageSource{Kind: protocolapi.SourceUser}, Content: []protocolapi.ContentBlock{protocolapi.TextBlock{Text: "context"}}}}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	failed := toolsapi.FailureResult("fixture", context.Canceled)
	var info *protocolapi.ToolFailureInfo = failed.Error.Info
	if info.Code != "fixture" || failed.Validate() != nil {
		t.Fatal(failed)
	}
}
