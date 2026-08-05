package cli

import (
	"bysir/creght-cli/internal/creght"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func runCMS(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printCMSUsage()
		return nil
	}

	switch args[0] {
	case "collections":
		return runCMSCollections(ctx, args[1:])
	case "collection":
		return runCMSCollection(ctx, args[1:])
	case "help", "-h", "--help":
		printCMSUsage()
		return nil
	default:
		return fmt.Errorf("unknown cms command: %s", args[0])
	}
}

func printCMSUsage() {
	fmt.Println(`creght cms

Usage:
  creght cms collections --site_id=<project_id>/<site_id>
  creght cms collection get --site_id=<project_id>/<site_id> (--id=<id> | --key=<key>)
  creght cms collection create --site_id=<project_id>/<site_id> --key=<key> --name=<name> --schema=./schema.json
  creght cms collection update --site_id=<project_id>/<site_id> (--id=<id> | --key=<key>) [--new-key=<key>] [--name=<name>] [--desc=<desc>] [--schema=./schema.json]
  creght cms collection delete --site_id=<project_id>/<site_id> (--id=<id> | --key=<key>)

Notes:
  --schema may be either a raw JSON Schema object or a full collection object
  with key, name, desc, and json_schema fields.`)
}

func runCMSCollection(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printCMSUsage()
		return nil
	}

	switch args[0] {
	case "get":
		return runCMSCollectionGet(ctx, args[1:])
	case "create":
		return runCMSCollectionCreate(ctx, args[1:])
	case "update":
		return runCMSCollectionUpdate(ctx, args[1:])
	case "delete":
		return runCMSCollectionDelete(ctx, args[1:])
	default:
		return fmt.Errorf("unknown cms collection command: %s", args[0])
	}
}

func runCMSCollections(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cms collections", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	limit := fs.Int("limit", 100, "result limit")
	offset := fs.Int("offset", 0, "result offset")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	res, err := client.GetCMSCollectionList(ctx, projectID, paginationQuery(*limit, *offset))
	if err != nil {
		return err
	}

	return printJSON(res)
}

func runCMSCollectionGet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cms collection get", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "collection id")
	key := fs.String("key", "", "collection key")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	var collection creght.ContentApp
	if strings.TrimSpace(*id) != "" {
		collection, err = client.GetCMSCollection(ctx, projectID, *id)
	} else if strings.TrimSpace(*key) != "" {
		collection, err = client.GetCMSCollectionByKey(ctx, projectID, *key)
	} else {
		return fmt.Errorf("one of --id or --key is required")
	}
	if err != nil {
		return err
	}

	return printJSON(collection)
}

func runCMSCollectionCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cms collection create", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	key := fs.String("key", "", "collection key")
	name := fs.String("name", "", "collection name")
	desc := fs.String("desc", "", "collection description")
	schemaPath := fs.String("schema", "", "JSON schema or collection JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	collection, err := collectionFromInputs(*schemaPath, *key, "", *name, *desc)
	if err != nil {
		return err
	}
	if collection.Key == "" || collection.Name == "" {
		return fmt.Errorf("--key and --name are required unless provided by --schema")
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	id, err := client.CreateCMSCollection(ctx, projectID, collection)
	if err != nil {
		return err
	}

	fmt.Println(id)
	return nil
}

func runCMSCollectionUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cms collection update", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "collection id")
	key := fs.String("key", "", "existing collection key")
	newKey := fs.String("new-key", "", "new collection key")
	name := fs.String("name", "", "collection name")
	desc := fs.String("desc", "", "collection description")
	schemaPath := fs.String("schema", "", "JSON schema or collection JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	appID, err := resolveCMSCollectionID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}

	collection, err := collectionFromInputs(*schemaPath, "", *newKey, *name, *desc)
	if err != nil {
		return err
	}
	if err := client.UpdateCMSCollection(ctx, projectID, appID, collection); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runCMSCollectionDelete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cms collection delete", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "collection id")
	key := fs.String("key", "", "collection key")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	appID, err := resolveCMSCollectionID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	if err := client.DeleteCMSCollection(ctx, projectID, appID); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runContent(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printContentUsage()
		return nil
	}

	switch args[0] {
	case "list":
		return runContentList(ctx, args[1:])
	case "get":
		return runContentGet(ctx, args[1:])
	case "create":
		return runContentCreate(ctx, args[1:])
	case "update":
		return runContentUpdate(ctx, args[1:])
	case "delete":
		return runContentDelete(ctx, args[1:])
	case "help", "-h", "--help":
		printContentUsage()
		return nil
	default:
		return fmt.Errorf("unknown content command: %s", args[0])
	}
}

func printContentUsage() {
	fmt.Println(`creght content

Usage:
  creght content list --site_id=<project_id>/<site_id> --collection=<key-or-id> [--limit=20] [--offset=0] [--filter=./filter.json]
  creght content get --site_id=<project_id>/<site_id> --collection=<key-or-id> (--id=<id> | --slug=<slug>) [--out=./content.json]
  creght content create --site_id=<project_id>/<site_id> --collection=<key-or-id> --data=./content.json [--slug=<slug>] [--sort=<n>]
  creght content update --site_id=<project_id>/<site_id> --collection=<key-or-id> --id=<id> [--data=./content.json] [--slug=<slug>] [--sort=<n>] [--publish=true]
  creght content delete --site_id=<project_id>/<site_id> --collection=<key-or-id> --id=<id>

Notes:
  --data must be a full content object with the business fields wrapped under
  an object-valued "body" key (this is the only accepted format):
  {"slug":"hello-world","sort":1,"body":{"title":"Hello world","tags":["news"]}}
  slug and sort are optional in the file; --slug/--sort take precedence only
  when actually passed, so a sort in the file survives a flagless run.

  update does partial updates, so --data is optional there: pass --slug/--sort
  alone to reorder or rename without re-submitting the body. create still
  requires --data.

  sort: bigger shows first in the CMS list. Omitting it entirely lets create
  append the entry last and leaves update's current value alone. On create,
  --sort=0 means the same "append last" (the platform reads 0 as auto-assign,
  so a literal 0 cannot be created); use update --sort=0 to actually store 0.`)
}

func runContentList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("content list", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	collection := fs.String("collection", "", "collection key or id")
	limit := fs.Int("limit", 20, "result limit")
	offset := fs.Int("offset", 0, "result offset")
	searchKey := fs.String("search_key", "", "search key")
	orderBy := fs.String("order_by", "", "order by")
	filterPath := fs.String("filter", "", "JSON request body or filter file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	appID, err := resolveCMSCollectionID(ctx, client, projectID, "", *collection)
	if err != nil {
		return err
	}

	query := paginationQuery(*limit, *offset)
	setQuery(query, "search_key", *searchKey)
	setQuery(query, "order_by", *orderBy)

	var body any
	if strings.TrimSpace(*filterPath) != "" {
		bodyMap, err := readJSONObject(*filterPath)
		if err != nil {
			return err
		}
		body = bodyMap
	}

	res, err := client.GetContentList(ctx, projectID, appID, query, body)
	if err != nil {
		return err
	}

	return printJSON(res)
}

func runContentGet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("content get", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	collection := fs.String("collection", "", "collection key or id")
	id := fs.String("id", "", "content id")
	slug := fs.String("slug", "", "content slug")
	outPath := fs.String("out", "", "write JSON output to file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" && strings.TrimSpace(*slug) == "" {
		return fmt.Errorf("one of --id or --slug is required")
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	appID, err := resolveCMSCollectionID(ctx, client, projectID, "", *collection)
	if err != nil {
		return err
	}

	query := url.Values{}
	setQuery(query, "id", *id)
	setQuery(query, "slug", *slug)
	content, err := client.GetContent(ctx, projectID, appID, query)
	if err != nil {
		return err
	}

	return outputJSON(content, *outPath)
}

func runContentCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("content create", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	collection := fs.String("collection", "", "collection key or id")
	dataPath := fs.String("data", "", "content JSON file")
	slug := fs.String("slug", "", "content slug")
	sortValue := fs.Int("sort", 0, "content sort")
	if err := fs.Parse(args); err != nil {
		return err
	}

	content, err := contentFromDataFile(*dataPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*slug) != "" {
		content.Slug = strings.TrimSpace(*slug)
	}
	if flagWasSet(fs, "sort") {
		if *sortValue == 0 {
			// 服务端 create 把 sort==0 当成“自动分配到末尾”（取 max+10），无法表示
			// 字面 0，也不做哨兵翻译。显式传 0 就等于让服务端自动分配，同时要覆盖掉
			// --data 里的 sort。要真正落一个 0，用 content update --sort=0。
			content.Sort = nil
		} else {
			content.Sort = sortValue
		}
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	appID, err := resolveCMSCollectionID(ctx, client, projectID, "", *collection)
	if err != nil {
		return err
	}

	id, err := client.CreateContent(ctx, projectID, appID, content)
	if err != nil {
		return err
	}

	fmt.Println(id)
	return nil
}

func runContentUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("content update", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	collection := fs.String("collection", "", "collection key or id")
	id := fs.String("id", "", "content id")
	dataPath := fs.String("data", "", "content JSON file (optional when --slug/--sort alone)")
	slug := fs.String("slug", "", "content slug")
	sortValue := fs.Int("sort", 0, "content sort")
	publish := fs.Bool("publish", true, "publish content update")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("--id is required")
	}

	// --data 可省略：服务端按提交上来的非空字段做局部更新，所以只改 slug/sort 时
	// 不必先 get 一遍再把整个 body 重新提交。
	var content creght.Content
	if strings.TrimSpace(*dataPath) != "" {
		parsed, err := contentFromDataFile(*dataPath)
		if err != nil {
			return err
		}
		content = parsed
	} else if strings.TrimSpace(*slug) == "" && !flagWasSet(fs, "sort") {
		return fmt.Errorf("one of --data, --slug, or --sort is required")
	}
	content.ID = strings.TrimSpace(*id)
	if strings.TrimSpace(*slug) != "" {
		content.Slug = strings.TrimSpace(*slug)
	}
	if flagWasSet(fs, "sort") {
		// 0 必须走哨兵值，否则服务端会把它当成“没传 sort”而忽略。
		v := creght.SortForUpdate(*sortValue)
		content.Sort = &v
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	appID, err := resolveCMSCollectionID(ctx, client, projectID, "", *collection)
	if err != nil {
		return err
	}

	ret, err := client.UpdateContent(ctx, projectID, appID, content, *publish)
	if err != nil {
		return err
	}
	if !ret.OK {
		// 服务端明确表示没有更新任何字段：把信息透出给人/agent，并以非零码退出。
		msg := ret.Message
		if msg == "" {
			msg = "no fields were updated"
		}
		return fmt.Errorf("not updated: %s", msg)
	}

	fmt.Println("ok")
	return nil
}

func runContentDelete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("content delete", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	collection := fs.String("collection", "", "collection key or id")
	id := fs.String("id", "", "content id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("--id is required")
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	appID, err := resolveCMSCollectionID(ctx, client, projectID, "", *collection)
	if err != nil {
		return err
	}

	if err := client.DeleteContent(ctx, projectID, appID, *id); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runForm(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printFormUsage()
		return nil
	}

	switch args[0] {
	case "list":
		return runFormList(ctx, args[1:])
	case "get":
		return runFormGet(ctx, args[1:])
	case "create":
		return runFormCreate(ctx, args[1:])
	case "update":
		return runFormUpdate(ctx, args[1:])
	case "delete":
		return runFormDelete(ctx, args[1:])
	case "logs":
		return runFormLogs(ctx, args[1:])
	case "log":
		return runFormLog(ctx, args[1:])
	case "submit":
		return runFormSubmit(ctx, args[1:])
	case "help", "-h", "--help":
		printFormUsage()
		return nil
	default:
		return fmt.Errorf("unknown form command: %s", args[0])
	}
}

func printFormUsage() {
	fmt.Println(`creght form

Usage:
  creght form list --site_id=<project_id>/<site_id>
  creght form get --site_id=<project_id>/<site_id> (--id=<id> | --key=<key>)
  creght form create --site_id=<project_id>/<site_id> --key=<key> --name=<name> --schema=./schema.json
  creght form update --site_id=<project_id>/<site_id> (--id=<id> | --key=<key>) [--new-key=<key>] [--name=<name>] [--desc=<desc>] [--schema=./schema.json] [--setting=./setting.json]
  creght form delete --site_id=<project_id>/<site_id> (--id=<id> | --key=<key>)
  creght form logs --site_id=<project_id>/<site_id> (--id=<id> | --key=<key>) [--limit=20] [--offset=0]
  creght form log get --site_id=<project_id>/<site_id> (--id=<form_id> | --key=<form_key>) --log_id=<log_id>
  creght form log delete --site_id=<project_id>/<site_id> (--id=<form_id> | --key=<form_key>) --log_id=<log_id>
  creght form submit --site_id=<project_id>/<site_id> --key=<form_key> --data=./payload.json

Notes:
  --schema may be either a raw JSON Schema object or a full form object with
  key, name, desc, json_schema, and setting fields.`)
}

func runFormList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("form list", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	limit := fs.Int("limit", 100, "result limit")
	offset := fs.Int("offset", 0, "result offset")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	res, err := client.GetFormList(ctx, projectID, paginationQuery(*limit, *offset))
	if err != nil {
		return err
	}

	return printJSON(res)
}

func runFormGet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("form get", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "form id")
	key := fs.String("key", "", "form key")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	formID, err := resolveFormID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}

	form, err := client.GetForm(ctx, projectID, formID)
	if err != nil {
		return err
	}

	return printJSON(form)
}

func runFormCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("form create", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	key := fs.String("key", "", "form key")
	name := fs.String("name", "", "form name")
	desc := fs.String("desc", "", "form description")
	schemaPath := fs.String("schema", "", "JSON schema or form JSON file")
	settingPath := fs.String("setting", "", "form setting JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	form, err := formFromInputs(*schemaPath, *settingPath, *key, "", *name, *desc)
	if err != nil {
		return err
	}
	if form.Key == "" || form.Name == "" {
		return fmt.Errorf("--key and --name are required unless provided by --schema")
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	id, err := client.CreateForm(ctx, projectID, form)
	if err != nil {
		return err
	}

	fmt.Println(id)
	return nil
}

func runFormUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("form update", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "form id")
	key := fs.String("key", "", "existing form key")
	newKey := fs.String("new-key", "", "new form key")
	name := fs.String("name", "", "form name")
	desc := fs.String("desc", "", "form description")
	schemaPath := fs.String("schema", "", "JSON schema or form JSON file")
	settingPath := fs.String("setting", "", "form setting JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	formID, err := resolveFormID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	form, err := formFromInputs(*schemaPath, *settingPath, "", *newKey, *name, *desc)
	if err != nil {
		return err
	}
	if err := client.UpdateForm(ctx, projectID, formID, form); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runFormDelete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("form delete", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "form id")
	key := fs.String("key", "", "form key")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	formID, err := resolveFormID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	if err := client.DeleteForm(ctx, projectID, formID); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runFormLogs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("form logs", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "form id")
	key := fs.String("key", "", "form key")
	limit := fs.Int("limit", 20, "result limit")
	offset := fs.Int("offset", 0, "result offset")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	formID, err := resolveFormID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	res, err := client.GetFormLogList(ctx, projectID, formID, paginationQuery(*limit, *offset))
	if err != nil {
		return err
	}

	return printJSON(res)
}

func runFormLog(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printFormUsage()
		return nil
	}
	switch args[0] {
	case "get":
		return runFormLogGet(ctx, args[1:])
	case "delete":
		return runFormLogDelete(ctx, args[1:])
	default:
		return fmt.Errorf("unknown form log command: %s", args[0])
	}
}

func runFormLogGet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("form log get", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "form id")
	key := fs.String("key", "", "form key")
	logID := fs.String("log_id", "", "form log id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*logID) == "" {
		return fmt.Errorf("--log_id is required")
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	formID, err := resolveFormID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	log, err := client.GetFormLog(ctx, projectID, formID, *logID)
	if err != nil {
		return err
	}

	return printJSON(log)
}

func runFormLogDelete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("form log delete", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "form id")
	key := fs.String("key", "", "form key")
	logID := fs.String("log_id", "", "form log id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*logID) == "" {
		return fmt.Errorf("--log_id is required")
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	formID, err := resolveFormID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	if err := client.DeleteFormLog(ctx, projectID, formID, *logID); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runFormSubmit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("form submit", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	key := fs.String("key", "", "form key")
	dataPath := fs.String("data", "", "form payload JSON file")
	fromURL := fs.String("from_url", "", "form source URL")
	uid := fs.String("uid", "", "submitter uid")
	ua := fs.String("ua", "", "submitter user agent")
	ip := fs.String("ip", "", "submitter IP")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" {
		return fmt.Errorf("--key is required")
	}
	payload, err := readJSONObject(*dataPath)
	if err != nil {
		return err
	}
	body := map[string]any{"data": payload}
	if strings.TrimSpace(*fromURL) != "" {
		body["from_url"] = strings.TrimSpace(*fromURL)
	}
	if strings.TrimSpace(*uid) != "" {
		body["uid"] = strings.TrimSpace(*uid)
	}
	if strings.TrimSpace(*ua) != "" {
		body["ua"] = strings.TrimSpace(*ua)
	}
	if strings.TrimSpace(*ip) != "" {
		body["ip"] = strings.TrimSpace(*ip)
	}

	projectID, _, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	if err := client.SubmitForm(ctx, projectID, *key, body); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func collectionFromInputs(schemaPath string, key string, newKey string, name string, desc string) (creght.ContentApp, error) {
	var collection creght.ContentApp
	raw, err := readOptionalJSON(schemaPath)
	if err != nil {
		return collection, err
	}
	if len(raw) > 0 {
		if rawObjectHas(raw, "json_schema") || rawObjectHas(raw, "key") || rawObjectHas(raw, "name") {
			if err := json.Unmarshal(raw, &collection); err != nil {
				return collection, fmt.Errorf("parse collection JSON: %w", err)
			}
		} else {
			collection.JsonSchema = raw
		}
	}
	if strings.TrimSpace(key) != "" {
		collection.Key = strings.TrimSpace(key)
	}
	if strings.TrimSpace(newKey) != "" {
		collection.Key = strings.TrimSpace(newKey)
	}
	if strings.TrimSpace(name) != "" {
		collection.Name = strings.TrimSpace(name)
	}
	if strings.TrimSpace(desc) != "" {
		collection.Desc = strings.TrimSpace(desc)
	}

	return collection, nil
}

func formFromInputs(schemaPath string, settingPath string, key string, newKey string, name string, desc string) (creght.Form, error) {
	var form creght.Form
	raw, err := readOptionalJSON(schemaPath)
	if err != nil {
		return form, err
	}
	if len(raw) > 0 {
		if rawObjectHas(raw, "json_schema") || rawObjectHas(raw, "key") || rawObjectHas(raw, "name") {
			if err := json.Unmarshal(raw, &form); err != nil {
				return form, fmt.Errorf("parse form JSON: %w", err)
			}
		} else {
			form.JsonSchema = raw
		}
	}
	settingRaw, err := readOptionalJSON(settingPath)
	if err != nil {
		return form, err
	}
	if len(settingRaw) > 0 {
		form.Setting = settingRaw
	}
	if strings.TrimSpace(key) != "" {
		form.Key = strings.TrimSpace(key)
	}
	if strings.TrimSpace(newKey) != "" {
		form.Key = strings.TrimSpace(newKey)
	}
	if strings.TrimSpace(name) != "" {
		form.Name = strings.TrimSpace(name)
	}
	if strings.TrimSpace(desc) != "" {
		form.Desc = strings.TrimSpace(desc)
	}

	return form, nil
}

// contentFromDataFile 只接受一种格式：完整 content 对象，业务字段包在对象类型的 "body" 键下。
// 曾经的“裸 body 自动嗅探”会在 body 含 tags/slug/sort 等同名业务字段时误判并静默丢字段，已移除。
func contentFromDataFile(path string) (creght.Content, error) {
	raw, err := readOptionalJSON(path)
	if err != nil {
		return creght.Content{}, err
	}
	if len(raw) == 0 {
		return creght.Content{}, fmt.Errorf("--data is required")
	}

	const formatHint = `--data must be a full content object with an object-valued "body" key, e.g. {"slug":"my-post","sort":1,"body":{"title":"..."}}`

	var content creght.Content
	if err := json.Unmarshal(raw, &content); err != nil {
		return creght.Content{}, fmt.Errorf("parse content JSON: %w; %s", err, formatHint)
	}
	body := bytes.TrimSpace(content.Body)
	if len(body) == 0 || body[0] != '{' {
		return creght.Content{}, fmt.Errorf("%s", formatHint)
	}
	return content, nil
}

func resolveCMSCollectionID(ctx context.Context, client *creght.Client, projectID string, id string, keyOrID string) (string, error) {
	if strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id), nil
	}
	if strings.TrimSpace(keyOrID) == "" {
		return "", fmt.Errorf("--collection, --id, or --key is required")
	}

	collections, err := client.GetCMSCollectionList(ctx, projectID, url.Values{"limit": []string{"-1"}})
	if err != nil {
		return "", err
	}
	keyOrID = strings.TrimSpace(keyOrID)
	for _, collection := range collections.List {
		if collection.ID == keyOrID || collection.Key == keyOrID {
			return collection.ID, nil
		}
	}

	return strings.TrimSpace(keyOrID), nil
}

func resolveFormID(ctx context.Context, client *creght.Client, projectID string, id string, key string) (string, error) {
	if strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id), nil
	}
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("one of --id or --key is required")
	}
	forms, err := client.GetFormList(ctx, projectID, url.Values{"limit": []string{"-1"}})
	if err != nil {
		return "", err
	}
	for _, form := range forms.List {
		if form.Key == strings.TrimSpace(key) {
			return form.ID, nil
		}
	}

	return "", fmt.Errorf("form key %q not found", key)
}

func paginationQuery(limit int, offset int) url.Values {
	query := url.Values{}
	query.Set("limit", strconv.Itoa(limit))
	query.Set("offset", strconv.Itoa(offset))
	return query
}

func setQuery(query url.Values, key string, value string) {
	if strings.TrimSpace(value) != "" {
		query.Set(key, strings.TrimSpace(value))
	}
}

func readOptionalJSON(path string) (json.RawMessage, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	bs, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var raw json.RawMessage
	if err := json.Unmarshal(bs, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return raw, nil
}

func readJSONObject(path string) (map[string]any, error) {
	raw, err := readOptionalJSON(path)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("JSON file path is required")
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("JSON file must contain an object: %w", err)
	}

	return object, nil
}

func rawObjectHas(raw json.RawMessage, key string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return false
	}
	_, ok := object[key]
	return ok
}

func printJSON(v any) error {
	return outputJSON(v, "")
}

func outputJSON(v any, outPath string) error {
	bs, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	outPath = strings.TrimSpace(outPath)
	if outPath != "" && outPath != "-" {
		dir := filepath.Dir(outPath)
		if dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create output dir: %w", err)
			}
		}
		if err := os.WriteFile(outPath, append(bs, '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
		fmt.Printf("Wrote %s\n", outPath)
		return nil
	}
	fmt.Println(string(bs))
	return nil
}
