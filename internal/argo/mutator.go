package argo

import "context"

// Mutator is implemented by kubernetes and Argo CD API operators.
type Mutator interface {
	SetReadOnly(bool)
	ReadOnly() bool
	Refresh(ctx context.Context, r Resource, hard bool) error
	Sync(ctx context.Context, r Resource) error
	Suspend(ctx context.Context, r Resource) error
	Resume(ctx context.Context, r Resource) error
	Delete(ctx context.Context, r Resource) error
	YAML(r Resource) (string, error)
}

// Ensure *Operator implements Mutator.
var _ Mutator = (*Operator)(nil)
