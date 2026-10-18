package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func registerRemoteTools(ctx context.Context, scope *gordis.Scope, runtime *tools.Runtime, session *rpc.Session, options HostOptions) error {
	client, err := newClient(session, options.Limits.MaxPending)
	if err != nil {
		return err
	}
	remoteTools, err := client.ListTools(ctx)
	if err != nil {
		return err
	}

	definitions := make([]tools.Definition, 0, len(remoteTools))
	for _, remote := range remoteTools {
		executeJSON := tools.JSONTool(func(callCtx context.Context, execution tools.Execution) (json.RawMessage, error) {
			return client.Call(callCtx, CallRequest{
				Tool:       remote.Name,
				Arguments:  execution.Arguments,
				ToolCallID: execution.Call.ID.String(),
			})
		})
		definitions = append(definitions, tools.Definition{
			Name:        remote.Name,
			Description: remote.Description,
			Parameters:  tools.JSONSchema(remote.Parameters),
			Output:      tools.JSONSchema(remote.Output),
			Timeout:     time.Duration(remote.TimeoutMillis) * time.Millisecond,
			FailureMode: tools.FailureMode(remote.FailureMode),
			Execute: func(callCtx context.Context, execution tools.Execution) (tools.ExecutionResult, error) {
				result, err := executeJSON(callCtx, execution)
				var callErr *CallError
				if errors.As(err, &callErr) && callErr.Code != CodeToolError && callErr.Code != CodeCancelled && callErr.Code != CodeTimeout {
					// Keep protocol/transport failures distinct from ordinary business
					// errors. Runtime still applies the definition's FailureMode.
					return tools.FailureResult(callErr.Code, callErr), nil
				}
				return result, err
			},
		})
	}
	if _, err := runtime.Bind(scope).RegisterBatch(definitions); err != nil {
		return fmt.Errorf("toolprocess: register Tools: %w", err)
	}
	return nil
}
