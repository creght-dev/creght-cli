package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
)

func runTable(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printTableUsage()
		return nil
	}

	switch args[0] {
	case "list":
		return runTableList(ctx, args[1:])
	case "get":
		return runTableGet(ctx, args[1:])
	case "create":
		return runTableCreate(ctx, args[1:])
	case "update":
		return runTableUpdate(ctx, args[1:])
	case "delete":
		return runTableDelete(ctx, args[1:])
	case "record":
		return runTableRecord(ctx, args[1:])
	case "help", "-h", "--help":
		printTableUsage()
		return nil
	default:
		return fmt.Errorf("unknown table command: %s", args[0])
	}
}

func printTableUsage() {
	fmt.Println(`creght table

Usage:
  creght table list --site_id=<project_id>/<site_id>
  creght table get --site_id=<project_id>/<site_id> (--id=<id> | --key=<key>)
  creght table create --site_id=<project_id>/<site_id> --key=<key> --name=<name> --schema=./schema.json
  creght table update --site_id=<project_id>/<site_id> (--id=<id> | --key=<key>) [--new-key=<key>] [--name=<name>] [--desc=<desc>] [--schema=./schema.json]
  creght table delete --site_id=<project_id>/<site_id> (--id=<id> | --key=<key>)
  creght table record list --site_id=<project_id>/<site_id> --table=<key-or-id> [--where=./where.json] [--filter=./filter.json]
  creght table record get --site_id=<project_id>/<site_id> --table=<key-or-id> --id=<record_id>
  creght table record create --site_id=<project_id>/<site_id> --table=<key-or-id> --data=./record.json [--sort=0]
  creght table record update --site_id=<project_id>/<site_id> --table=<key-or-id> --id=<record_id> --data=./patch.json [--sort=0]
  creght table record delete --site_id=<project_id>/<site_id> --table=<key-or-id> --id=<record_id>

Notes:
  Tables are project JSON tables used by Func ctx.db.*. --schema may be either
  a raw JSON Schema object or a full table object with key, name, desc, and
  json_schema fields. Record update merges the JSON body; null removes fields.`)
}

func runTableList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("table list", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	limit := fs.Int("limit", 100, "result limit")
	offset := fs.Int("offset", 0, "result offset")
	searchKey := fs.String("search_key", "", "search key")
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
	query := paginationQuery(*limit, *offset)
	setQuery(query, "search_key", *searchKey)
	res, err := client.GetProjectTableList(ctx, projectID, query)
	if err != nil {
		return err
	}

	return printJSON(res)
}

func runTableGet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("table get", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "table id")
	key := fs.String("key", "", "table key")
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
	tableID, err := resolveTableID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	table, err := client.GetProjectTable(ctx, projectID, tableID)
	if err != nil {
		return err
	}

	return printJSON(table)
}

func runTableCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("table create", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	key := fs.String("key", "", "table key")
	name := fs.String("name", "", "table name")
	desc := fs.String("desc", "", "table description")
	schemaPath := fs.String("schema", "", "JSON schema or table JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	table, err := tableFromInputs(*schemaPath, *key, "", *name, *desc)
	if err != nil {
		return err
	}
	if table.Key == "" || table.Name == "" {
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
	id, err := client.CreateProjectTable(ctx, projectID, table)
	if err != nil {
		return err
	}

	fmt.Println(id)
	return nil
}

func runTableUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("table update", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "table id")
	key := fs.String("key", "", "existing table key")
	newKey := fs.String("new-key", "", "new table key")
	name := fs.String("name", "", "table name")
	desc := fs.String("desc", "", "table description")
	schemaPath := fs.String("schema", "", "JSON schema or table JSON file")
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
	tableID, err := resolveTableID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	table, err := tableFromInputs(*schemaPath, "", *newKey, *name, *desc)
	if err != nil {
		return err
	}
	if err := client.UpdateProjectTable(ctx, projectID, tableID, table); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runTableDelete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("table delete", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "table id")
	key := fs.String("key", "", "table key")
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
	tableID, err := resolveTableID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	if err := client.DeleteProjectTable(ctx, projectID, tableID); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runTableRecord(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printTableUsage()
		return nil
	}
	switch args[0] {
	case "list":
		return runTableRecordList(ctx, args[1:])
	case "get":
		return runTableRecordGet(ctx, args[1:])
	case "create":
		return runTableRecordCreate(ctx, args[1:])
	case "update":
		return runTableRecordUpdate(ctx, args[1:])
	case "delete":
		return runTableRecordDelete(ctx, args[1:])
	default:
		return fmt.Errorf("unknown table record command: %s", args[0])
	}
}

func runTableRecordList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("table record list", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	tableKey := fs.String("table", "", "table key or id")
	limit := fs.Int("limit", 20, "result limit")
	offset := fs.Int("offset", 0, "result offset")
	orderBy := fs.String("order_by", "", "order by")
	wherePath := fs.String("where", "", "simple equality filter JSON file")
	filterPath := fs.String("filter", "", "structured filter JSON file")
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
	tableID, err := resolveTableID(ctx, client, projectID, "", *tableKey)
	if err != nil {
		return err
	}

	query := paginationQuery(*limit, *offset)
	setQuery(query, "order_by", *orderBy)
	body := map[string]any{}
	if strings.TrimSpace(*wherePath) != "" {
		where, err := readJSONObject(*wherePath)
		if err != nil {
			return err
		}
		body["where"] = where
	}
	if strings.TrimSpace(*filterPath) != "" {
		filter, err := readJSONObject(*filterPath)
		if err != nil {
			return err
		}
		body["filter"] = normalizeTableRecordFilter(filter)
	}
	var requestBody any
	if len(body) > 0 {
		body["limit"] = *limit
		body["offset"] = *offset
		if strings.TrimSpace(*orderBy) != "" {
			body["order_by"] = strings.TrimSpace(*orderBy)
		}
		requestBody = body
		query = url.Values{}
	}

	res, err := client.GetProjectTableRecordList(ctx, projectID, tableID, query, requestBody)
	if err != nil {
		return err
	}

	return printJSON(res)
}

func runTableRecordGet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("table record get", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	tableKey := fs.String("table", "", "table key or id")
	id := fs.String("id", "", "record id")
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
	tableID, err := resolveTableID(ctx, client, projectID, "", *tableKey)
	if err != nil {
		return err
	}
	record, err := client.GetProjectTableRecord(ctx, projectID, tableID, *id)
	if err != nil {
		return err
	}

	return printJSON(record)
}

func runTableRecordCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("table record create", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	tableKey := fs.String("table", "", "table key or id")
	dataPath := fs.String("data", "", "record JSON file")
	sortValue := fs.Int("sort", 0, "record sort")
	if err := fs.Parse(args); err != nil {
		return err
	}

	body, err := readJSONRawRequired(*dataPath)
	if err != nil {
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
	tableID, err := resolveTableID(ctx, client, projectID, "", *tableKey)
	if err != nil {
		return err
	}
	id, err := client.CreateProjectTableRecord(ctx, projectID, tableID, creght.ProjectTableRecord{
		Body: body,
		Sort: *sortValue,
	})
	if err != nil {
		return err
	}

	fmt.Println(id)
	return nil
}

func runTableRecordUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("table record update", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	tableKey := fs.String("table", "", "table key or id")
	id := fs.String("id", "", "record id")
	dataPath := fs.String("data", "", "record patch JSON file")
	sortValue := fs.Int("sort", 0, "record sort")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("--id is required")
	}
	body, err := readJSONRawRequired(*dataPath)
	if err != nil {
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
	tableID, err := resolveTableID(ctx, client, projectID, "", *tableKey)
	if err != nil {
		return err
	}
	if err := client.UpdateProjectTableRecord(ctx, projectID, tableID, creght.ProjectTableRecord{
		ID:   strings.TrimSpace(*id),
		Body: body,
		Sort: *sortValue,
	}); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runTableRecordDelete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("table record delete", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	tableKey := fs.String("table", "", "table key or id")
	id := fs.String("id", "", "record id")
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
	tableID, err := resolveTableID(ctx, client, projectID, "", *tableKey)
	if err != nil {
		return err
	}
	if err := client.DeleteProjectTableRecord(ctx, projectID, tableID, *id); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runFunc(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printFuncUsage()
		return nil
	}

	switch args[0] {
	case "list":
		return runFuncList(ctx, args[1:])
	case "get":
		return runFuncGet(ctx, args[1:])
	case "create":
		return runFuncCreate(ctx, args[1:])
	case "update":
		return runFuncUpdate(ctx, args[1:])
	case "delete":
		return runFuncDelete(ctx, args[1:])
	case "run":
		return runFuncRun(ctx, args[1:])
	case "help", "-h", "--help":
		printFuncUsage()
		return nil
	default:
		return fmt.Errorf("unknown func command: %s", args[0])
	}
}

func printFuncUsage() {
	fmt.Println(`creght func

Usage:
  creght func list --site_id=<project_id>/<site_id>
  creght func get --site_id=<project_id>/<site_id> --key=<key>
  creght func create --site_id=<project_id>/<site_id> --key=<key> --file=./func.ts [--name=<name>] [--desc=<desc>]
  creght func update --site_id=<project_id>/<site_id> --key=<key> [--new-key=<key>] [--file=./func.ts] [--name=<name>] [--desc=<desc>]
  creght func delete --site_id=<project_id>/<site_id> --key=<key>
  creght func run --site_id=<project_id>/<site_id> --key=<key-or-key.method> [--method=<method>] [--input=./input.json] [--timeout_ms=3000]

Notes:
  Func keys are extensionless project paths such as booking or profile/settings.
  Write ESM exports with (input, ctx): export function create(input, ctx) { ... }.
  Use talizen/func in page code to call invoke("booking.create", input).`)
}

func runFuncList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("func list", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	limit := fs.Int("limit", 100, "result limit")
	offset := fs.Int("offset", 0, "result offset")
	searchKey := fs.String("search_key", "", "search key")
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
	query := paginationQuery(*limit, *offset)
	setQuery(query, "search_key", *searchKey)
	res, err := client.GetProjectFuncList(ctx, projectID, query)
	if err != nil {
		return err
	}

	return printJSON(res)
}

func runFuncGet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("func get", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "func id")
	key := fs.String("key", "", "func key")
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
	funcID, err := resolveFuncID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	fn, err := client.GetProjectFunc(ctx, projectID, funcID)
	if err != nil {
		return err
	}

	return printJSON(fn)
}

func runFuncCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("func create", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	key := fs.String("key", "", "func key")
	name := fs.String("name", "", "func name")
	desc := fs.String("desc", "", "func description")
	filePath := fs.String("file", "", "TypeScript func file")
	body := fs.String("body", "", "inline func body")
	mimetype := fs.String("mimetype", "application/javascript", "func MIME type")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" {
		return fmt.Errorf("--key is required")
	}
	code, err := readFuncBodyRequired(*filePath, *body)
	if err != nil {
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
	id, err := client.CreateProjectFunc(ctx, projectID, creght.ProjectFunc{
		Key:      strings.TrimSpace(*key),
		Name:     strings.TrimSpace(*name),
		Desc:     strings.TrimSpace(*desc),
		Body:     code,
		Mimetype: strings.TrimSpace(*mimetype),
	})
	if err != nil {
		return err
	}

	fmt.Println(id)
	return nil
}

func runFuncUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("func update", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "func id")
	key := fs.String("key", "", "existing func key")
	newKey := fs.String("new-key", "", "new func key")
	name := fs.String("name", "", "func name")
	desc := fs.String("desc", "", "func description")
	filePath := fs.String("file", "", "TypeScript func file")
	body := fs.String("body", "", "inline func body")
	mimetype := fs.String("mimetype", "", "func MIME type")
	if err := fs.Parse(args); err != nil {
		return err
	}

	code, err := readFuncBodyOptional(*filePath, *body)
	if err != nil {
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
	funcID, err := resolveFuncID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	fn := creght.ProjectFunc{
		Name:     strings.TrimSpace(*name),
		Desc:     strings.TrimSpace(*desc),
		Body:     code,
		Mimetype: strings.TrimSpace(*mimetype),
	}
	if strings.TrimSpace(*newKey) != "" {
		fn.Key = strings.TrimSpace(*newKey)
	}
	if err := client.UpdateProjectFunc(ctx, projectID, funcID, fn); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runFuncDelete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("func delete", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	id := fs.String("id", "", "func id")
	key := fs.String("key", "", "func key")
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
	funcID, err := resolveFuncID(ctx, client, projectID, *id, *key)
	if err != nil {
		return err
	}
	if err := client.DeleteProjectFunc(ctx, projectID, funcID); err != nil {
		return err
	}

	fmt.Println("ok")
	return nil
}

func runFuncRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("func run", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	key := fs.String("key", "", "func key or key.method")
	method := fs.String("method", "", "method name")
	inputPath := fs.String("input", "", "input JSON file")
	timeoutMS := fs.Int("timeout_ms", 0, "timeout in milliseconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" {
		return fmt.Errorf("--key is required")
	}

	projectID, site, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	var input any = map[string]any{}
	if strings.TrimSpace(*inputPath) != "" {
		inputMap, err := readJSONObject(*inputPath)
		if err != nil {
			return err
		}
		input = inputMap
	}
	body := map[string]any{
		"key":     strings.TrimSpace(*key),
		"input":   input,
		"site_id": site,
	}
	if strings.TrimSpace(*method) != "" {
		body["method"] = strings.TrimSpace(*method)
	}
	if *timeoutMS > 0 {
		body["timeout_ms"] = *timeoutMS
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	res, err := client.RunProjectFunc(ctx, projectID, body)
	if err != nil {
		return err
	}

	return printJSON(res)
}

func tableFromInputs(schemaPath string, key string, newKey string, name string, desc string) (creght.ProjectTable, error) {
	var table creght.ProjectTable
	raw, err := readOptionalJSON(schemaPath)
	if err != nil {
		return table, err
	}
	if len(raw) > 0 {
		if rawObjectHas(raw, "json_schema") || rawObjectHas(raw, "key") || rawObjectHas(raw, "name") {
			if err := json.Unmarshal(raw, &table); err != nil {
				return table, fmt.Errorf("parse table JSON: %w", err)
			}
		} else {
			table.JsonSchema = raw
		}
	}
	if strings.TrimSpace(key) != "" {
		table.Key = strings.TrimSpace(key)
	}
	if strings.TrimSpace(newKey) != "" {
		table.Key = strings.TrimSpace(newKey)
	}
	if strings.TrimSpace(name) != "" {
		table.Name = strings.TrimSpace(name)
	}
	if strings.TrimSpace(desc) != "" {
		table.Desc = strings.TrimSpace(desc)
	}

	return table, nil
}

func resolveTableID(ctx context.Context, client *creght.Client, projectID string, id string, keyOrID string) (string, error) {
	if strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id), nil
	}
	if strings.TrimSpace(keyOrID) == "" {
		return "", fmt.Errorf("--table, --id, or --key is required")
	}
	tables, err := client.GetProjectTableList(ctx, projectID, url.Values{"limit": []string{"-1"}})
	if err != nil {
		return "", err
	}
	keyOrID = strings.TrimSpace(keyOrID)
	for _, table := range tables.List {
		if table.ID == keyOrID || table.Key == keyOrID {
			return table.ID, nil
		}
	}

	return "", fmt.Errorf("table key or id %q not found", keyOrID)
}

func resolveFuncID(ctx context.Context, client *creght.Client, projectID string, id string, key string) (string, error) {
	if strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id), nil
	}
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("one of --id or --key is required")
	}
	funcs, err := client.GetProjectFuncList(ctx, projectID, url.Values{"limit": []string{"-1"}})
	if err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	for _, fn := range funcs.List {
		if fn.ID == key || funcKeyMatches(fn.Key, key) {
			return fn.ID, nil
		}
	}

	return "", fmt.Errorf("func key %q not found", key)
}

func funcKeyMatches(stored string, input string) bool {
	stored = strings.Trim(strings.TrimSpace(stored), "/")
	input = strings.Trim(strings.TrimSpace(input), "/")
	return stored == input
}

func readJSONRawRequired(path string) (json.RawMessage, error) {
	raw, err := readOptionalJSON(path)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("JSON file path is required")
	}
	return raw, nil
}

func readFuncBodyRequired(filePath string, inline string) (string, error) {
	body, err := readFuncBodyOptional(filePath, inline)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("one of --file or --body is required")
	}
	return body, nil
}

func readFuncBodyOptional(filePath string, inline string) (string, error) {
	if strings.TrimSpace(filePath) != "" {
		bs, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", filePath, err)
		}
		body := string(bs)
		if strings.TrimSpace(body) == "" {
			return "", fmt.Errorf("func file is empty")
		}
		return body, nil
	}
	if strings.TrimSpace(inline) != "" {
		return inline, nil
	}
	return "", nil
}

func normalizeTableRecordFilter(filter map[string]any) map[string]any {
	conditions, ok := filter["conditions"].([]any)
	if !ok {
		return filter
	}
	nextConditions := make([]any, 0, len(conditions))
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if !ok {
			nextConditions = append(nextConditions, item)
			continue
		}
		if _, ok := condition["fieldId"]; !ok {
			if value, ok := condition["field_id"]; ok {
				condition["fieldId"] = value
			}
		}
		if _, ok := condition["value"]; !ok {
			if values, ok := condition["values"]; ok {
				condition["value"] = values
			}
		}
		nextConditions = append(nextConditions, condition)
	}
	filter["conditions"] = nextConditions
	return filter
}
