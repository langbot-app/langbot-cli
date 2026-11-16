package app

import (
	"context"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func (s *Service) ProviderList(ctx context.Context, options CheckOptions) (Result, error) {
	preflight, err := s.readPreflight(ctx, "provider.list", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	providers, err := (api.Client{Transport: s.deps.Transport}).Providers(ctx, readTarget(preflight))
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"providers": providers}, Meta: preflight.Meta()}, nil
}

func (s *Service) ProviderGet(ctx context.Context, uuid string, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	preflight, err := s.readPreflight(ctx, "provider.get", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	provider, err := (api.Client{Transport: s.deps.Transport}).Provider(ctx, readTarget(preflight), uuid)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"provider": provider}, Meta: preflight.Meta()}, nil
}

func (s *Service) ProviderCreate(ctx context.Context, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if body == nil {
		return Result{}, result.New("input", i18n.T("Provider request body must be a JSON object"))
	}
	if _, exists := body["uuid"]; exists {
		return Result{}, result.New("input", i18n.T("Cannot specify uuid when creating a Provider"))
	}
	preflight, err := s.writePreflight(ctx, "provider.create", "provider_secret.manage", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "provider.create", ""), nil
	}
	client, target := s.writeClient(preflight)
	written, err := client.ProviderCreate(ctx, target, body)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	provider, err := client.Provider(ctx, target, written.UUID)
	data := writeResultData("provider.create", written.UUID)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	if !observableFieldsEqual(body, provider, "name", "requester") {
		return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Provider content differs after creation"))
	}
	data["provider"] = provider
	data["verified"] = true
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) ProviderUpdate(ctx context.Context, uuid string, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	if body == nil {
		return Result{}, result.New("input", i18n.T("Provider request body must be a JSON object"))
	}
	if err := validateBodyUUID(body, uuid); err != nil {
		return Result{}, err
	}
	if len(withoutUUID(body)) == 0 {
		return Result{}, result.New("input", i18n.T("Provider update contains no editable fields"))
	}
	preflight, err := s.writePreflight(ctx, "provider.update", "provider_secret.manage", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "provider.update", uuid), nil
	}
	client, target := s.writeClient(preflight)
	if err := client.ProviderUpdate(ctx, target, uuid, withoutUUID(body)); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	provider, err := client.Provider(ctx, target, uuid)
	data := writeResultData("provider.update", uuid)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	if !observableFieldsEqual(body, provider, "name", "requester") {
		return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Provider content differs after update"))
	}
	data["provider"] = provider
	data["verified"] = true
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) ProviderDelete(ctx context.Context, uuid string, confirmed, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	if !dryRun && !confirmed {
		return Result{}, result.New("input", i18n.T("Deleting a Provider requires --yes"))
	}
	preflight, err := s.writePreflight(ctx, "provider.delete", "resource.manage", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "provider.delete", uuid), nil
	}
	client, target := s.writeClient(preflight)
	if err := client.ProviderDelete(ctx, target, uuid); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data := writeResultData("provider.delete", uuid)
	_, err = client.Provider(ctx, target, uuid)
	if err != nil && result.AsError(err).Kind == "not_found" {
		data["verified"] = true
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Provider is still readable after deletion"))
}

func (s *Service) ProviderScanModels(ctx context.Context, uuid, modelType string, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	if modelType != "" {
		if modelType != api.ModelTypeLLM && modelType != api.ModelTypeEmbedding && modelType != api.ModelTypeRerank {
			return Result{}, result.New("input", i18n.T("provider scan-models type must be llm, embedding or rerank"))
		}
	}
	preflight, err := s.writePreflight(ctx, "provider.scan_models", "provider_secret.manage", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data := map[string]any{
		"operation":               "provider.scan_models",
		"provider_uuid":           uuid,
		"dry_run":                 dryRun,
		"server_write":            false,
		"preconditions_confirmed": true,
		"business_validation":     "not_run",
	}
	if modelType != "" {
		data["model_type"] = modelType
	}
	client, target := s.writeClient(preflight)
	if _, err := client.Provider(ctx, target, uuid); err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	data["business_validation"] = "target_exists"
	if dryRun {
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	scanned, err := client.ProviderScanModels(ctx, target, uuid, modelType)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	data["executed"] = true
	data["result"] = scanned
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) probeAudioModel(ctx context.Context, preflight *WritePreflight, operation string) error {
	parts := strings.Split(operation, ".")
	if len(parts) != 3 || parts[0] != "model" || parts[1] != "asr" && parts[1] != "tts" {
		return nil
	}
	_, err := (api.Client{Transport: s.deps.Transport}).Models(ctx, readTarget(*preflight), parts[1], "")
	if err != nil {
		if result.AsError(err).HTTPStatus == 404 || result.AsError(err).HTTPStatus == 405 {
			return result.New("incompatible", i18n.T("Current Core does not provide ")+parts[1]+i18n.T(" model routing"))
		}
		return err
	}
	for _, action := range []string{"list", "get", "create", "update", "delete", "test"} {
		preflight.Capabilities.Operations["model."+parts[1]+"."+action] = true
	}
	return nil
}

func (s *Service) ModelList(ctx context.Context, modelType, providerUUID string, options CheckOptions) (Result, error) {
	if modelType != "" {
		if err := api.ValidateModelType(modelType); err != nil {
			return Result{}, err
		}
	}
	if providerUUID != "" {
		if err := api.ValidateResourceID(providerUUID); err != nil {
			return Result{}, err
		}
	}
	if modelType != "" {
		return s.modelListOne(ctx, modelType, providerUUID, options)
	}
	combined := map[string]any{}
	preflight, err := s.readPreflight(ctx, "model.llm.list", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	client, target := s.writeClient(preflight)
	for _, currentType := range []string{api.ModelTypeLLM, api.ModelTypeEmbedding, api.ModelTypeRerank} {
		if currentType != api.ModelTypeLLM {
			if err := requireCapability(preflight, "model."+currentType+".list"); err != nil {
				return Result{Data: map[string]any{"models": combined}, Meta: preflight.Meta()}, err
			}
		}
		models, listErr := client.Models(ctx, target, currentType, providerUUID)
		if listErr != nil {
			return Result{Data: map[string]any{"models": combined}, Meta: preflight.Meta()}, listErr
		}
		combined[currentType] = models
	}
	return Result{Data: map[string]any{"models": combined}, Meta: preflight.Meta()}, nil
}

func (s *Service) modelListOne(ctx context.Context, modelType, providerUUID string, options CheckOptions) (Result, error) {
	operation := "model." + modelType + ".list"
	preflight, err := s.readPreflight(ctx, operation, "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	models, err := (api.Client{Transport: s.deps.Transport}).Models(ctx, readTarget(preflight), modelType, providerUUID)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"model_type": modelType, "models": models}, Meta: preflight.Meta()}, nil
}

func (s *Service) ModelGet(ctx context.Context, modelType, uuid string, options CheckOptions) (Result, error) {
	if err := api.ValidateModelType(modelType); err != nil {
		return Result{}, err
	}
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	operation := "model." + modelType + ".get"
	preflight, err := s.readPreflight(ctx, operation, "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	model, err := (api.Client{Transport: s.deps.Transport}).Model(ctx, readTarget(preflight), modelType, uuid)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"model_type": modelType, "model": model}, Meta: preflight.Meta()}, nil
}

func (s *Service) ModelCreate(ctx context.Context, modelType string, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateModelType(modelType); err != nil {
		return Result{}, err
	}
	if body == nil {
		return Result{}, result.New("input", i18n.T("Model request body must be a JSON object"))
	}
	if _, exists := body["uuid"]; exists {
		return Result{}, result.New("input", i18n.T("Cannot specify uuid when creating a Model"))
	}
	operation := "model." + modelType + ".create"
	preflight, err := s.writePreflight(ctx, operation, "provider_secret.manage", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, operation, ""), nil
	}
	client, target := s.writeClient(preflight)
	written, err := client.ModelCreate(ctx, target, modelType, body)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	model, err := client.Model(ctx, target, modelType, written.UUID)
	data := writeResultData(operation, written.UUID)
	data["model_type"] = modelType
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	if !observableModelFieldsEqual(body, model) {
		return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Model content differs after creation"))
	}
	data["model"] = model
	data["verified"] = true
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) ModelUpdate(ctx context.Context, modelType, uuid string, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateModelType(modelType); err != nil {
		return Result{}, err
	}
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	if body == nil {
		return Result{}, result.New("input", i18n.T("Model request body must be a JSON object"))
	}
	if err := validateBodyUUID(body, uuid); err != nil {
		return Result{}, err
	}
	if len(withoutUUID(body)) == 0 {
		return Result{}, result.New("input", i18n.T("Model update contains no editable fields"))
	}
	operation := "model." + modelType + ".update"
	preflight, err := s.writePreflight(ctx, operation, "provider_secret.manage", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, operation, uuid), nil
	}
	client, target := s.writeClient(preflight)
	if err := client.ModelUpdate(ctx, target, modelType, uuid, withoutUUID(body)); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	model, err := client.Model(ctx, target, modelType, uuid)
	data := writeResultData(operation, uuid)
	data["model_type"] = modelType
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	if !observableModelFieldsEqual(body, model) {
		return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Model content differs after update"))
	}
	data["model"] = model
	data["verified"] = true
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) ModelDelete(ctx context.Context, modelType, uuid string, confirmed, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateModelType(modelType); err != nil {
		return Result{}, err
	}
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	if !dryRun && !confirmed {
		return Result{}, result.New("input", i18n.T("Deleting a Model requires --yes"))
	}
	operation := "model." + modelType + ".delete"
	preflight, err := s.writePreflight(ctx, operation, "resource.manage", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, operation, uuid), nil
	}
	client, target := s.writeClient(preflight)
	if err := client.ModelDelete(ctx, target, modelType, uuid); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data := writeResultData(operation, uuid)
	data["model_type"] = modelType
	_, err = client.Model(ctx, target, modelType, uuid)
	if err != nil && result.AsError(err).Kind == "not_found" {
		data["verified"] = true
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Model is still readable after deletion"))
}

func (s *Service) ModelTest(ctx context.Context, modelType, uuid string, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateModelType(modelType); err != nil {
		return Result{}, err
	}
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	operation := "model." + modelType + ".test"
	preflight, err := s.writePreflight(ctx, operation, "provider_secret.manage", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data := map[string]any{
		"operation":               operation,
		"model_type":              modelType,
		"model_uuid":              uuid,
		"dry_run":                 dryRun,
		"server_write":            false,
		"preconditions_confirmed": true,
		"business_validation":     "not_run",
	}
	client, target := s.writeClient(preflight)
	if _, err := client.Model(ctx, target, modelType, uuid); err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	data["business_validation"] = "target_exists"
	if dryRun {
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	testResult, err := client.ModelTest(ctx, target, modelType, uuid, body)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	data["executed"] = true
	data["result"] = testResult
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func observableModelFieldsEqual(body, model map[string]any) bool {
	return observableFieldsEqual(
		body,
		model,
		"name", "provider_uuid", "abilities", "context_length", "reasoning_config", "reasoning_capabilities",
		"extra_args", "prefered_ranking",
	)
}
