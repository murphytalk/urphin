package commands

import "context"

type Command interface {
	Name() string
	Execute(ctx context.Context, args map[string]any) error
}
