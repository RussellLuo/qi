package llm

import (
	"fmt"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/registry"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
)

// RegisterModels binds the product directory to the same Scope as its adapter.
// Queries copy values; prepared calls keep the existing adapter-generation lease.
func (r *Runtime) RegisterModels(owner contracts.ContributionOwner, models []modelcatalog.Spec) error {
	if len(models) == 0 {
		return nil
	}

	batch := make([]registry.Registration[modelcatalog.Spec], len(models))

	for i, model := range models {
		batch[i] = registry.Registration[modelcatalog.Spec]{Key: model.ID, Value: model}
	}

	_, err := r.models.RegisterBatch(owner, batch)

	return err
}

func (r *Runtime) RegisterDefaults(owner contracts.ContributionOwner, defaults modelcatalog.Defaults) error {
	_, err := r.defaults.Register(owner, "agent.defaults", 0, defaults)
	return err
}

func (r *Runtime) ModelCatalog() ([]modelcatalog.Spec, string, error) {
	models, err := r.models.Prepare()
	if err != nil {
		return nil, "", err
	}

	defer models.Close()

	defaults, err := r.defaults.Prepare()
	if err != nil {
		return nil, "", err
	}

	defer defaults.Close()

	result := []modelcatalog.Spec{}

	for _, item := range models.Items() {
		result = append(result, item.Value)
	}

	selected := ""

	for _, item := range defaults.Items() {
		selected = item.Value.Model
	}

	return result, selected, nil
}

func (r *Runtime) ResolveModel(id string) (modelcatalog.Spec, error) {
	models, selected, err := r.ModelCatalog()
	if err != nil {
		return modelcatalog.Spec{}, err
	}
	if id == "" {
		id = selected
	}

	for _, model := range models {
		if model.ID == id {
			return model, nil
		}
	}

	return modelcatalog.Spec{}, fmt.Errorf("model %q is not declared by the active provider catalog", id)
}

func (r *Runtime) DefaultSelection() (modelcatalog.Defaults, error) {
	snapshot, err := r.defaults.Prepare()
	if err != nil {
		return modelcatalog.Defaults{}, err
	}

	defer snapshot.Close()

	var selected modelcatalog.Defaults

	for _, item := range snapshot.Items() {
		selected = item.Value
	}

	return selected, nil
}
