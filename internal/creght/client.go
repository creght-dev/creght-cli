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

func (c *Client) doRaw(ctx context.Context, method string, path string, query url.Values, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		bs, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(bs)
	}

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
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
		return nil, 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	return bs, resp.StatusCode, nil
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

// EmptyNumberSentinel 是服务端表达“把数值字段显式设为 0”的哨兵值。
//
// 服务端的更新接口按“非空字段”计算要写哪些列（fieldutil.GetNotEmptyFields 用
// reflect IsZero 判断），所以裸的 sort:0 会被当成“调用方没传这个字段”而丢弃。
// 提交这个哨兵值时服务端会把它翻译回 0（util.IsSpecialEmpty，快速平方根倒数里
// 那个魔数）。
//
// 只对 update 有效：create 把 sort==0 当作“自动分配到末尾”（取 max+10），
// 不做哨兵翻译，传过去会把这个魔数原样写进库。
const EmptyNumberSentinel = -1082549324

// SortForUpdate 把用户输入的 sort 转成更新接口能识别的值。
func SortForUpdate(sort int) int {
	if sort == 0 {
		return EmptyNumberSentinel
	}
	return sort
}

// Content 的 Sort 用指针：nil 表示请求里不带 sort 字段（create 走服务端默认、
// update 保持原值）。用 int + omitempty 会让显式的 0 静默消失。
type Content struct {
	ID           string          `json:"id,omitempty"`
	Slug         string          `json:"slug,omitempty"`
	ContentAppID string          `json:"content_app_id,omitempty"`
	UserID       int64           `json:"user_id,omitempty"`
	JsonSchema   json.RawMessage `json:"json_schema,omitempty"`
	Tags         []string        `json:"tags,omitempty"`
	Status       string          `json:"status,omitempty"`
	Body         json.RawMessage `json:"body,omitempty"`
	Sort         *int            `json:"sort,omitempty"`
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

type ProjectTable struct {
	ID         string          `json:"id,omitempty"`
	ProjectID  string          `json:"project_id,omitempty"`
	Key        string          `json:"key,omitempty"`
	UserID     int64           `json:"user_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Desc       string          `json:"desc,omitempty"`
	JsonSchema json.RawMessage `json:"json_schema,omitempty"`
	CreatedAt  string          `json:"created_at,omitempty"`
	UpdatedAt  string          `json:"updated_at,omitempty"`
}

// Sort 语义同 Content.Sort：nil 表示不提交该字段。
type ProjectTableRecord struct {
	ID        string          `json:"id,omitempty"`
	TableID   string          `json:"table_id,omitempty"`
	UserID    int64           `json:"user_id,omitempty"`
	Body      json.RawMessage `json:"body,omitempty"`
	Sort      *int            `json:"sort,omitempty"`
	CreatedAt string          `json:"created_at,omitempty"`
	UpdatedAt string          `json:"updated_at,omitempty"`
}

type RunFuncResponse struct {
	StatusCode int
	Body       json.RawMessage
}

type ListResponse[T any] struct {
	Total   int64 `json:"total"`
	HasMore bool  `json:"has_more,omitempty"`
	List    []T   `json:"list"`
}

type IDResponse struct {
	ID string `json:"id"`
}

// AssetPreUploadRequest omits source_url on purpose: the server stores it as the
// remote origin an asset was fetched from, so a local file path has no meaning
// there.
type AssetPreUploadRequest struct {
	FileName     string `json:"file_name"`
	Hash         string `json:"hash"`
	Mimetype     string `json:"mimetype"`
	Size         int    `json:"size"`
	From         string `json:"from,omitempty"`
	CacheControl string `json:"cache_control,omitempty"`
}

type AssetPreUploadResponse struct {
	HashExist    bool   `json:"hash_exist"`
	PresignedURL string `json:"presigned_url"`
	FilePath     string `json:"file_path"`
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

// UpdateResult 是内容更新接口的响应：OK=false 表示服务端没有更新任何字段，Message 说明原因。
type UpdateResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func (c *Client) UpdateContent(ctx context.Context, projectID string, appID string, content Content, publish bool) (UpdateResult, error) {
	path := fmt.Sprintf("/api/u/project/%s/cms/%s/content", url.PathEscape(projectID), url.PathEscape(appID))
	body, err := contentRequestBody(content, publish)
	if err != nil {
		return UpdateResult{}, err
	}

	var raw json.RawMessage
	if err := c.do(ctx, http.MethodPut, path, nil, body, &raw); err != nil {
		return UpdateResult{}, err
	}

	// 兼容旧服务端：响应是字符串 "ok"
	var legacy string
	if json.Unmarshal(raw, &legacy) == nil {
		return UpdateResult{OK: true}, nil
	}

	var ret UpdateResult
	if err := json.Unmarshal(raw, &ret); err != nil {
		return UpdateResult{}, fmt.Errorf("parse update content response: %w", err)
	}
	return ret, nil
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

// Table and Func routes live under the primary /api/p usercenter group; unlike
// cms/form/site they are not mirrored into the /api/u talizen-compat group, so
// these paths intentionally use /api/p while the rest of the client uses /api/u.
func (c *Client) GetProjectTableList(ctx context.Context, projectID string, query url.Values) (ListResponse[ProjectTable], error) {
	var ret ListResponse[ProjectTable]
	path := fmt.Sprintf("/api/p/project/%s/table_list", url.PathEscape(projectID))
	err := c.do(ctx, http.MethodGet, path, query, nil, &ret)
	if err != nil {
		return ListResponse[ProjectTable]{}, err
	}

	return ret, nil
}

func (c *Client) GetProjectTable(ctx context.Context, projectID string, tableID string) (ProjectTable, error) {
	var ret ProjectTable
	path := fmt.Sprintf("/api/p/project/%s/table/%s", url.PathEscape(projectID), url.PathEscape(tableID))
	err := c.do(ctx, http.MethodGet, path, nil, nil, &ret)
	if err != nil {
		return ProjectTable{}, err
	}

	return ret, nil
}

func (c *Client) CreateProjectTable(ctx context.Context, projectID string, table ProjectTable) (string, error) {
	var ret IDResponse
	path := fmt.Sprintf("/api/p/project/%s/table", url.PathEscape(projectID))
	err := c.do(ctx, http.MethodPost, path, nil, table, &ret)
	if err != nil {
		return "", err
	}

	return ret.ID, nil
}

func (c *Client) UpdateProjectTable(ctx context.Context, projectID string, tableID string, table ProjectTable) error {
	path := fmt.Sprintf("/api/p/project/%s/table/%s", url.PathEscape(projectID), url.PathEscape(tableID))
	return c.do(ctx, http.MethodPut, path, nil, table, nil)
}

func (c *Client) DeleteProjectTable(ctx context.Context, projectID string, tableID string) error {
	path := fmt.Sprintf("/api/p/project/%s/table/%s", url.PathEscape(projectID), url.PathEscape(tableID))
	return c.do(ctx, http.MethodDelete, path, nil, nil, nil)
}

func (c *Client) GetProjectTableRecordList(ctx context.Context, projectID string, tableID string, query url.Values, body any) (ListResponse[ProjectTableRecord], error) {
	var ret ListResponse[ProjectTableRecord]
	path := fmt.Sprintf("/api/p/project/%s/table/%s/record_list", url.PathEscape(projectID), url.PathEscape(tableID))
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	err := c.do(ctx, method, path, query, body, &ret)
	if err != nil {
		return ListResponse[ProjectTableRecord]{}, err
	}

	return ret, nil
}

func (c *Client) GetProjectTableRecord(ctx context.Context, projectID string, tableID string, recordID string) (ProjectTableRecord, error) {
	var ret ProjectTableRecord
	path := fmt.Sprintf("/api/p/project/%s/table/%s/record", url.PathEscape(projectID), url.PathEscape(tableID))
	err := c.do(ctx, http.MethodGet, path, url.Values{"id": []string{recordID}}, nil, &ret)
	if err != nil {
		return ProjectTableRecord{}, err
	}

	return ret, nil
}

func (c *Client) CreateProjectTableRecord(ctx context.Context, projectID string, tableID string, record ProjectTableRecord) (string, error) {
	var ret IDResponse
	path := fmt.Sprintf("/api/p/project/%s/table/%s/record", url.PathEscape(projectID), url.PathEscape(tableID))
	err := c.do(ctx, http.MethodPost, path, nil, record, &ret)
	if err != nil {
		return "", err
	}

	return ret.ID, nil
}

func (c *Client) UpdateProjectTableRecord(ctx context.Context, projectID string, tableID string, record ProjectTableRecord) error {
	path := fmt.Sprintf("/api/p/project/%s/table/%s/record", url.PathEscape(projectID), url.PathEscape(tableID))
	return c.do(ctx, http.MethodPut, path, nil, record, nil)
}

func (c *Client) DeleteProjectTableRecord(ctx context.Context, projectID string, tableID string, recordID string) error {
	path := fmt.Sprintf("/api/p/project/%s/table/%s/record", url.PathEscape(projectID), url.PathEscape(tableID))
	return c.do(ctx, http.MethodDelete, path, nil, map[string]string{"id": recordID}, nil)
}

// Func code is now stored as ordinary site source files under /backend/func/ and
// synced through the site file_list / site_action endpoints. The only dedicated
// func endpoint that remains is invocation, which lives under /api/p (see the
// note on GetProjectTableList): it is not part of the /api/u talizen-compat
// group, so use /api/p here.
func (c *Client) RunProjectFunc(ctx context.Context, projectID string, body map[string]any) (RunFuncResponse, error) {
	path := fmt.Sprintf("/api/p/project/%s/func/run", url.PathEscape(projectID))
	bs, statusCode, err := c.doRaw(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return RunFuncResponse{}, err
	}
	if len(bs) == 0 {
		bs = []byte("null")
	}
	if !json.Valid(bs) {
		return RunFuncResponse{}, fmt.Errorf("parse response: invalid JSON")
	}

	return RunFuncResponse{StatusCode: statusCode, Body: json.RawMessage(bs)}, nil
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

// SiteVersion is one immutable snapshot of a site's source files — the platform
// equivalent of a git commit. VersionNo is the per-site number shown in the
// editor; ID is what the publish API takes.
//
// Snapshots cover source files only. CMS content and the platform state under
// /platform/** are live, so they are neither captured nor restored.
type SiteVersion struct {
	ID        int64     `json:"id"`
	VersionNo int64     `json:"version_no"`
	Note      string    `json:"note"`
	From      string    `json:"from"`
	CreatedAt time.Time `json:"created_at"`
}

// SitePublishDomain describes one hostname in the publish panel. Follow means
// the domain has no pinned version, so it moves with the site default version.
type SitePublishDomain struct {
	ID               int64  `json:"id"`
	Domain           string `json:"domain"`
	System           bool   `json:"system"`
	PublishVersionID int64  `json:"publish_version_id"`
	PublishVersionNo int64  `json:"publish_version_no"`
	Follow           bool   `json:"follow"`
}

// SiteFileChange is one file that differs between the remote editable workspace
// and the newest version. ChangeAction is create | update | delete.
type SiteFileChange struct {
	Path         string `json:"path"`
	ChangeAction string `json:"change_action"`
}

// SitePublishState is the whole version/publish picture for a site: the recent
// versions, which one is live, and whether the remote workspace has drifted
// from the newest version.
type SitePublishState struct {
	Versions []SiteVersion `json:"versions"`
	// CurrentVersionID is the site default version, i.e. the one served by every
	// domain that is not pinned to a specific version.
	CurrentVersionID int64               `json:"current_version_id"`
	CurrentVersionNo int64               `json:"current_version_no"`
	SystemDomain     string              `json:"system_domain"`
	Domains          []SitePublishDomain `json:"domains"`
	PublishTargets   []string            `json:"publish_targets"`
	HasChanges       bool                `json:"has_changes"`
	WorkspaceChanges []SiteFileChange    `json:"workspace_changes,omitempty"`
}

// FindVersionByNo resolves a per-site version number to its version, searching
// only the versions this state carries.
func (s SitePublishState) FindVersionByNo(versionNo int64) (SiteVersion, bool) {
	for _, version := range s.Versions {
		if version.VersionNo == versionNo {
			return version, true
		}
	}
	return SiteVersion{}, false
}

// FindVersionByID resolves a version id to its version, searching only the
// versions this state carries.
func (s SitePublishState) FindVersionByID(versionID int64) (SiteVersion, bool) {
	for _, version := range s.Versions {
		if version.ID == versionID {
			return version, true
		}
	}
	return SiteVersion{}, false
}

// PublishVersionResult reports what a create/publish call did. Changed is false
// when the requested version was already the live one, which is a no-op.
type PublishVersionResult struct {
	VersionID int64    `json:"version_id"`
	VersionNo int64    `json:"version_no"`
	Created   bool     `json:"created"`
	Published bool     `json:"published"`
	Changed   bool     `json:"changed"`
	Targets   []string `json:"targets"`
}

func (c *Client) GetSitePublishState(ctx context.Context, projectID string, siteID string) (SitePublishState, error) {
	var ret SitePublishState
	path := fmt.Sprintf("/api/u/project/%s/site/%s/publish/state", url.PathEscape(projectID), url.PathEscape(siteID))
	err := c.do(ctx, http.MethodGet, path, nil, nil, &ret)
	if err != nil {
		return SitePublishState{}, err
	}

	return ret, nil
}

// CreateSiteVersion snapshots the remote workspace into a new version without
// touching the live site. The server rejects it when the workspace is identical
// to the newest version, so versions never duplicate.
func (c *Client) CreateSiteVersion(ctx context.Context, projectID string, siteID string, note string) (PublishVersionResult, error) {
	return c.postPublishVersion(ctx, projectID, siteID, map[string]any{
		"version_id":  0,
		"note":        strings.TrimSpace(note),
		"create_only": true,
	})
}

// PublishSiteVersion points the live site at an existing version. Domains pinned
// to another version do not move.
func (c *Client) PublishSiteVersion(ctx context.Context, projectID string, siteID string, versionID int64, note string) (PublishVersionResult, error) {
	if versionID <= 0 {
		return PublishVersionResult{}, fmt.Errorf("version id must be positive")
	}

	return c.postPublishVersion(ctx, projectID, siteID, map[string]any{
		"version_id": versionID,
		"note":       strings.TrimSpace(note),
	})
}

// PublishSite snapshots the remote workspace into a new version and publishes
// it in one step (version_id 0 means "create from the workspace first").
func (c *Client) PublishSite(ctx context.Context, projectID string, siteID string, note string) (PublishVersionResult, error) {
	return c.postPublishVersion(ctx, projectID, siteID, map[string]any{
		"version_id": 0,
		"note":       strings.TrimSpace(note),
	})
}

func (c *Client) postPublishVersion(ctx context.Context, projectID string, siteID string, body map[string]any) (PublishVersionResult, error) {
	var ret PublishVersionResult
	path := fmt.Sprintf("/api/u/project/%s/site/%s/publish/version", url.PathEscape(projectID), url.PathEscape(siteID))
	err := c.do(ctx, http.MethodPost, path, nil, body, &ret)
	if err != nil {
		return PublishVersionResult{}, err
	}

	return ret, nil
}

func StringPtr(v string) *string {
	return &v
}

// Logout revokes the token this client authenticates with, server-side.
//
// This is not merely a local "forget": until it runs, the token stays valid for
// the rest of its TTL, so every copy of it keeps working — another machine's CLI
// config, an entry cached by git's credential store, a line pasted into curl.
func (c *Client) Logout(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/api/p/logout", nil, nil, nil)
}
