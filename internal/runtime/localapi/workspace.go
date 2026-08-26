package localapi

import (
	"context"
	"encoding/json"
	"fmt"
)

// bindResolvedProjectArguments adapts legacy handler inputs to the Gateway-
// owned project selected by the exact resolved workspace. Public callers never
// supply this field; Task 3 replaces the legacy constructors around it.
func bindResolvedProjectArguments(ctx context.Context, public json.RawMessage) (json.RawMessage, error) {
	binding, err := ResolvedBinding(ctx)
	if err != nil {
		return nil, err
	}
	var arguments map[string]json.RawMessage
	if len(public) == 0 {
		arguments = map[string]json.RawMessage{}
	} else if err := json.Unmarshal(public, &arguments); err != nil || arguments == nil {
		return nil, fmt.Errorf("localapi: public tool arguments must be an object")
	}
	projectID, err := json.Marshal(binding.Scope.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("localapi: encode resolved project: %w", err)
	}
	arguments["project_id"] = projectID
	bound, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("localapi: encode resolved arguments: %w", err)
	}
	return bound, nil
}
