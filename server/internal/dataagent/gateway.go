package dataagent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Zero limits select 30s / 4 MiB defaults; the endpoint must be explicit.
type GatewayOptions struct {
	Endpoint         string
	RequestTimeout   time.Duration
	MaxResponseBytes int64
}

func WithGatewayOptions(opts GatewayOptions) ClientOption {
	return func(c *Client) { c.gateway = opts }
}

// The provider must return concurrency-safe snapshots and bound SDK IO separately from the HTTP budget.
func WithStrictCredentialProvider(fn func() (*Credential, error)) ClientOption {
	return func(c *Client) { c.strictCredFn = fn }
}

type GatewayPageOptions struct {
	PageSize  int
	NextToken string
}

type GatewayDatabaseSearchOptions struct {
	CatalogUUID string
	DbType      string
	EnvType     string
}

type GatewayResult struct {
	Data      json.RawMessage
	NextToken string
	RequestID string
}

// Only sanitized messages and real, validated upstream request IDs may cross the gateway boundary.
type GatewayError struct {
	Code      string
	Message   string
	RequestID string
	Retryable bool
}

const (
	gatewayInvalid      = "GATEWAY_INVALID_ARGUMENT"
	gatewayCredential   = "GATEWAY_CREDENTIAL_ERROR"
	gatewayAuth         = "GATEWAY_AUTH_FAILED"
	gatewayDenied       = "GATEWAY_ACCESS_DENIED"
	gatewayLimited      = "GATEWAY_RATE_LIMITED"
	gatewayIncompatible = "GATEWAY_UPSTREAM_INCOMPATIBLE"
	gatewayTooLarge     = "GATEWAY_RESULT_TOO_LARGE"
	gatewayUnavailable  = "GATEWAY_UNAVAILABLE"
)

func gatewayMessage(code string) string {
	switch code {
	case gatewayInvalid:
		return "Invalid gateway parameters or configuration."
	case "GATEWAY_IDENTITY_REJECTED":
		return "Gateway identity rejected."
	case "GATEWAY_IDENTITY_UNMAPPED":
		return "Gateway identity has no role mapping."
	case gatewayCredential:
		return "Gateway credentials unavailable."
	case gatewayAuth:
		return "Gateway authentication failed."
	case gatewayDenied:
		return "Gateway access denied."
	case gatewayLimited:
		return "Gateway request rate limited."
	case gatewayIncompatible:
		return "Gateway response or action is incompatible."
	case gatewayTooLarge:
		return "Gateway response exceeds the configured limit."
	default:
		return "Gateway request unavailable or canceled."
	}
}

// Formatting an error must not reflect arbitrary Message or RequestID values.
func (e *GatewayError) Error() string { return gatewayMessage(e.Code) }

func gatewayError(code, id string, retryable bool) *GatewayError {
	return &GatewayError{Code: code, Message: gatewayMessage(code), RequestID: id, Retryable: retryable}
}

func (c *Client) ListAgenticCatalog(ctx context.Context, page GatewayPageOptions) (*GatewayResult, error) {
	return c.gatewayPage(ctx, "ListAgenticCatalog", nil, page)
}

func (c *Client) SearchAgenticCatalog(ctx context.Context, searchKey string, page GatewayPageOptions) (*GatewayResult, error) {
	return c.gatewayPage(ctx, "SearchAgenticCatalog", map[string]string{"SearchKey": searchKey}, page)
}

func (c *Client) ListAgenticDatabase(ctx context.Context, catalogUUID string, page GatewayPageOptions) (*GatewayResult, error) {
	return c.gatewayPage(ctx, "ListAgenticDatabase", map[string]string{"CatalogUuid": catalogUUID}, page)
}

func (c *Client) GetAgenticCatalog(ctx context.Context, catalogUUID string) (*GatewayResult, error) {
	return c.callGateway(ctx, "GetAgenticCatalog", map[string]string{"CatalogUuid": catalogUUID})
}

func (c *Client) GetAgenticDatabase(ctx context.Context, databaseUUID string) (*GatewayResult, error) {
	return c.callGateway(ctx, "GetAgenticDatabase", map[string]string{"DatabaseUuid": databaseUUID})
}

func (c *Client) SearchAgenticDatabase(ctx context.Context, searchKey string, filters GatewayDatabaseSearchOptions, page GatewayPageOptions) (*GatewayResult, error) {
	params := map[string]string{"SearchKey": searchKey}
	for key, value := range map[string]string{"CatalogUuid": filters.CatalogUUID, "DbType": filters.DbType, "EnvType": filters.EnvType} {
		if value != "" {
			params[key] = value
		}
	}
	return c.gatewayPage(ctx, "SearchAgenticDatabase", params, page)
}

func (c *Client) ListAgenticDataSources(ctx context.Context, dbType string, page GatewayPageOptions) (*GatewayResult, error) {
	params := make(map[string]string)
	if dbType != "" {
		params["DbType"] = dbType
	}
	return c.gatewayPage(ctx, "ListAgenticDataSources", params, page)
}

func (c *Client) GetAgenticDataSource(ctx context.Context, dataSourceUUID string) (*GatewayResult, error) {
	return c.callGateway(ctx, "GetAgenticDataSource", map[string]string{"DataSourceUuid": dataSourceUUID})
}

func (c *Client) ListAgenticTableBaseInfo(ctx context.Context, databaseUUID string, page GatewayPageOptions) (*GatewayResult, error) {
	return c.gatewayPage(ctx, "ListAgenticTableBaseInfo", map[string]string{"DatabaseUuid": databaseUUID}, page)
}

func (c *Client) GetAgenticTableColumn(ctx context.Context, databaseUUID, qualifiedName string) (*GatewayResult, error) {
	return c.callGateway(ctx, "GetAgenticTableColumn", map[string]string{"DatabaseUuid": databaseUUID, "TableQualifiedName": qualifiedName})
}

func (c *Client) GetAgenticTableIndex(ctx context.Context, databaseUUID, qualifiedName string) (*GatewayResult, error) {
	return c.callGateway(ctx, "GetAgenticTableIndex", map[string]string{"DatabaseUuid": databaseUUID, "TableQualifiedName": qualifiedName})
}

func (c *Client) gatewayPage(ctx context.Context, action string, params map[string]string, page GatewayPageOptions) (*GatewayResult, error) {
	if page.PageSize == 0 {
		page.PageSize = 50
	}
	if page.PageSize < 1 || page.PageSize > 100 {
		return nil, gatewayError(gatewayInvalid, "", false)
	}
	if params == nil {
		params = make(map[string]string)
	}
	params["MaxResults"] = strconv.Itoa(page.PageSize)
	if page.NextToken != "" {
		params["NextToken"] = page.NextToken // Opaque: never trim or reinterpret.
	}
	return c.callGateway(ctx, action, params)
}

func (c *Client) gatewayOptions() (GatewayOptions, error) {
	opts := c.gateway
	// Documented Dms endpoints do not guarantee Agentic availability in every region.
	switch c.region {
	case "cn-shenzhen", "cn-beijing", "cn-shanghai", "cn-hongkong",
		"ap-southeast-1", "cn-hangzhou", "us-west-1", "us-east-1":
	default:
		return opts, gatewayError(gatewayInvalid, "", false)
	}
	if opts.Endpoint != "dms."+c.region+".aliyuncs.com" {
		return opts, gatewayError(gatewayInvalid, "", false)
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 30 * time.Second
	}
	if opts.MaxResponseBytes == 0 {
		opts.MaxResponseBytes = 4 << 20
	}
	if opts.RequestTimeout < 0 || opts.MaxResponseBytes < 0 || opts.MaxResponseBytes == math.MaxInt64 {
		return opts, gatewayError(gatewayInvalid, "", false)
	}
	return opts, nil
}

func (c *Client) gatewayCredential() (*Credential, error) {
	var cred *Credential
	if c.strictCredFn != nil {
		var err error
		cred, err = c.strictCredFn()
		if err != nil {
			return nil, gatewayError(gatewayCredential, "", false)
		}
	} else if c.credFn == nil {
		cred = c.cred
	}
	// credential() may silently fall back to stale snapshots or the static identity.
	if cred == nil || cred.IsAPIKey() || !gatewayCredentialPart(cred.AccessKeyID) || !gatewayCredentialPart(cred.AccessKeySecret) ||
		(cred.SecurityToken != "" && !gatewayCredentialPart(cred.SecurityToken)) {
		return nil, gatewayError(gatewayCredential, "", false)
	}
	snapshot := *cred
	return &snapshot, nil
}

func gatewayCredentialPart(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c <= ' ' || c >= 127 {
			return false
		}
	}
	return true
}

func gatewayPaged(action string) bool {
	switch action {
	case "ListAgenticCatalog", "SearchAgenticCatalog", "ListAgenticDatabase", "ListAgenticTableBaseInfo", "SearchAgenticDatabase", "ListAgenticDataSources":
		return true
	}
	return false
}

func GatewayDatabaseTypeValid(value string) bool {
	if value == "" || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func GatewayEnvironmentTypeValid(value string) bool {
	switch value {
	case "product", "pre", "dev", "test", "sit", "uat", "pet", "stag":
		return true
	}
	return false
}

func gatewayParamsValid(action string, params map[string]string) bool {
	var required []string
	switch action {
	case "ListAgenticCatalog", "ListAgenticDataSources":
	case "GetAgenticDataSource":
		required = []string{"DataSourceUuid"}
	case "SearchAgenticCatalog", "SearchAgenticDatabase":
		required = []string{"SearchKey"}
	case "ListAgenticDatabase", "GetAgenticCatalog":
		required = []string{"CatalogUuid"}
	case "ListAgenticTableBaseInfo", "GetAgenticDatabase":
		required = []string{"DatabaseUuid"}
	case "GetAgenticTableColumn", "GetAgenticTableIndex":
		required = []string{"DatabaseUuid", "TableQualifiedName"}
	default:
		return false
	}
	allowed := make(map[string]bool)
	for _, key := range required {
		if strings.TrimSpace(params[key]) == "" {
			return false
		}
		allowed[key] = true
	}
	if action == "ListAgenticDataSources" {
		allowed["DbType"] = true
		if value, exists := params["DbType"]; exists && !GatewayDatabaseTypeValid(value) {
			return false
		}
	}
	if action == "SearchAgenticDatabase" {
		for _, key := range []string{"CatalogUuid", "DbType", "EnvType"} {
			allowed[key] = true
			if value, exists := params[key]; exists {
				if strings.TrimSpace(value) == "" || key == "DbType" && !GatewayDatabaseTypeValid(value) ||
					key == "EnvType" && !GatewayEnvironmentTypeValid(value) {
					return false
				}
			}
		}
	}
	if gatewayPaged(action) {
		n, err := strconv.Atoi(params["MaxResults"])
		if err != nil || n < 1 || n > 100 {
			return false
		}
		allowed["MaxResults"], allowed["NextToken"] = true, true
	}
	for key := range params {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func (c *Client) callGateway(ctx context.Context, action string, params map[string]string) (*GatewayResult, error) {
	if !gatewayParamsValid(action, params) {
		return nil, gatewayError(gatewayInvalid, "", false)
	}
	opts, err := c.gatewayOptions()
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, gatewayError(gatewayUnavailable, "", false)
	}
	cred, err := c.gatewayCredential()
	if err != nil {
		return nil, err
	}
	// One budget for both attempts, body reads and backoff, not one per attempt.
	ctx, cancel := context.WithTimeout(ctx, opts.RequestTimeout)
	defer cancel()
	client := &http.Client{
		Timeout:       opts.RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// Share the transport, not the general client's cookies, redirects, destinations or timeout.
	if c.http != nil {
		client.Transport = c.http.Transport
	}
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			return nil, gatewayError(gatewayUnavailable, "", false)
		}
		req, buildErr := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://"+opts.Endpoint+"/?"+BuildSignedQueryString(action, "2025-04-14", params), nil)
		if buildErr != nil {
			return nil, gatewayError(gatewayInvalid, "", false)
		}
		for key, value := range SignRequest(cred, http.MethodPost, opts.Endpoint, action, params, "") {
			req.Header.Set(key, value)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Accept-Encoding", "gzip") // We bound the decompressed bytes below.
		req.Header.Set("User-Agent", userAgent)
		resp, doErr := client.Do(req)
		var result *GatewayResult
		var failure *GatewayError
		var retry bool
		var retryAfter string
		if doErr != nil {
			retry = gatewayTransient(doErr) && ctx.Err() == nil
			failure = gatewayError(gatewayUnavailable, "", retry)
		} else {
			retryAfter = resp.Header.Get("Retry-After")
			result, failure, retry = readGatewayResponse(resp, action, opts.MaxResponseBytes, cred, params)
			if result != nil {
				recordRequestID(ctx, action, result.RequestID)
			} else if failure != nil {
				recordRequestID(ctx, action, failure.RequestID)
			}
		}
		if ctx.Err() != nil {
			id := ""
			if failure != nil {
				id = failure.RequestID
			}
			return nil, gatewayError(gatewayUnavailable, id, false)
		}
		if failure == nil {
			return result, nil
		}
		if !retry || attempt == 1 {
			return nil, failure
		}
		delay := gatewayRetryDelay(retryAfter, time.Now())
		deadline, _ := ctx.Deadline()
		if delay >= time.Until(deadline) {
			return nil, failure // Never ignore Retry-After to squeeze in a retry.
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, gatewayError(gatewayUnavailable, failure.RequestID, false)
		case <-timer.C:
		}
	}
	return nil, gatewayError(gatewayUnavailable, "", false)
}

func gatewayTransient(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && (ne.Timeout() || ne.Temporary()) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE)
}

func gatewayRetryDelay(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value != "" && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds > math.MaxInt64/int64(time.Second) {
			return time.Duration(math.MaxInt64)
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		if date.Before(now) {
			return 0
		}
		return date.Sub(now)
	}
	return 100 * time.Millisecond
}

func gatewaySafeRequestID(id string, cred *Credential) string {
	if id == "" || len(id) > 128 {
		return ""
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ""
		}
	}
	if cred != nil {
		for _, secret := range []string{cred.AccessKeyID, cred.AccessKeySecret, cred.SecurityToken, cred.APIKey} {
			if secret != "" && strings.Contains(id, secret) {
				return ""
			}
		}
	}
	return id
}

func readGatewayResponse(resp *http.Response, action string, limit int64, cred *Credential, params map[string]string) (*GatewayResult, *GatewayError, bool) {
	defer resp.Body.Close()
	id := gatewaySafeRequestID(resp.Header.Get("x-acs-request-id"), cred)
	if id == "" {
		id = gatewaySafeRequestID(resp.Header.Get("x-request-id"), cred)
	}
	reader := io.Reader(resp.Body)
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, gatewayError(gatewayIncompatible, id, false), false
		}
		defer gz.Close()
		reader = gz
	default:
		return nil, gatewayError(gatewayIncompatible, id, false), false
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if int64(len(body)) > limit {
		return nil, gatewayError(gatewayTooLarge, id, false), false
	}
	if err != nil {
		retry := gatewayTransient(err)
		code := gatewayUnavailable
		if errors.Is(err, gzip.ErrChecksum) || errors.Is(err, gzip.ErrHeader) {
			code = gatewayIncompatible
		}
		return nil, gatewayError(code, id, retry), retry
	}
	root, valid := gatewayDecodeRoot(body)
	if !valid {
		if resp.StatusCode != http.StatusOK && gatewayPlainHTTPError(body) {
			failure := gatewayAPIError(&APIError{StatusCode: resp.StatusCode}, id)
			return nil, failure, failure.Code == gatewayLimited
		}
		// Ambiguous/invalid JSON cannot supply IDs or authorize a throttle retry.
		return nil, gatewayError(gatewayIncompatible, id, false), false
	}
	if raw, exists := root["RequestId"]; exists && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return nil, gatewayError(gatewayIncompatible, id, false), false
		}
		if safe := gatewaySafeRequestID(value, cred); safe != "" {
			id = safe
		}
	}
	// Only root control fields enter the shared error classifier; its raw errors never escape.
	control := make(map[string]interface{})
	for _, key := range []string{"ErrorCode", "Code", "ErrorMessage", "Message", "errorCode", "error_code", "code", "errorMessage", "error_message", "message"} {
		if value, exists := root[key]; exists && value != nil {
			if _, ok := value.(string); !ok {
				return nil, gatewayError(gatewayIncompatible, id, false), false
			}
			control[key] = value
		}
	}
	// Alternative error spellings do not authorize alternative success envelopes.
	success, hasSuccess := root["Success"].(bool)
	for _, key := range []string{"Success", "success"} {
		if value, exists := root[key]; exists {
			if _, ok := value.(bool); !ok {
				return nil, gatewayError(gatewayIncompatible, id, false), false
			}
			control[key] = value
		}
	}
	// A business HTTP status can reject a nominally successful HTTP 200 response.
	if value, exists := root["HttpStatusCode"]; exists {
		if !gatewayScalar(value, "int32") {
			return nil, gatewayError(gatewayIncompatible, id, false), false
		}
		control["HttpStatusCode"] = value
	}
	apiErr := responseAPIError(control, resp.StatusCode, id, true)
	failureCode := firstFailureCode(control, "ErrorCode", "errorCode", "error_code", "Code", "code")
	if apiErr == nil && (resp.StatusCode != http.StatusOK || failureCode != "" ||
		(hasSuccess && !success) || firstFailureCode(control, "ErrorMessage", "errorMessage", "error_message") != "") {
		apiErr = &APIError{Code: failureCode, StatusCode: resp.StatusCode}
	}
	if apiErr != nil {
		failure := gatewayAPIError(apiErr, id)
		return nil, failure, failure.Code == gatewayLimited
	}
	if !hasSuccess || !success {
		return nil, gatewayError(gatewayIncompatible, id, false), false
	}
	next := ""
	if gatewayPaged(action) {
		if value, exists := root["NextToken"]; exists {
			var ok bool
			next, ok = value.(string)
			if !ok {
				return nil, gatewayError(gatewayIncompatible, id, false), false
			}
		}
		if value, exists := root["MaxResults"]; exists && !gatewayScalar(value, "int32") {
			return nil, gatewayError(gatewayIncompatible, id, false), false
		}
	}
	fields, required := gatewayEntitySchema(action)
	var filtered interface{}
	if action == "GetAgenticCatalog" || action == "GetAgenticDatabase" || action == "GetAgenticDataSource" {
		obj, valid := gatewayFilterObject(root["Data"], fields, required)
		if !valid || obj[required] != params[required] {
			return nil, gatewayError(gatewayIncompatible, id, false), false
		}
		filtered = obj
	} else {
		items, ok := root["Data"].([]interface{})
		if !ok {
			return nil, gatewayError(gatewayIncompatible, id, false), false
		}
		list := make([]interface{}, 0, len(items))
		for _, item := range items {
			obj, valid := gatewayFilterObject(item, fields, required)
			if !valid {
				return nil, gatewayError(gatewayIncompatible, id, false), false
			}
			if action == "SearchAgenticDatabase" && params["CatalogUuid"] != "" {
				if catalog, exists := obj["CatalogUuid"].(string); exists && catalog != params["CatalogUuid"] {
					return nil, gatewayError(gatewayIncompatible, id, false), false
				}
			}
			list = append(list, obj)
		}
		filtered = list
	}
	data, marshalErr := json.Marshal(filtered)
	if marshalErr != nil {
		return nil, gatewayError(gatewayIncompatible, id, false), false
	}
	return &GatewayResult{Data: data, NextToken: next, RequestID: id}, nil, false
}

// Preserve status-only handling for plain-text/HTML HTTP errors, not failed
// JSON envelopes. In particular, a malformed 429 JSON body must not be retried.
func gatewayPlainHTTPError(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && (body[0] == '{' || body[0] == '[' || body[0] == '"') {
		return false
	}
	_, err := json.NewDecoder(bytes.NewReader(body)).Token()
	return err != nil
}

const gatewayMaxJSONDepth = 64 // Count open objects/arrays, including the root.

func gatewayDecodeRoot(body []byte) (map[string]interface{}, bool) {
	if !utf8.Valid(body) || !gatewayValidStringEscapes(body) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, valid := gatewayDecodeValue(decoder, 0)
	root, object := value.(map[string]interface{})
	if !valid || !object {
		return nil, false
	}
	// Token, rather than Decode, avoids unbounded decoding of trailing values.
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return root, true
}

func gatewayDecodeValue(decoder *json.Decoder, depth int) (interface{}, bool) {
	token, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	delim, container := token.(json.Delim)
	if !container {
		return token, true // UseNumber retains the exact numeric literal.
	}
	if depth >= gatewayMaxJSONDepth {
		return nil, false
	}
	switch delim {
	case '{':
		object := make(map[string]interface{})
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				return nil, false
			}
			if _, duplicate := object[key]; duplicate {
				return nil, false // Includes decoded-equivalent and unknown keys.
			}
			value, valid := gatewayDecodeValue(decoder, depth+1)
			if !valid {
				return nil, false
			}
			object[key] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return nil, false
		}
		return object, true
	case '[':
		array := make([]interface{}, 0)
		for decoder.More() {
			value, valid := gatewayDecodeValue(decoder, depth+1)
			if !valid {
				return nil, false
			}
			array = append(array, value)
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return nil, false
		}
		return array, true
	default:
		return nil, false
	}
}

// Validate before encoding/json replaces lone surrogate escapes with U+FFFD.
func gatewayValidStringEscapes(body []byte) bool {
	inString := false
	for i := 0; i < len(body); {
		switch {
		case body[i] == '"':
			inString = !inString
			i++
		case inString && body[i] == '\\':
			if i+1 >= len(body) {
				return false
			}
			if body[i+1] != 'u' {
				i += 2 // Includes escaped quotes/backslashes, not Unicode escapes.
				continue
			}
			if i+6 > len(body) {
				return false
			}
			unit, err := strconv.ParseUint(string(body[i+2:i+6]), 16, 16)
			if err != nil {
				return false
			}
			i += 6
			if unit >= 0xD800 && unit <= 0xDBFF {
				if i+6 > len(body) || body[i] != '\\' || body[i+1] != 'u' {
					return false
				}
				low, err := strconv.ParseUint(string(body[i+2:i+6]), 16, 16)
				if err != nil || low < 0xDC00 || low > 0xDFFF {
					return false
				}
				i += 6
			} else if unit >= 0xDC00 && unit <= 0xDFFF {
				return false
			}
		default:
			i++
		}
	}
	return !inString
}

func gatewayAPIError(e *APIError, id string) *GatewayError {
	code := strings.ToLower(strings.TrimSpace(e.Code))
	base, _, _ := strings.Cut(code, ".")
	local := gatewayIncompatible
	switch {
	case e.StatusCode == http.StatusUnauthorized:
		local = gatewayAuth
	case e.StatusCode == http.StatusForbidden:
		local = gatewayDenied
	default:
		switch base {
		case "invalidaccesskeyid", "invalidaccesskeysecret", "invalidsecuritytoken", "expiredsecuritytoken",
			"signaturedoesnotmatch", "invalidsignature", "incompletesignature", "signatureexpired",
			"signature nonce used", "signaturenonceused", "missingsecuritytoken", "missingaccesskeyid", "authfailure":
			local = gatewayAuth
		case "forbidden", "accessdenied", "unauthorizedoperation", "nopermission", "permissiondenied":
			local = gatewayDenied
		case "throttling", "throttlingexception", "requestthrottled", "toomanyrequests":
			local = gatewayLimited
		case "invalidparameter", "invalidparameters", "missingparameter", "invalidnexttoken":
			local = gatewayInvalid
		case "internalerror", "internalfailure", "serviceunavailable", "unknownerror":
			local = gatewayUnavailable
		default:
			if e.StatusCode == http.StatusTooManyRequests {
				local = gatewayLimited
			} else if e.StatusCode >= 500 {
				local = gatewayUnavailable
			}
		}
	}
	return gatewayError(local, id, local == gatewayLimited || local == gatewayUnavailable)
}

// Entity refs use Python SDK 4.2.2 and Go SDK v4.2.0; required IDs are local discovery constraints.
type gatewayFields map[string]gatewayField

type gatewayField struct {
	kind   string
	fields gatewayFields
}

func gatewayStrings(names ...string) gatewayFields {
	fields := make(gatewayFields, len(names))
	for _, name := range names {
		fields[name] = gatewayField{kind: "string"}
	}
	return fields
}

func gatewayEntitySchema(action string) (gatewayFields, string) {
	switch action {
	case "ListAgenticDataSources", "GetAgenticDataSource":
		fields := gatewayStrings("DataSourceUuid", "Alias", "DbType", "EnvType", "InstanceSource", "RegionId", "CatalogUuid", "CatalogName")
		fields["State"] = gatewayField{kind: "int32"}
		return fields, "DataSourceUuid"
	case "ListAgenticCatalog", "SearchAgenticCatalog", "GetAgenticCatalog":
		fields := gatewayStrings("CatalogUuid", "CatalogType", "DataSourceUuid", "DbType", "Description", "Name", "RegionId")
		fields["State"] = gatewayField{kind: "int32"}
		return fields, "CatalogUuid"
	case "ListAgenticDatabase", "GetAgenticDatabase", "SearchAgenticDatabase":
		fields := gatewayStrings("CatalogUuid", "CatalogName", "CatalogType", "DatabaseUuid", "DbType", "Description", "Name", "QualifiedName", "RegionId", "SearchName")
		fields["State"] = gatewayField{kind: "int32"}
		engine := gatewayStrings("CatalogName", "Encoding", "SchemaName")
		engine["StorageCapacity"] = gatewayField{kind: "int64"}
		fields["EngineMeta"] = gatewayField{kind: "object", fields: engine}
		return fields, "DatabaseUuid"
	case "ListAgenticTableBaseInfo":
		fields := gatewayStrings("CatalogType", "DatabaseQualifiedName", "DatabaseUuid", "Description", "Name", "QualifiedName", "TableType")
		engine := gatewayStrings("CreateTime", "Encoding", "Engine", "LastDdlTime", "TableSchemaName")
		for _, key := range []string{"DataBytes", "IndexBytes", "NumRows", "StorageCapacity"} {
			engine[key] = gatewayField{kind: "int64"}
		}
		fields["EngineMeta"] = gatewayField{kind: "object", fields: engine}
		return fields, "QualifiedName"
	case "GetAgenticTableColumn":
		fields := gatewayStrings("ColumnName", "ColumnType", "Description")
		fields["Position"] = gatewayField{kind: "int32"}
		engine := gatewayStrings("Encoding")
		for _, key := range []string{"AutoIncrement", "GenerationColumn", "Nullable"} {
			engine[key] = gatewayField{kind: "bool"}
		}
		engine["DataLength"] = gatewayField{kind: "int64"}
		engine["DataPrecision"] = gatewayField{kind: "int32"}
		engine["DataScale"] = gatewayField{kind: "int32"}
		fields["EngineMeta"] = gatewayField{kind: "object", fields: engine}
		return fields, "ColumnName"
	case "GetAgenticTableIndex":
		fields := gatewayStrings("IndexName", "IndexType", "Description")
		fields["Primary"] = gatewayField{kind: "bool"}
		fields["Unique"] = gatewayField{kind: "bool"}
		fields["ColumnNames"] = gatewayField{kind: "strings"}
		fields["RealColumnNames"] = gatewayField{kind: "strings"}
		return fields, "IndexName"
	}
	return nil, ""
}

func gatewayFilterObject(value interface{}, fields gatewayFields, required string) (map[string]interface{}, bool) {
	obj, ok := value.(map[string]interface{})
	if !ok || obj == nil || fields == nil {
		return nil, false
	}
	if required != "" {
		id, valid := obj[required].(string)
		if !valid || strings.TrimSpace(id) == "" {
			return nil, false
		}
	}
	out := make(map[string]interface{})
	for key, field := range fields {
		v, exists := obj[key]
		if !exists {
			continue
		}
		if v == nil { // Preserve optional NULL, distinct from absent/zero/empty.
			out[key] = nil
			continue
		}
		if field.kind == "object" {
			nested, valid := gatewayFilterObject(v, field.fields, "")
			if !valid {
				return nil, false
			}
			out[key] = nested
		} else {
			if !gatewayScalar(v, field.kind) {
				return nil, false
			}
			out[key] = v
		}
	}
	return out, true
}

func gatewayScalar(value interface{}, kind string) bool {
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "bool":
		_, ok := value.(bool)
		return ok
	case "int32", "int64":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		n, err := number.Int64()
		return err == nil && (kind == "int64" || n >= math.MinInt32 && n <= math.MaxInt32)
	case "strings":
		list, ok := value.([]interface{})
		if !ok {
			return false
		}
		for _, item := range list {
			if _, valid := item.(string); !valid {
				return false
			}
		}
		return true
	}
	return false
}
