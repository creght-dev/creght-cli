package creght

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// 导出包 v2：把一个站点整个搬到另一个 creght 部署（独立部署版或别的集群）。
// 格式见 weave docs/architecture/selfhost-export.md。

// packageHTTP 不设总超时：导出要边下载素材边写，导入要上传整个包，几分钟都正常；靠 ctx 取消。
var packageHTTP = &http.Client{}

// ExportProjectPackage 把导出包（zip）写到 w。with 可选 "members"、"form_logs"。
func (c *Client) ExportProjectPackage(ctx context.Context, projectID string, siteIDs, with []string, w io.Writer) (int64, error) {
	q := url.Values{}
	if len(siteIDs) > 0 {
		q.Set("site_ids", strings.Join(siteIDs, ","))
	}
	if len(with) > 0 {
		q.Set("with", strings.Join(with, ","))
	}
	path := fmt.Sprintf("/api/p/project/%s/export_package", url.PathEscape(projectID))
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	if err := c.setAuth(ctx, req); err != nil {
		return 0, err
	}
	resp, err := packageHTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 || !strings.Contains(resp.Header.Get("Content-Type"), "zip") {
		return 0, c.packageError("GET", path, resp)
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("download package: %w", err)
	}
	return n, nil
}

type ImportPackageOptions struct {
	NewProjectName string
	SkipMembers    bool
	BindDomains    bool
}

// ImportProjectPackage 上传导出包建一个新项目，返回接口的结果（project_id、sites、missing_secrets…）。
func (c *Client) ImportProjectPackage(ctx context.Context, fileName string, pkg io.Reader, opts ImportPackageOptions) (json.RawMessage, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			fields := map[string]string{"new_project_name": opts.NewProjectName}
			if opts.SkipMembers {
				fields["skip_members"] = "1"
			}
			if opts.BindDomains {
				fields["bind_domains"] = "1"
			}
			for k, v := range fields {
				if v == "" {
					continue
				}
				if err := mw.WriteField(k, v); err != nil {
					return err
				}
			}
			fw, err := mw.CreateFormFile("file", fileName)
			if err != nil {
				return err
			}
			if _, err := io.Copy(fw, pkg); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()

	path := "/api/p/project/import_package"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	if err := c.setAuth(ctx, req); err != nil {
		return nil, err
	}
	resp, err := packageHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, c.packageError("POST", path, resp)
	}
	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(bs), nil
}

func (c *Client) packageError(method, path string, resp *http.Response) error {
	bs, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var apiErr struct {
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	_ = json.Unmarshal(bs, &apiErr)
	msg := apiErr.Message
	if msg == "" {
		msg = apiErr.Msg
	}
	if msg == "" {
		msg = strings.TrimSpace(string(bytes.TrimSpace(bs)))
	}
	text := fmt.Sprintf("%s %s: status %d", method, path, resp.StatusCode)
	if msg != "" {
		text = fmt.Sprintf("%s %s: %s", method, path, msg)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return &AuthError{Message: text, Hint: c.authHint}
	}
	return errors.New(text)
}
