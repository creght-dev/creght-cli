package creght

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL string, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   strings.TrimSpace(token),
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method string, path string, query url.Values, body any, out any) error {
	var reader io.Reader
	if body != nil {
		bs, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(bs)
	}

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr APIError
		_ = json.Unmarshal(bs, &apiErr)
		if apiErr.Message != "" {
			return fmt.Errorf("%s %s: %s", method, path, apiErr.Message)
		}
		return fmt.Errorf("%s %s: status %d", method, path, resp.StatusCode)
	}

	if out == nil || len(bs) == 0 {
		return nil
	}

	err = json.Unmarshal(bs, out)
	if err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	return nil
}

type CLIAuthSession struct {
	Code      string `json:"code"`
	VerifyURL string `json:"verify_url"`
	ExpiresIn int    `json:"expires_in"`
}

type CLIAuthSessionResult struct {
	Status string `json:"status"`
	Token  string `json:"token"`
	UserID int64  `json:"user_id"`
}

func (c *Client) CreateCLIAuthSession(ctx context.Context, webURL string) (CLIAuthSession, error) {
	var ret CLIAuthSession
	err := c.do(ctx, http.MethodPost, "/api/u/cli/auth/session", nil, map[string]any{
		"web_url": strings.TrimSpace(webURL),
	}, &ret)
	if err != nil {
		return CLIAuthSession{}, err
	}

	return ret, nil
}

func (c *Client) GetCLIAuthSession(ctx context.Context, code string) (CLIAuthSessionResult, error) {
	var ret CLIAuthSessionResult
	err := c.do(ctx, http.MethodGet, "/api/u/cli/auth/session/"+url.PathEscape(code), nil, nil, &ret)
	if err != nil {
		return CLIAuthSessionResult{}, err
	}

	return ret, nil
}

type Site struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Project struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	SiteList []Site `json:"site_list"`
}

type ProjectListResponse struct {
	Total int       `json:"total"`
	List  []Project `json:"list"`
}

type CreateProjectRequest struct {
	Name   string `json:"name"`
	FromID string `json:"from_id,omitempty"`
	TplID  int64  `json:"tpl_id,omitempty"`
}

func (c *Client) GetProjectList(ctx context.Context) (ProjectListResponse, error) {
	var ret ProjectListResponse
	err := c.do(ctx, http.MethodGet, "/api/u/project_list", nil, nil, &ret)
	if err != nil {
		return ProjectListResponse{}, err
	}

	return ret, nil
}

func (c *Client) CreateProject(ctx context.Context, project CreateProjectRequest) (string, error) {
	var ret IDResponse
	err := c.do(ctx, http.MethodPost, "/api/u/project", nil, project, &ret)
	if err != nil {
		return "", err
	}

	return ret.ID, nil
}

type ContentApp struct {
	ID         string          `json:"id,omitempty"`
	ProjectID  string          `json:"project_id,omitempty"`
	Key        string          `json:"key,omitempty"`
	UserID     int64           `json:"user_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Desc       string          `json:"desc,omitempty"`
	JsonSchema json.RawMessage `json:"json_schema,omitempty"`
	Visibility string          `json:"visibility,omitempty"`
	CreatedAt  string          `json:"created_at,omitempty"`
	UpdatedAt  string          `json:"updated_at,omitempty"`
}

type Content struct {
	ID           string          `json:"id,omitempty"`
	Slug         string          `json:"slug,omitempty"`
	ContentAppID string          `json:"content_app_id,omitempty"`
	UserID       int64           `json:"user_id,omitempty"`
	JsonSchema   json.RawMessage `json:"json_schema,omitempty"`
	Tags         []string        `json:"tags,omitempty"`
	Status       string          `json:"status,omitempty"`
	Body         json.RawMessage `json:"body,omitempty"`
	Sort         int             `json:"sort,omitempty"`
	CreatedAt    string          `json:"created_at,omitempty"`
	UpdatedAt    string          `json:"updated_at,omitempty"`
}

type Form struct {
	ID         string          `json:"id,omitempty"`
	ProjectID  string          `json:"project_id,omitempty"`
	Key        string          `json:"key,omitempty"`
	UserID     int64           `json:"user_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Desc       string          `json:"desc,omitempty"`
	JsonSchema json.RawMessage `json:"json_schema,omitempty"`
	Setting    json.RawMessage `json:"setting,omitempty"`
	CreatedAt  string          `json:"created_at,omitempty"`
	UpdatedAt  string          `json:"updated_at,omitempty"`
}

type FormLog struct {
	ID        string          `json:"id,omitempty"`
	FormID    string          `json:"form_id,omitempty"`
	UID       string          `json:"uid,omitempty"`
	UA        string          `json:"ua,omitempty"`
	IP        string          `json:"ip,omitempty"`
	FormURL   string          `json:"form_url,omitempty"`
	Body      json.RawMessage `json:"body,omitempty"`
	CreatedAt string          `json:"created_at,omitempty"`
	UpdatedAt string          `json:"updated_at,omitempty"`
}

type ListResponse[T any] struct {
	Total   int64 `json:"total"`
	HasMore bool  `json:"has_more,omitempty"`
	List    []T   `json:"list"`
}

type IDResponse struct {
	ID string `json:"id"`
}

type AssetPreUploadRequest struct {
	FileName     string `json:"file_name"`
	Hash         string `json:"hash"`
	Mimetype     string `json:"mimetype"`
	Size         int    `json:"size"`
	From         string `json:"from,omitempty"`
	SourceURL    string `json:"source_url,omitempty"`
	CacheControl string `json:"cache_control,omitempty"`
}

type AssetPreUploadResponse struct {
	HashExist    bool   `json:"hash_exist"`
	PresignedURL string `json:"presigned_url"`
	FilePath     string `json:"file_path"`
	SitePath     string `json:"site_path"`
	FileURL      string `json:"file_url"`
	ID           int64  `json:"id"`
}

func (c *Client) GetCMSCollectionList(ctx context.Context, projectID string, query url.Values) (ListResponse[ContentApp], error) {
	var ret ListResponse[ContentApp]
	path := fmt.Sprintf("/api/u/project/%s/cms_list", url.PathEscape(projectID))
	err := c.do(ctx, http.MethodGet, path, query, nil, &ret)
	if err != nil {
		return ListResponse[ContentApp]{}, err
	}

	return ret, nil
}

func (c *Client) GetCMSCollection(ctx context.Context, projectID string, appID string) (ContentApp, error) {
	var ret ContentApp
	path := fmt.Sprintf("/api/u/project/%s/cms/%s", url.PathEscape(projectID), url.PathEscape(appID))
	err := c.do(ctx, http.MethodGet, path, nil, nil, &ret)
	if err != nil {
		return ContentApp{}, err
	}

	return ret, nil
}

func (c *Client) GetCMSCollectionByKey(ctx context.Context, projectID string, key string) (ContentApp, error) {
	var ret ContentApp
	path := fmt.Sprintf("/api/u/v2/project/%s/cms/%s", url.PathEscape(projectID), url.PathEscape(key))
	err := c.do(ctx, http.MethodGet, path, nil, nil, &ret)
	if err != nil {
		return ContentApp{}, err
	}

	return ret, nil
}

func (c *Client) CreateCMSCollection(ctx context.Context, projectID string, collection ContentApp) (string, error) {
	var ret IDResponse
	path := fmt.Sprintf("/api/u/project/%s/cms", url.PathEscape(projectID))
	err := c.do(ctx, http.MethodPost, path, nil, collection, &ret)
	if err != nil {
		return "", err
	}

	return ret.ID, nil
}

func (c *Client) UpdateCMSCollection(ctx context.Context, projectID string, appID string, collection ContentApp) error {
	path := fmt.Sprintf("/api/u/project/%s/cms/%s", url.PathEscape(projectID), url.PathEscape(appID))
	return c.do(ctx, http.MethodPut, path, nil, collection, nil)
}

func (c *Client) DeleteCMSCollection(ctx context.Context, projectID string, appID string) error {
	path := fmt.Sprintf("/api/u/project/%s/cms/%s", url.PathEscape(projectID), url.PathEscape(appID))
	return c.do(ctx, http.MethodDelete, path, nil, nil, nil)
}

func (c *Client) GetContentList(ctx context.Context, projectID string, appID string, query url.Values, body any) (ListResponse[Content], error) {
	var ret ListResponse[Content]
	path := fmt.Sprintf("/api/u/project/%s/cms/%s/content_list", url.PathEscape(projectID), url.PathEscape(appID))
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	err := c.do(ctx, method, path, query, body, &ret)
	if err != nil {
		return ListResponse[Content]{}, err
	}

	return ret, nil
}

func (c *Client) GetContent(ctx context.Context, projectID string, appID string, query url.Values) (Content, error) {
	var ret Content
	path := fmt.Sprintf("/api/u/project/%s/cms/%s/content", url.PathEscape(projectID), url.PathEscape(appID))
	err := c.do(ctx, http.MethodGet, path, query, nil, &ret)
	if err != nil {
		return Content{}, err
	}

	return ret, nil
}

func (c *Client) CreateContent(ctx context.Context, projectID string, appID string, content Content) (string, error) {
	var ret IDResponse
	path := fmt.Sprintf("/api/u/project/%s/cms/%s/content", url.PathEscape(projectID), url.PathEscape(appID))
	body, err := contentRequestBody(content, true)
	if err != nil {
		return "", err
	}
	err = c.do(ctx, http.MethodPost, path, nil, body, &ret)
	if err != nil {
		return "", err
	}

	return ret.ID, nil
}

func (c *Client) UpdateContent(ctx context.Context, projectID string, appID string, content Content, publish bool) error {
	path := fmt.Sprintf("/api/u/project/%s/cms/%s/content", url.PathEscape(projectID), url.PathEscape(appID))
	body, err := contentRequestBody(content, publish)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, path, nil, body, nil)
}

func (c *Client) DeleteContent(ctx context.Context, projectID string, appID string, contentID string) error {
	path := fmt.Sprintf("/api/u/project/%s/cms/%s/content", url.PathEscape(projectID), url.PathEscape(appID))
	return c.do(ctx, http.MethodDelete, path, nil, map[string]string{"id": contentID}, nil)
}

func (c *Client) GetFormList(ctx context.Context, projectID string, query url.Values) (ListResponse[Form], error) {
	var ret ListResponse[Form]
	path := fmt.Sprintf("/api/u/project/%s/form_list", url.PathEscape(projectID))
	err := c.do(ctx, http.MethodGet, path, query, nil, &ret)
	if err != nil {
		return ListResponse[Form]{}, err
	}

	return ret, nil
}

func (c *Client) GetForm(ctx context.Context, projectID string, formID string) (Form, error) {
	var ret Form
	path := fmt.Sprintf("/api/u/project/%s/form/%s", url.PathEscape(projectID), url.PathEscape(formID))
	err := c.do(ctx, http.MethodGet, path, nil, nil, &ret)
	if err != nil {
		return Form{}, err
	}

	return ret, nil
}

func (c *Client) CreateForm(ctx context.Context, projectID string, form Form) (string, error) {
	var ret IDResponse
	path := fmt.Sprintf("/api/u/project/%s/form", url.PathEscape(projectID))
	err := c.do(ctx, http.MethodPost, path, nil, form, &ret)
	if err != nil {
		return "", err
	}

	return ret.ID, nil
}

func (c *Client) UpdateForm(ctx context.Context, projectID string, formID string, form Form) error {
	path := fmt.Sprintf("/api/u/project/%s/form/%s", url.PathEscape(projectID), url.PathEscape(formID))
	return c.do(ctx, http.MethodPut, path, nil, form, nil)
}

func (c *Client) DeleteForm(ctx context.Context, projectID string, formID string) error {
	path := fmt.Sprintf("/api/u/project/%s/form/%s", url.PathEscape(projectID), url.PathEscape(formID))
	return c.do(ctx, http.MethodDelete, path, nil, nil, nil)
}

func (c *Client) GetFormLogList(ctx context.Context, projectID string, formID string, query url.Values) (ListResponse[FormLog], error) {
	var ret ListResponse[FormLog]
	path := fmt.Sprintf("/api/u/project/%s/form/%s/form_log_list", url.PathEscape(projectID), url.PathEscape(formID))
	err := c.do(ctx, http.MethodGet, path, query, nil, &ret)
	if err != nil {
		return ListResponse[FormLog]{}, err
	}

	return ret, nil
}

func (c *Client) GetFormLog(ctx context.Context, projectID string, formID string, logID string) (FormLog, error) {
	var ret FormLog
	path := fmt.Sprintf("/api/u/project/%s/form/%s/form_log", url.PathEscape(projectID), url.PathEscape(formID))
	err := c.do(ctx, http.MethodGet, path, url.Values{"id": []string{logID}}, nil, &ret)
	if err != nil {
		return FormLog{}, err
	}

	return ret, nil
}

func (c *Client) DeleteFormLog(ctx context.Context, projectID string, formID string, logID string) error {
	path := fmt.Sprintf("/api/u/project/%s/form/%s/form_log", url.PathEscape(projectID), url.PathEscape(formID))
	return c.do(ctx, http.MethodDelete, path, nil, map[string]string{"id": logID}, nil)
}

func (c *Client) SubmitForm(ctx context.Context, projectID string, formKey string, data map[string]any) error {
	path := fmt.Sprintf("/api/u/v2/project/%s/form/%s/submit", url.PathEscape(projectID), url.PathEscape(formKey))
	return c.do(ctx, http.MethodPost, path, nil, data, nil)
}

func (c *Client) PreUploadSiteAsset(ctx context.Context, projectID string, siteID string, req AssetPreUploadRequest) (AssetPreUploadResponse, error) {
	var ret AssetPreUploadResponse
	path := fmt.Sprintf("/api/u/project/%s/site/%s/file/s3_pre_upload", url.PathEscape(projectID), url.PathEscape(siteID))
	err := c.do(ctx, http.MethodPost, path, nil, req, &ret)
	if err != nil {
		return AssetPreUploadResponse{}, err
	}

	return ret, nil
}

func (c *Client) AckS3FileUpload(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodPost, "/api/u/file/ack_s3_upload", nil, map[string]int64{"id": id}, nil)
}

func contentRequestBody(content Content, publish bool) (map[string]any, error) {
	bs, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal content request: %w", err)
	}
	var body map[string]any
	if err := json.Unmarshal(bs, &body); err != nil {
		return nil, fmt.Errorf("build content request: %w", err)
	}
	body["publish"] = publish
	return body, nil
}

type SystemInfo struct {
	SelfAPIHost  string       `json:"self_api_host"`
	RenderConfig RenderConfig `json:"render_config"`
}

type RenderConfig struct {
	ImportMap       map[string]string `json:"import_map"`
	DevImportMap    map[string]string `json:"dev_import_map"`
	IgnoreImportMap []string          `json:"ignore_import_map"`
}

func (c *Client) GetSystemInfo(ctx context.Context) (SystemInfo, error) {
	var ret SystemInfo
	err := c.do(ctx, http.MethodGet, "/api/u/system/info", nil, nil, &ret)
	if err != nil {
		return SystemInfo{}, err
	}

	return ret, nil
}

type File struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Body     string `json:"body"`
	Hash     string `json:"hash"`
	Readonly bool   `json:"readonly"`
	IsDir    bool   `json:"is_dir"`
}

type FileListResponse struct {
	List []File `json:"list"`
}

func (c *Client) GetFileList(ctx context.Context, projectID string, siteID string) (FileListResponse, error) {
	var ret FileListResponse
	path := fmt.Sprintf("/api/u/project/%s/site/%s/file_list", url.PathEscape(projectID), url.PathEscape(siteID))
	err := c.do(ctx, http.MethodGet, path, nil, nil, &ret)
	if err != nil {
		return FileListResponse{}, err
	}

	return ret, nil
}

type SiteActionFileSpec struct {
	ID   string  `json:"id,omitempty"`
	Path *string `json:"path,omitempty"`
	Body *string `json:"body,omitempty"`
}

type SiteActionChange struct {
	Action string             `json:"action"`
	File   SiteActionFileSpec `json:"file"`
}

type SiteActionResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		Total   int `json:"total"`
		Success int `json:"success"`
		Failed  int `json:"failed"`
	} `json:"result"`
}

func (c *Client) DoSiteAction(ctx context.Context, projectID string, siteID string, clientID string, changes []SiteActionChange) (SiteActionResponse, error) {
	var ret SiteActionResponse
	path := fmt.Sprintf("/api/u/project/%s/site/%s/site_action", url.PathEscape(projectID), url.PathEscape(siteID))
	body := map[string]any{
		"client_id": clientID,
		"changes":   changes,
	}
	err := c.do(ctx, http.MethodPost, path, nil, body, &ret)
	if err != nil {
		return SiteActionResponse{}, err
	}
	if ret.Result.Failed > 0 {
		return ret, fmt.Errorf("site action partially failed: %d/%d", ret.Result.Failed, ret.Result.Total)
	}

	return ret, nil
}

func (c *Client) PublishSite(ctx context.Context, projectID string, siteID string, note string) error {
	path := fmt.Sprintf("/api/u/project/%s/site/%s/publish", url.PathEscape(projectID), url.PathEscape(siteID))
	body := map[string]any{
		"note": strings.TrimSpace(note),
	}

	return c.do(ctx, http.MethodPost, path, nil, body, nil)
}

func StringPtr(v string) *string {
	return &v
}
