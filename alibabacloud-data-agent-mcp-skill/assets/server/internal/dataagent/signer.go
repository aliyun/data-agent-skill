package dataagent

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// emptyHash is the SHA-256 hash of an empty string.
const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// sha256Hex computes the SHA-256 digest of s and returns the lowercase hex string.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// hmacSHA256 computes HMAC-SHA256(key, data).
func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// percentEncode performs RFC 3986 percent-encoding (equivalent to Python quote(s, safe="")).
// url.QueryEscape encodes spaces as '+' which is application/x-www-form-urlencoded,
// not RFC 3986. Alibaba Cloud ACS3 V3 signing requires strict RFC 3986 (%20 for spaces).
func percentEncode(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// SignRequest generates Alibaba Cloud API Signature Version 3 headers.
//
// It returns a map of HTTP headers that must be set on the outgoing request.
// The caller should also include these headers when building the request URL
// query string (Action, Version, and any action-specific params are already
// embedded in the canonical query string used for signing).
func SignRequest(cred *Credential, method, host, action string, params map[string]string, body string) map[string]string {
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	nonce := uuidNew()

	// Hash the request body.
	hashedPayload := emptyHash
	if body != "" {
		hashedPayload = sha256Hex(body)
	}

	httpMethod := strings.ToUpper(method)
	canonicalURI := "/"

	// Build sorted query string (Action + Version + caller params).
	queryParams := map[string]string{
		"Action":  action,
		"Version": "2025-04-14",
	}
	for k, v := range params {
		queryParams[k] = v
	}

	sortedKeys := make([]string, 0, len(queryParams))
	for k := range queryParams {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	var qsParts []string
	for _, k := range sortedKeys {
		qsParts = append(qsParts, percentEncode(k)+"="+percentEncode(queryParams[k]))
	}
	canonicalQueryString := strings.Join(qsParts, "&")

	// Headers to sign.
	headersToSign := map[string]string{
		"host":                   host,
		"x-acs-action":          action,
		"x-acs-content-sha256":  hashedPayload,
		"x-acs-date":            timestamp,
		"x-acs-signature-nonce": nonce,
		"x-acs-version":         "2025-04-14",
	}
	if cred.SecurityToken != "" {
		headersToSign["x-acs-security-token"] = cred.SecurityToken
	}

	// Sort header keys for canonical headers and signed-headers string.
	headerKeys := make([]string, 0, len(headersToSign))
	for k := range headersToSign {
		headerKeys = append(headerKeys, k)
	}
	sort.Strings(headerKeys)

	var canonicalHeadersBuf strings.Builder
	for _, k := range headerKeys {
		fmt.Fprintf(&canonicalHeadersBuf, "%s:%s\n", k, headersToSign[k])
	}
	canonicalHeaders := canonicalHeadersBuf.String()
	signedHeaders := strings.Join(headerKeys, ";")

	// Canonical request.
	canonicalRequest := strings.Join([]string{
		httpMethod,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		hashedPayload,
	}, "\n")

	hashedCanonicalRequest := sha256Hex(canonicalRequest)

	// String to sign.
	algorithm := "ACS3-HMAC-SHA256"
	stringToSign := algorithm + "\n" + hashedCanonicalRequest

	// Signature.
	signature := hex.EncodeToString(hmacSHA256([]byte(cred.AccessKeySecret), stringToSign))

	authorization := fmt.Sprintf(
		"%s Credential=%s,SignedHeaders=%s,Signature=%s",
		algorithm, cred.AccessKeyID, signedHeaders, signature,
	)

	// Build the result headers (PascalCase Host for the HTTP header).
	result := map[string]string{
		"Authorization":        authorization,
		"Host":                 host,
		"x-acs-action":         action,
		"x-acs-content-sha256": hashedPayload,
		"x-acs-date":           timestamp,
		"x-acs-signature-nonce": nonce,
		"x-acs-version":        "2025-04-14",
	}
	if cred.SecurityToken != "" {
		result["x-acs-security-token"] = cred.SecurityToken
	}

	return result
}

// BuildSignedQueryString returns the full query string (Action + Version + params)
// sorted and percent-encoded, ready to append to a URL.
func BuildSignedQueryString(action, version string, params map[string]string) string {
	all := map[string]string{
		"Action":  action,
		"Version": version,
	}
	for k, v := range params {
		all[k] = v
	}

	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		parts = append(parts, percentEncode(k)+"="+percentEncode(all[k]))
	}
	return strings.Join(parts, "&")
}

// uuidNew generates a version-4 UUID string using crypto/rand.
func uuidNew() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	// Set version (4) and variant (RFC 4122).
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
