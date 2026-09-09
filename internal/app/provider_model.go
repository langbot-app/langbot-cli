package app

import (
	"context"

	"github.com/langbot-app/langbot-cli/internal/api"
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
		return Result{}, result.New("input", "Provider 请求体必须是 JSON object")
	}
	if _, exists := body["uuid"]; exists {
		return Result{}, result.New("input", "创建 Provider 时不能指定 uuid")
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
		return Result{Data: data, Meta: preflight.Meta()}, verificationError("创建后回读到的 Provider 内容不一致")
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
		return Result{}, result.New("input", "Provider 请求体必须是 JSON object")
	}
	if err := validateBodyUUID(body, uuid); err != nil {
		return Result{}, err
	}
	if len(withoutUUID(body)) == 0 {
		return Result{}, result.New("input", "Provider 更新请求没有可修改字段")
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
		return Result{Data: data, Meta: preflight.Meta()}, verificationError("更新后回读到的 Provider 内容不一致")
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
		return Result{}, result.New("input", "删除 Provider 需要 --yes")
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
	return Result{Data: data, Meta: preflight.Meta()}, verificationError("删除后 Provider 仍可读取")
}

func (s *Service) ProviderScanModels(ctx context.Context, uuid, modelType string, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	if modelType != "" {
		if err := api.ValidateModelType(modelType); err != nil {
			return Result{}, err
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
		return Result{}, result.New("input", "Model 请求体必须是 JSON object")
	}
	if _, exists := body["uuid"]; exists {
		return Result{}, result.New("input", "创建 Model 时不能指定 uuid")
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
		return Result{Data: data, Meta: preflight.Meta()}, verificationError("创建后回读到的 Model 内容不一致")
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
		return Result{}, result.New("input", "Model 请求体必须是 JSON object")
	}
	if err := validateBodyUUID(body, uuid); err != nil {
		return Result{}, err
	}
	if len(withoutUUID(body)) == 0 {
		return Result{}, result.New("input", "Model 更新请求没有可修改字段")
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
		return Result{Data: data, Meta: preflight.Meta()}, verificationError("更新后回读到的 Model 内容不一致")
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
		return Result{}, result.New("input", "删除 Model 需要 --yes")
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
	return Result{Data: data, Meta: preflight.Meta()}, verificationError("删除后 Model 仍可读取")
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
