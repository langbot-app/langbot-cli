package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func (c Client) ImageDownload(ctx context.Context, target Target, key string, destination io.Writer) error {
	if key == "" || strings.ContainsAny(key, "\\\r\n") {
		return result.New("input", "图片 key 无效")
	}
	segments := strings.Split(key, "/")
	for index, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return result.New("input", "图片 key 无效")
		}
		segments[index] = url.PathEscape(segment)
	}
	response, cancel, err := c.applicationRequest(ctx, target, http.MethodGet, "/api/v1/files/image/"+strings.Join(segments, "/"), nil, "")
	if err != nil {
		return err
	}
	defer cancel()
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, err = applicationResponse(response, target, false)
		return err
	}
	if _, err := io.Copy(destination, response.Body); err != nil {
		return result.New("network", "读取或写入图片失败")
	}
	return nil
}
