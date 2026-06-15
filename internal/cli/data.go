package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
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
  creght content get --site_id=<project_id>/<site_id> --collection=<key-or-id> (--id=<id> | --slug=<slug>)
  creght content create --site_id=<project_id>/<site_id> --collection=<key-or-id> --data=./content.json [--slug=<slug>] [--sort=0]
  creght content update --site_id=<project_id>/<site_id> --collection=<key-or-id> --id=<id> --data=./content.json [--slug=<slug>] [--publish=true]
  creght content delete --site_id=<project_id>/<site_id> --collection=<key-or-id> --id=<id>

Notes:
  Plain --data JSON is treated as the CMS content body, even when it contains a
  business field named "body". JSON is treated as a full content object only
  when it includes fields such as id, slug, status, sort, or tags.
  If your business JSON has a top-level slug, either pass it as --slug and omit
  it from --data, or wrap business fields under body, for example:
  {"slug":"hello-world","body":{"title":"Hello world","tags":["news"]}}`)
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

	return printJSON(content)
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
	content.Sort = *sortValue

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
	dataPath := fs.String("data", "", "content JSON file")
	slug := fs.String("slug", "", "content slug")
	publish := fs.Bool("publish", true, "publish content update")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("--id is required")
	}

	content, err := contentFromDataFile(*dataPath)
	if err != nil {
		return err
	}
	content.ID = strings.TrimSpace(*id)
	if strings.TrimSpace(*slug) != "" {
		content.Slug = strings.TrimSpace(*slug)
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

	if err := client.UpdateContent(ctx, projectID, appID, content, *publish); err != nil {
		return err
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

func contentFromDataFile(path string) (creght.Content, error) {
	raw, err := readOptionalJSON(path)
	if err != nil {
		return creght.Content{}, err
	}
	if len(raw) == 0 {
		return creght.Content{}, fmt.Errorf("--data is required")
	}

	if isFullContentObject(raw) {
		var content creght.Content
		if err := json.Unmarshal(raw, &content); err != nil {
			return creght.Content{}, fmt.Errorf("parse content JSON: %w", err)
		}
		return content, nil
	}

	return creght.Content{Body: raw}, nil
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

func isFullContentObject(raw json.RawMessage) bool {
	for _, key := range []string{"id", "slug", "content_app_id", "json_schema", "status", "sort", "tags"} {
		if rawObjectHas(raw, key) {
			return true
		}
	}

	return false
}

func printJSON(v any) error {
	bs, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(bs))
	return nil
}
