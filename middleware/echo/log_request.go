package echo

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/garin-allo/log"
)

// SetLogRequest is used for save log request model to echo locals as context.
func SetLogRequest() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Get parent context from Echo
			parentCtx := c.Request().Context()

			newCtx := log.NewRequest().SaveToContext(parentCtx)

			c.SetRequest(c.Request().WithContext(newCtx))

			return next(c)
		}
	}
}

// Save log request to file, dont forget to initiate SetLogRequest middleware before using this middleware.
// Use echo body dump when initiate this middleware.
// echo.Use(middleware.BodyDump(logMiddleware.SaveLogRequest())).
func SaveLogRequest() middleware.BodyDumpHandler {
	return func(c echo.Context, req []byte, resp []byte) {
		// Get parent context from Echo Locals
		ctx := c.Request().Context()

		extractRequestData(ctx, c, req, resp)
		log.Context(ctx).Save() // Save log request
	}
}

func extractRequestData(ctx context.Context, c echo.Context, req, resp []byte) {
	requestLog := log.Context(ctx) // Get log request from context

	requestLog.Source = "api"
	requestLog.Method = c.Request().Method
	requestLog.URL = c.Request().Host + c.Request().URL.String()
	requestLog.ReqHeader = getHeader(c, "REQ")
	requestLog.RespHeader = getHeader(c, "RESP")
	requestLog.StatusCode = c.Response().Status

	// Set the response body if not set yet
	if requestLog.RespBody == nil {
		if isBlobResponse(c) {
			requestLog.RespBody = extractResponseFileName(c)
		} else if err := json.Unmarshal(resp, &requestLog.RespBody); err != nil {
			requestLog.RespBody = string(resp)
		}
	}

	// Extract Query Args if using GET or DELETE Method
	if requestLog.Method == "GET" || requestLog.Method == "DELETE" {
		queryArgs := make(map[string][]string)
		for k, v := range c.Request().URL.Query() {
			queryArgs[string(k)] = v
		}
		requestLog.ReqBody = queryArgs
	} else {
		if requestLog.ReqBody == nil {
			if isBlobRequest(c) {
				requestLog.ReqBody = extractRequestFileNames(c)
			} else if err := json.Unmarshal(req, &requestLog.ReqBody); err != nil {
				requestLog.ReqBody = string(req)
			}
		}
	}
}

// Get header from request or response
func getHeader(c echo.Context, status string) map[string][]string {
	header := make(map[string][]string)
	if status == "REQ" {
		for k, v := range c.Request().Header {
			header[string(k)] = v
		}
	} else if status == "RESP" {
		for k, v := range c.Response().Header() {
			header[string(k)] = v
		}
	}
	return header
}

func isBlobRequest(c echo.Context) bool {
	contentType := c.Request().Header.Get(echo.HeaderContentType)
	return strings.Contains(strings.ToLower(contentType), "multipart/form-data")
}

func isBlobResponse(c echo.Context) bool {
	header := c.Response().Header()
	contentType := header.Get(echo.HeaderContentType)
	if strings.Contains(strings.ToLower(contentType), "application/octet-stream") {
		return true
	}
	return header.Get(echo.HeaderContentDisposition) != ""
}

func extractRequestFileNames(c echo.Context) map[string][]string {
	files := make(map[string][]string)
	if err := c.Request().ParseMultipartForm(32 << 20); err != nil && err != http.ErrNotMultipart {
		return files
	}
	if c.Request().MultipartForm == nil {
		return files
	}
	for field, fhs := range c.Request().MultipartForm.File {
		for _, fh := range fhs {
			files[field] = append(files[field], fh.Filename)
		}
	}
	return files
}

func extractResponseFileName(c echo.Context) string {
	header := c.Response().Header()
	disposition := header.Get(echo.HeaderContentDisposition)
	_, params, err := mime.ParseMediaType(disposition)
	if err == nil {
		if filename := params["filename"]; filename != "" {
			return filename
		}
	}
	return "blob"
}
