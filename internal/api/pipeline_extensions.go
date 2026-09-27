package api

import (
	"context"
	"net/http"
	"net/url"
)

func resolvePipelineExtensionOperation(path string) (Operation, bool) {
	return Operation{ID: "pipeline.extensions.get", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, nestedPath(path, pipelinesPath, 1, "extensions")
}
func (c Client) PipelineExtensionsUpdate(ctx context.Context, target Target, id string, body map[string]any) error {
	if err := ValidateResourceID(id); err != nil {
		return err
	}
	_, err := c.write(ctx, target, http.MethodPut, pipelinesPath+"/"+url.PathEscape(id)+"/extensions", body)
	return err
}
