package managed

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

type Model struct {
	Provider  string `json:"provider"`
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Available bool   `json:"available"`
}
type Catalog struct {
	AuthRevision int64
	Models       []Model
}
type ModelSource interface {
	Catalog(context.Context) (Catalog, error)
}
type ModelSelector interface {
	Select(context.Context, *Model) (Model, error)
}
type ModelSourceFunc func(context.Context) (Catalog, error)

func (f ModelSourceFunc) Catalog(ctx context.Context) (Catalog, error) { return f(ctx) }

type modelUnavailableError struct{ Provider, ID string }

func (e *modelUnavailableError) Error() string {
	return fmt.Sprintf("model %s/%s is unavailable", e.Provider, e.ID)
}
func (e *modelUnavailableError) Code() string { return "model_unavailable" }
func IsModelUnavailable(err error) bool {
	var target *modelUnavailableError
	return errors.As(err, &target)
}

var modelIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func ValidateSelectedModel(model Model) error {
	if model.Provider == "" || !modelIDRE.MatchString(model.Provider) || model.ID == "" || !modelIDRE.MatchString(model.ID) {
		return errors.New("invalid model")
	}
	return nil
}

func SelectModel(explicit *Model, saved *store.ManagedModel, catalog Catalog) (Model, error) {
	available := func(candidate Model) bool {
		for _, model := range catalog.Models {
			if model.Provider == candidate.Provider && model.ID == candidate.ID && model.Available {
				return true
			}
		}
		return false
	}
	if explicit != nil {
		if err := ValidateSelectedModel(*explicit); err != nil {
			return Model{}, err
		}
		if !available(*explicit) {
			return Model{}, &modelUnavailableError{explicit.Provider, explicit.ID}
		}
		return *explicit, nil
	}
	if saved != nil {
		candidate := Model{Provider: saved.Provider, ID: saved.ID}
		if available(candidate) {
			return candidate, nil
		}
	}
	// An omitted model delegates to the router's built-in Pi default. Provider
	// catalogs gate explicit choices, but must not turn the optional launch field
	// into a validation error while a catalog refresh or sign-in is unavailable.
	return Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Available: true}, nil
}

type StoreCatalogSource struct {
	Store  *store.Store
	Helper ModelSource
}

func (s StoreCatalogSource) Catalog(ctx context.Context) (Catalog, error) {
	if s.Store == nil {
		return Catalog{}, errors.New("managed model store is unavailable")
	}
	persisted, err := s.Store.ManagedProviderSnapshot(ctx)
	if err != nil {
		return Catalog{}, err
	}
	// Once provider orchestration has committed models, the router's durable
	// catalog is launch truth. A compatibility helper is used only by older test
	// seams; production passes no helper and therefore cannot launch from a
	// signed-out migration row.
	if len(persisted.Providers) > 0 && (len(persisted.Models) > 0 || s.Helper == nil) {
		models := make([]Model, 0, len(persisted.Models))
		for _, model := range persisted.Models {
			models = append(models, Model{Provider: model.Provider, ID: model.ID, Name: model.Name, Available: model.Available})
		}
		return Catalog{AuthRevision: persisted.AuthRevision, Models: models}, nil
	}
	if s.Helper == nil {
		return Catalog{AuthRevision: persisted.AuthRevision, Models: []Model{}}, nil
	}
	catalog, err := s.Helper.Catalog(ctx)
	if err != nil {
		return Catalog{}, err
	}
	catalog.AuthRevision = persisted.AuthRevision
	return catalog, nil
}

func (s StoreCatalogSource) DefaultModel(ctx context.Context) (*store.ManagedModel, error) {
	if s.Store == nil {
		return nil, errors.New("managed model store is unavailable")
	}
	return s.Store.GetDefaultModel(ctx)
}

// Select is the production boundary: one helper snapshot is combined with the
// persisted router default before a child reservation is created.
func (s StoreCatalogSource) Select(ctx context.Context, explicit *Model) (Model, error) {
	catalog, err := s.Catalog(ctx)
	if err != nil {
		return Model{}, err
	}
	saved, err := s.DefaultModel(ctx)
	if err != nil {
		return Model{}, err
	}
	return SelectModel(explicit, saved, catalog)
}
func CatalogFromModels(models ...Model) ModelSource {
	return ModelSourceFunc(func(context.Context) (Catalog, error) { return Catalog{Models: append([]Model(nil), models...)}, nil })
}
