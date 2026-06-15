package cli

import (
	"bysir/creght-cli/internal/creght"
	"bytes"
	"context"
	"flag"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/br41n10/qetag"
)

func runUpload(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printUploadUsage()
		return nil
	}

	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	filePath := fs.String("file", "", "local file path")
	name := fs.String("name", "", "uploaded file name")
	mimeType := fs.String("mimetype", "", "file MIME type")
	cacheControl := fs.String("cache-control", "", "Cache-Control metadata for uploaded object")
	jsonOut := fs.Bool("json", false, "print upload metadata as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("upload does not accept positional arguments; use --file=<path>")
	}
	if strings.TrimSpace(*filePath) == "" {
		return fmt.Errorf("--file is required")
	}

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}

	body, err := os.ReadFile(*filePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", *filePath, err)
	}
	if len(body) == 0 {
		return fmt.Errorf("file is empty: %s", *filePath)
	}

	uploadName := strings.TrimSpace(*name)
	if uploadName == "" {
		uploadName = filepath.Base(*filePath)
	}
	resolvedMIME := strings.TrimSpace(*mimeType)
	if resolvedMIME == "" {
		resolvedMIME = detectMIMEType(*filePath, body)
	}

	hash, err := qetagHash(body)
	if err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	pre, err := client.PreUploadSiteAsset(ctx, projectID, realSiteID, creght.AssetPreUploadRequest{
		FileName:     uploadName,
		Hash:         hash,
		Mimetype:     resolvedMIME,
		Size:         len(body),
		From:         "user",
		SourceURL:    *filePath,
		CacheControl: strings.TrimSpace(*cacheControl),
	})
	if err != nil {
		return err
	}

	if !pre.HashExist {
		if strings.TrimSpace(pre.PresignedURL) == "" {
			return fmt.Errorf("upload presigned URL is empty")
		}
		if err := putPresignedObject(ctx, pre.PresignedURL, resolvedMIME, strings.TrimSpace(*cacheControl), body); err != nil {
			return err
		}
		if err := client.AckS3FileUpload(ctx, pre.ID); err != nil {
			return err
		}
	}

	if *jsonOut {
		return printJSON(map[string]any{
			"file_url": pre.FileURL,
		})
	}
	if strings.TrimSpace(pre.FileURL) != "" {
		fmt.Println(pre.FileURL)
		return nil
	}
	return nil
}

func printUploadUsage() {
	fmt.Println(`creght upload

Usage:
  creght upload --site_id=<project_id>/<site_id> --file=./image.png
  creght upload --site_id=<project_id>/<site_id> --file=./image.png --name=hero.png --json

Options:
  --file          Local file path to upload.
  --name          Optional uploaded file name. Defaults to the local base name.
  --mimetype      Optional MIME type. Defaults to file extension or content detection.
  --cache-control Optional Cache-Control metadata for the uploaded object.
  --json          Print upload metadata as JSON instead of only the public URL.

The command uploads to the site's asset flow and prints the public file URL by
default. The JSON output also includes site_path, a stable /_assets/... path that
can be used from Creght site code.`)
}

func detectMIMEType(path string, body []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != "" {
		if typ := mime.TypeByExtension(ext); typ != "" {
			return typ
		}
	}
	return http.DetectContentType(body)
}

func qetagHash(body []byte) (string, error) {
	qe := qetag.New()
	_, err := qe.Write(body)
	if err != nil {
		return "", fmt.Errorf("qetag hash: %w", err)
	}
	return qe.Etag(), nil
}

func putPresignedObject(ctx context.Context, rawURL string, mimeType string, cacheControl string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", mimeType)
	if strings.TrimSpace(cacheControl) != "" {
		req.Header.Set("Cache-Control", strings.TrimSpace(cacheControl))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("upload file: status %d", resp.StatusCode)
	}

	return nil
}
