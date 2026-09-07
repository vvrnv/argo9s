package argocd

import (
	"context"
	"fmt"

	"sigs.k8s.io/yaml"

	"github.com/vvrnv/argo9s/internal/argo"
)

// Operator mutates Applications via the Argo CD API.
type Operator struct {
	client   *Client
	readOnly bool
}

func NewOperator(client *Client, readOnly bool) *Operator {
	return &Operator{client: client, readOnly: readOnly}
}

var _ argo.Mutator = (*Operator)(nil)

func (o *Operator) SetReadOnly(v bool) { o.readOnly = v }
func (o *Operator) ReadOnly() bool     { return o.readOnly }

func (o *Operator) guard() error {
	if o.readOnly {
		return fmt.Errorf("read-only mode (toggle with :readonly)")
	}
	return nil
}

func (o *Operator) Refresh(ctx context.Context, r argo.Resource, hard bool) error {
	if err := o.guard(); err != nil {
		return err
	}
	if r.Kind != argo.KindApplication {
		return fmt.Errorf("refresh is only supported for Applications")
	}
	return o.client.RefreshApplication(ctx, r.Name, r.Namespace, hard)
}

func (o *Operator) Sync(ctx context.Context, r argo.Resource) error {
	if err := o.guard(); err != nil {
		return err
	}
	if r.Kind != argo.KindApplication {
		return fmt.Errorf("sync is only supported for Applications")
	}
	return o.client.SyncApplication(ctx, r.Name, r.Namespace)
}

func (o *Operator) Suspend(ctx context.Context, r argo.Resource) error {
	if err := o.guard(); err != nil {
		return err
	}
	if r.Kind != argo.KindApplication {
		return fmt.Errorf("suspend is only supported for Applications")
	}
	app, err := o.client.GetApplication(ctx, r.Name, r.Namespace)
	if err != nil {
		return err
	}
	spec, _ := app["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
		app["spec"] = spec
	}
	syncPolicy, _ := spec["syncPolicy"].(map[string]any)
	if syncPolicy == nil {
		return nil
	}
	delete(syncPolicy, "automated")
	spec["syncPolicy"] = syncPolicy
	return o.client.UpdateApplication(ctx, app)
}

func (o *Operator) Resume(ctx context.Context, r argo.Resource) error {
	if err := o.guard(); err != nil {
		return err
	}
	if r.Kind != argo.KindApplication {
		return fmt.Errorf("resume is only supported for Applications")
	}
	app, err := o.client.GetApplication(ctx, r.Name, r.Namespace)
	if err != nil {
		return err
	}
	spec, _ := app["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
		app["spec"] = spec
	}
	syncPolicy, _ := spec["syncPolicy"].(map[string]any)
	if syncPolicy == nil {
		syncPolicy = map[string]any{}
	}
	syncPolicy["automated"] = map[string]any{"prune": true, "selfHeal": true}
	spec["syncPolicy"] = syncPolicy
	return o.client.UpdateApplication(ctx, app)
}

func (o *Operator) Delete(ctx context.Context, r argo.Resource) error {
	if err := o.guard(); err != nil {
		return err
	}
	if r.Kind != argo.KindApplication {
		return fmt.Errorf("delete via API currently supports Applications only")
	}
	return o.client.DeleteApplication(ctx, r.Name, r.Namespace)
}

func (o *Operator) YAML(r argo.Resource) (string, error) {
	if r.Raw == nil {
		return "", fmt.Errorf("no raw object")
	}
	obj := r.Raw.DeepCopy()
	obj.SetManagedFields(nil)
	data, err := yaml.Marshal(obj.Object)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
