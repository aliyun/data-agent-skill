package dataagent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

type gatewayRoundTripper func(*http.Request) (*http.Response, error)

func (fn gatewayRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func gatewayTestCredential() *Credential {
	return &Credential{AccessKeyID: "test-access-id", AccessKeySecret: "test-access-secret", SecurityToken: "test-sts-token"}
}

func gatewayTestClient(fn gatewayRoundTripper, opts ...ClientOption) *Client {
	options := []ClientOption{WithGatewayOptions(GatewayOptions{Endpoint: "dms.cn-shanghai.aliyuncs.com", RequestTimeout: 2 * time.Second})}
	options = append(options, opts...)
	c := NewClient(gatewayTestCredential(), "cn-shanghai", options...)
	c.http = &http.Client{Transport: fn}
	return c
}

func gatewayResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func gatewayAssertError(t *testing.T, result *GatewayResult, err error, code string) *GatewayError {
	t.Helper()
	var failure *GatewayError
	if result != nil || !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("result = %v, error = %#v; want %s", result, err, code)
	}
	for _, value := range []string{failure.Error(), failure.Message, failure.RequestID, failure.Code} {
		for _, secret := range []string{"test-access-id", "test-access-secret", "test-sts-token", "private-query", "https://", "Authorization", "SecurityToken="} {
			if strings.Contains(value, secret) {
				t.Fatalf("unsafe gateway error field: %q", value)
			}
		}
	}
	return failure
}

// Reconstruct the canonical request from the wire, including STS, so signing
// tests check the signature rather than just the existence of Authorization.
func gatewayAssertSignature(t *testing.T, req *http.Request, cred *Credential) {
	t.Helper()
	authorization := req.Header.Get("Authorization")
	prefix := "ACS3-HMAC-SHA256 Credential=" + cred.AccessKeyID + ",SignedHeaders="
	if !strings.HasPrefix(authorization, prefix) {
		t.Fatalf("unexpected authorization format")
	}
	parts := strings.Split(strings.TrimPrefix(authorization, prefix), ",Signature=")
	if len(parts) != 2 {
		t.Fatal("missing signature")
	}
	var headers strings.Builder
	for _, key := range strings.Split(parts[0], ";") {
		value := req.Header.Get(key)
		if key == "host" {
			value = req.URL.Host
		}
		fmt.Fprintf(&headers, "%s:%s\n", key, value)
	}
	canonical := strings.Join([]string{req.Method, req.URL.Path, req.URL.RawQuery, headers.String(), parts[0], emptyHash}, "\n")
	want := hexEncode(hmacSHA256([]byte(cred.AccessKeySecret), "ACS3-HMAC-SHA256\n"+sha256Hex(canonical)))
	if parts[1] != want {
		t.Fatal("signature does not cover wire query and headers")
	}
}

type gatewayJSONCase struct{ name, body string }

func gatewayAmbiguousJSONCases() []gatewayJSONCase {
	cases := []gatewayJSONCase{
		{"root", `{"Success":true,"Success":true,"Data":[]}`},
		{"rootEscaped", `{"Success":true,"Succ\u0065ss":true,"Data":[]}`},
		{"data", `{"Success":true,"Data":{},"Data":[]}`},
		{"detail", `{"Success":true,"Data":{"DatabaseUuid":"d","Name":"a","Name":"a"}}`},
		{"arrayElement", `{"Success":true,"Data":[{"DatabaseUuid":"d","Name":"a","N\u0061me":"a"}]}`},
		{"laterElement", `{"Success":true,"Data":[{"DatabaseUuid":"good"},{"DatabaseUuid":"d","DatabaseUuid":"d"}]}`},
		{"engineMeta", `{"Success":true,"Data":[{"DatabaseUuid":"d","EngineMeta":{"StorageCapacity":1,"StorageCapacity":2}}]}`},
		{"unknownRoot", `{"Success":true,"Data":[],"Unknown":{"a":null,"a":null}}`},
		{"unknownArray", `{"Success":true,"Data":[],"Unknown":[[{"a":1,"\u0061":1}]]}`},
		{"unknownEntity", `{"Success":true,"Data":[{"DatabaseUuid":"d","Unknown":{"a":true,"a":true}}]}`},
		{"unknownEngine", `{"Success":true,"Data":[{"DatabaseUuid":"d","EngineMeta":{"Unknown":{"a":1,"a":2}}}]}`},
		{"emptyKey", `{"Success":true,"Data":[],"Unknown":{"":null,"":null}}`},
		{"nonBMPKey", `{"Success":true,"Data":[],"Unknown":{"\ud834\udd1e":1,"𝄞":1}}`},
		{"replacementKey", `{"Success":true,"Data":[],"Unknown":{"\ufffd":1,"�":1}}`},
		{"duplicateID", `{"Success":true,"Data":[],"RequestId":"one","RequestId":"two"}`},
		{"trailingJSON", `{"Success":true,"Data":[]} {}`},
		{"trailingText", `{"Success":true,"Data":[]} ignored`},
		{"truncated", `{"Success":true,"Data":[`},
		{"invalidUTF8", "{\"Success\":true,\"Data\":[],\"Unknown\":\"\xff\"}"},
		{"encodedSurrogateUTF8", "{\"Success\":true,\"Data\":[],\"Unknown\":\"\xed\xa0\x80\"}"},
		{"unknownDeepArray", `{"Success":true,"Data":[],"Unknown":` + strings.Repeat("[", 64) + `0` + strings.Repeat("]", 64) + `}`},
		{"unknownDeepObject", `{"Success":true,"Data":[],"Unknown":` + strings.Repeat(`{"x":`, 64) + `null` + strings.Repeat("}", 64) + `}`},
	}
	for i, token := range []string{`"\ud800"`, `"\udbff"`, `"\udc00"`, `"\udfff"`, `"\ud800\ud800"`, `"\udc00\ud800"`, `"\ud800\u0041"`, `"\ud800x\udc00"`, `"\ud800\\udc00"`, `"\ud800\udc00\udfff"`, `"\ud80"`, `"\uXXXX"`} {
		for j, template := range []string{
			`{"Success":true,"Data":[{"DatabaseUuid":"d","Name":%s}]}`,
			`{"Success":true,"Data":[{"DatabaseUuid":"d","EngineMeta":{"SchemaName":%s}}]}`,
			`{"Success":true,"Data":[],"Unknown":[{"value":%s}]}`,
			`{"Success":true,"Data":[],"Unknown":{%s:0}}`,
			`{"Success":true,"Data":[],%s:0}`,
		} {
			cases = append(cases, gatewayJSONCase{fmt.Sprintf("surrogate-%d/location-%d", i, j), fmt.Sprintf(template, token)})
		}
	}
	return cases
}

func TestGatewayAmbiguousJSONNeverReturnsDataOrRetries(t *testing.T) {
	for _, tc := range gatewayAmbiguousJSONCases() {
		t.Run(tc.name, func(t *testing.T) {
			// A safe-looking body ID before the fault must never be trusted.
			body := `{"RequestId":"untrusted-body-id",` + tc.body[1:]
			if root, valid := gatewayDecodeRoot([]byte(body)); valid || root != nil {
				t.Fatal("ambiguous body accepted or partial root returned")
			}
			for _, status := range []int{200, 401, 403, 404, 429, 503} {
				for _, headerMode := range []string{"none", "safe", "unsafeWithFallback"} {
					calls := 0
					c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
						calls++
						resp := gatewayResponse(status, body)
						resp.Header.Set("Retry-After", "0")
						if headerMode == "safe" {
							resp.Header.Set("x-acs-request-id", "verified-header-id")
						} else if headerMode == "unsafeWithFallback" {
							resp.Header.Set("x-acs-request-id", "test-sts-token")
							resp.Header.Set("x-request-id", "verified-header-id")
						}
						return resp, nil
					})
					ctx, ids := WithRequestIDs(context.Background())
					result, err := c.ListAgenticDatabase(ctx, "c", GatewayPageOptions{})
					failure := gatewayAssertError(t, result, err, gatewayIncompatible)
					wantID, wantIDs := "", 0
					if headerMode != "none" {
						wantID, wantIDs = "verified-header-id", 1
					}
					if failure.RequestID != wantID || failure.Retryable || calls != 1 || len(ids.Snapshot()) != wantIDs {
						t.Fatalf("HTTP %d: calls=%d failure=%+v IDs=%+v", status, calls, failure, ids.Snapshot())
					}
					if wantIDs != 0 && ids.Snapshot()[0].RequestID != wantID {
						t.Fatal("body ID entered collector")
					}
				}
			}
		})
	}
}

func TestGatewayStrictJSONDepthAndSyntax(t *testing.T) {
	for _, containers := range []string{"array", "object", "mixed"} {
		for _, depth := range []int{1, 63, 64, 65, 128} {
			body := "null"
			for i := 1; i < depth; i++ {
				if containers == "object" || containers == "mixed" && i%2 == 0 {
					body = `{"x":` + body + `}`
				} else {
					body = `[` + body + `]`
				}
			}
			body = `{"x":` + body + `}`
			root, valid := gatewayDecodeRoot([]byte(body))
			if valid != (depth <= 64) || !valid && root != nil {
				t.Fatalf("%s depth %d: valid=%v", containers, depth, valid)
			}
		}
	}
	for _, body := range []string{``, `null`, `[]`, `true`, `42`, `"text"`, `{"a":01}`, `{"a":1,}`, `{"a":[1,]}`, `{"a":1 "b":2}`, `{"a":NaN}`, `{"a":+1}`, `{"a":1e}`, `{"a":"\x41"}`, `{"a":"\u+123"}`, "{\"a\":\"raw\nnewline\"}", `{"a":1}null`, `{"a":1}` + strings.Repeat("[", 1000)} {
		if root, valid := gatewayDecodeRoot([]byte(body)); valid || root != nil {
			t.Fatalf("invalid JSON accepted: %q", body)
		}
	}
}

func TestGatewayStrictJSONPreservesUnicodeNumbersAndNull(t *testing.T) {
	for _, tc := range []struct{ token, want string }{
		{`"\ud800\udc00"`, "\U00010000"}, {`"\udbff\udfff"`, "\U0010ffff"},
		{`"\uD834\uDD1e"`, "𝄞"}, {`"𝄞雪"`, "𝄞雪"}, {`"\ufffd"`, "�"}, {`"�"`, "�"},
		{`"\ud7ff\ue000"`, "\ud7ff\ue000"}, {`"\\ud800"`, `\ud800`},
		{`"\\\"\ud834\udd1e"`, "\\\"𝄞"}, {`"\u0000\b\f\n\r\t\/"`, "\x00\b\f\n\r\t/"},
	} {
		body := `{"Success":true,"RequestId":"unicode-id","Data":[{"DatabaseUuid":"  d  ","Name":` + tc.token + `,"QualifiedName":"  db.𝄞  ","Description":null,"State":-2147483648,"EngineMeta":{"StorageCapacity":9223372036854775807,"Encoding":null}}],"Unknown":{` + tc.token + `:null}}`
		root, valid := gatewayDecodeRoot([]byte(body))
		if !valid {
			t.Fatalf("valid string rejected: %s", tc.token)
		}
		unknown := root["Unknown"].(map[string]interface{})
		if value, present := unknown[tc.want]; !present || value != nil {
			t.Fatal("Unicode key or NULL changed")
		}
		result, failure, retry := readGatewayResponse(gatewayResponse(200, body), "ListAgenticDatabase", 4096, gatewayTestCredential(), nil)
		if failure != nil || retry || result.RequestID != "unicode-id" || bytes.Contains(result.Data, []byte("Unknown")) {
			t.Fatalf("valid Unicode response rejected: %v", failure)
		}
		var rows []map[string]interface{}
		decoder := json.NewDecoder(bytes.NewReader(result.Data))
		decoder.UseNumber()
		if err := decoder.Decode(&rows); err != nil {
			t.Fatal(err)
		}
		row := rows[0]
		if row["Name"] != tc.want || row["DatabaseUuid"] != "  d  " || row["QualifiedName"] != "  db.𝄞  " || row["State"] != json.Number("-2147483648") || row["EngineMeta"].(map[string]interface{})["StorageCapacity"] != json.Number("9223372036854775807") {
			t.Fatal("Unicode, identifiers or numeric precision changed")
		}
		if v, present := row["Description"]; !present || v != nil {
			t.Fatal("optional NULL lost")
		}
	}
	// Numbers in unknown fields still decode exactly, even beyond float64 range.
	for _, number := range []string{"9007199254740993", "-9223372036854775808", "0.123456789012345678901234567890", "1e400", "-0", "1.2300e-30"} {
		root, valid := gatewayDecodeRoot([]byte(`{"unknown":[` + number + `]}`))
		if !valid || root["unknown"].([]interface{})[0] != json.Number(number) {
			t.Fatalf("number literal changed: %s", number)
		}
	}
	// Duplicates are scoped per object; distinct casing/normalization is retained.
	if _, valid := gatewayDecodeRoot([]byte(`{"Data":[{"Name":"a"},{"Name":"b"}],"x":{"Name":1,"name":2,"é":3,"e\u0301":4}}`)); !valid {
		t.Fatal("distinct object keys treated as duplicates")
	}
}

func TestGatewayStrictJSONAfterDecompression(t *testing.T) {
	for _, body := range []string{
		`{"Success":true,"Data":[],"Unknown":{"a":null,"a":null}}`,
		`{"Success":true,"Data":[],"Unknown":"\ud800"}`,
		`{"Success":true,"Data":[],"Unknown":` + strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64) + "}",
	} {
		var buffer bytes.Buffer
		gz := gzip.NewWriter(&buffer)
		if _, err := gz.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		resp := gatewayResponse(429, buffer.String())
		resp.Header.Set("Content-Encoding", "gzip")
		resp.Header.Set("x-acs-request-id", "compressed-id")
		result, failure, retry := readGatewayResponse(resp, "ListAgenticDatabase", int64(len(body)), gatewayTestCredential(), nil)
		gatewayAssertError(t, result, failure, gatewayIncompatible)
		if retry || failure.Retryable || failure.RequestID != "compressed-id" {
			t.Fatal("compressed ambiguity retried or lost safe header ID")
		}
	}
}

func FuzzGatewayDecodeRoot(f *testing.F) {
	for _, tc := range gatewayAmbiguousJSONCases() {
		f.Add([]byte(tc.body))
	}
	for _, seed := range []string{`{}`, `{"Data":[]}`, `{"Data":{"Name":"\ud834\udd1e �","Number":0.12345678901234567890}}`, `{"x":[null,true,false,-0,1e400]}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 8192 {
			t.Skip()
		}
		root, valid := gatewayDecodeRoot(body)
		if !valid {
			if root != nil {
				t.Fatal("partial data returned")
			}
			return
		}
		if !json.Valid(body) || !utf8.Valid(body) {
			t.Fatal("accepted invalid JSON/UTF-8")
		}
		var reference map[string]interface{}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if decoder.Decode(&reference) != nil || !reflect.DeepEqual(root, reference) {
			t.Fatal("changed decoded value or numeric precision")
		}
		encoded, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip, valid := gatewayDecodeRoot(encoded)
		if !valid || !reflect.DeepEqual(root, roundTrip) {
			t.Fatal("strict JSON roundtrip changed values")
		}
	})
}

func FuzzGatewayUnicodeEscapes(f *testing.F) {
	for _, units := range [][2]uint16{{0xd800, 0xdc00}, {0xdbff, 0xdfff}, {0xd834, 0xdd1e}, {0xfffd, 0xfffd}, {0xdc00, 0xd800}, {0xd800, 0x41}, {0x41, 0xdc00}} {
		f.Add(units[0], units[1])
	}
	f.Fuzz(func(t *testing.T, first, second uint16) {
		token := fmt.Sprintf(`"\u%04x\u%04x"`, first, second)
		body := `{"Unknown":[{` + token + `:` + token + `}]}`
		pair := first >= 0xd800 && first <= 0xdbff && second >= 0xdc00 && second <= 0xdfff
		wantValid := pair || !utf16.IsSurrogate(rune(first)) && !utf16.IsSurrogate(rune(second))
		root, valid := gatewayDecodeRoot([]byte(body))
		if valid != wantValid || !valid && root != nil {
			t.Fatal("incorrect surrogate acceptance")
		}
		if valid {
			want := string(utf16.Decode([]uint16{first, second}))
			if root["Unknown"].([]interface{})[0].(map[string]interface{})[want] != want {
				t.Fatal("valid Unicode key/value changed")
			}
		}
	})
}

func FuzzGatewayDecodedDuplicateKeys(f *testing.F) {
	f.Add("Name", uint8(0))
	f.Add("𝄞", uint8(61))
	f.Add("�", uint8(62))
	f.Fuzz(func(t *testing.T, key string, layers uint8) {
		if len(key) > 256 || !utf8.ValidString(key) {
			t.Skip()
		}
		encoded, _ := json.Marshal(key)
		// Quoted-to-ASCII yields a different JSON spelling for non-ASCII keys.
		var escaped strings.Builder
		escaped.WriteByte('"')
		for _, r := range key {
			if r <= 0xffff {
				fmt.Fprintf(&escaped, `\u%04x`, r)
			} else {
				r -= 0x10000
				fmt.Fprintf(&escaped, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			}
		}
		escaped.WriteByte('"')
		body := "{" + string(encoded) + ":null," + escaped.String() + ":null}"
		for i := 0; i < int(layers)%65; i++ {
			body = `{"unknown":[` + body + `]}`
		}
		if root, valid := gatewayDecodeRoot([]byte(body)); valid || root != nil {
			t.Fatal("decoded-equivalent duplicate accepted")
		}
	})
}

func TestGatewayDataSourceSigning(t *testing.T) {
	id := "  region_source+/=&雪  "
	page := GatewayPageOptions{PageSize: 100, NextToken: "  cursor+/=&雪  "}
	for _, tc := range []struct {
		name, action string
		params       map[string]string
		call         func(*Client) (*GatewayResult, error)
	}{
		{"defaults", "ListAgenticDataSources", map[string]string{"MaxResults": "50"}, func(c *Client) (*GatewayResult, error) {
			return c.ListAgenticDataSources(context.Background(), "", GatewayPageOptions{})
		}},
		{"filtered", "ListAgenticDataSources", map[string]string{"MaxResults": "100", "NextToken": page.NextToken, "DbType": "POLARDB_POSTGRESQL"}, func(c *Client) (*GatewayResult, error) {
			return c.ListAgenticDataSources(context.Background(), "POLARDB_POSTGRESQL", page)
		}},
		{"detail", "GetAgenticDataSource", map[string]string{"DataSourceUuid": id}, func(c *Client) (*GatewayResult, error) { return c.GetAgenticDataSource(context.Background(), id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := gatewayTestClient(func(req *http.Request) (*http.Response, error) {
				calls++
				want := url.Values{"Action": {tc.action}, "Version": {"2025-04-14"}}
				for k, v := range tc.params {
					want.Set(k, v)
				}
				if req.Method != http.MethodPost || req.URL.Scheme != "https" || req.URL.Host != "dms.cn-shanghai.aliyuncs.com" || req.URL.Path != "/" || !reflect.DeepEqual(req.URL.Query(), want) || req.Body != nil && req.Body != http.NoBody {
					t.Fatal("unexpected data source request")
				}
				if req.Header.Get("x-acs-action") != tc.action || req.Header.Get("x-acs-security-token") != "test-sts-token" {
					t.Fatal("action/STS header missing")
				}
				gatewayAssertSignature(t, req, gatewayTestCredential())
				var data any = []any{}
				if tc.action == "GetAgenticDataSource" {
					data = map[string]string{"DataSourceUuid": id}
				}
				body, _ := json.Marshal(map[string]any{"Success": true, "Data": data})
				return gatewayResponse(200, string(body)), nil
			}, WithWorkspaceID("not-forwarded"), WithDMSUnit("not-forwarded"))
			result, err := tc.call(c)
			if err != nil || result == nil || calls != 1 {
				t.Fatalf("unexpected result/error/calls: %v %v %d", result, err, calls)
			}
		})
	}
}

func TestGatewayDataSourceParameterBoundary(t *testing.T) {
	c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid request reached network")
		return nil, nil
	},
		WithStrictCredentialProvider(func() (*Credential, error) { t.Fatal("invalid request resolved credentials"); return nil, nil }))
	for _, value := range []string{" ", "\u3000", "mysql", "1MYSQL", "MYSQL-8", "MYSQL/Host", "MYSQL ", "ＭＹＳＱＬ"} {
		result, err := c.ListAgenticDataSources(context.Background(), value, GatewayPageOptions{})
		gatewayAssertError(t, result, err, gatewayInvalid)
	}
	for _, value := range []string{"", " ", "\u3000"} {
		result, err := c.GetAgenticDataSource(context.Background(), value)
		gatewayAssertError(t, result, err, gatewayInvalid)
	}
	for _, size := range []int{-1, 101} {
		result, err := c.ListAgenticDataSources(context.Background(), "", GatewayPageOptions{PageSize: size})
		gatewayAssertError(t, result, err, gatewayInvalid)
	}
	for _, action := range []string{"ListAgenticDataSources", "GetAgenticDataSource"} {
		for _, key := range []string{"SearchKey", "Host", "Port", "State", "EnvType", "InstanceSource", "Schema", "RoleArn", "Action", "Actor", "CatalogUuid", "TotalCount"} {
			params := map[string]string{key: "untrusted"}
			if action == "ListAgenticDataSources" {
				params["MaxResults"] = "50"
			} else {
				params["DataSourceUuid"] = "source"
			}
			result, err := c.callGateway(context.Background(), action, params)
			gatewayAssertError(t, result, err, gatewayInvalid)
		}
	}
	for _, key := range []string{"MaxResults", "NextToken", "DbType"} {
		result, err := c.callGateway(context.Background(), "GetAgenticDataSource", map[string]string{"DataSourceUuid": "source", key: "1"})
		gatewayAssertError(t, result, err, gatewayInvalid)
	}
	if gatewayParamsValid("ListAgenticDataSources", map[string]string{"MaxResults": "50", "DbType": ""}) {
		t.Fatal("explicit empty DbType accepted")
	}
}

func TestGatewayDataSourceWhitelist(t *testing.T) {
	fields := map[string]any{"DataSourceUuid": "source", "Alias": "summary", "DbType": "MYSQL", "EnvType": "product", "InstanceSource": "RDS", "RegionId": "cn-shanghai", "CatalogUuid": nil, "CatalogName": nil, "State": -2}
	blocked := []string{"AuthAccount", "AuthPassword", "AuthType", "KmsSecret", "RoleArn", "ResourceAccountId", "Host", "Port", "Schema", "PolicyId", "VpcId", "VSwitchId", "ZoneId", "SnowflakeRole", "SnowflakeWarehouse", "UnknownNested"}
	for _, key := range blocked {
		fields[key] = map[string]any{"secret": "private-query"}
	}
	for _, action := range []string{"ListAgenticDataSources", "GetAgenticDataSource"} {
		t.Run(action, func(t *testing.T) {
			var data any = fields
			if action == "ListAgenticDataSources" {
				data = []any{fields}
			}
			body, _ := json.Marshal(map[string]any{"Success": true, "Data": data, "NextToken": " cursor+/= ", "TotalCount": "not-returned-or-required"})
			result, failure, retry := readGatewayResponse(gatewayResponse(200, string(body)), action, 1<<20, gatewayTestCredential(), map[string]string{"DataSourceUuid": "source"})
			if failure != nil || retry || result == nil {
				t.Fatalf("unexpected whitelist failure: %v", failure)
			}
			if strings.Contains(string(result.Data), "private-query") || strings.Contains(string(result.Data), "TotalCount") {
				t.Fatal("excluded metadata returned")
			}
			var obj map[string]any
			if action == "ListAgenticDataSources" {
				var rows []map[string]any
				json.Unmarshal(result.Data, &rows)
				if len(rows) != 1 {
					t.Fatal("bad array")
				}
				obj = rows[0]
				if result.NextToken != " cursor+/= " {
					t.Fatal("cursor changed")
				}
			} else {
				json.Unmarshal(result.Data, &obj)
				if result.NextToken != "" {
					t.Fatal("detail became paginated")
				}
			}
			if len(obj) != 9 || obj["State"] != float64(-2) {
				t.Fatal("wrong whitelist or signed state")
			}
			for _, key := range []string{"CatalogUuid", "CatalogName"} {
				if v, ok := obj[key]; !ok || v != nil {
					t.Fatal("optional null lost")
				}
			}
		})
	}
	for _, state := range []any{nil, int64(math.MinInt32), int64(math.MaxInt32)} {
		body, _ := json.Marshal(map[string]any{"Success": true, "Data": map[string]any{"DataSourceUuid": "source", "State": state}})
		result, failure, _ := readGatewayResponse(gatewayResponse(200, string(body)), "GetAgenticDataSource", 1<<20, gatewayTestCredential(), map[string]string{"DataSourceUuid": "source"})
		if failure != nil || result == nil {
			t.Fatal("valid nullable int32 rejected")
		}
	}
}

func TestGatewayDataSourceResponseBoundary(t *testing.T) {
	for _, action := range []string{"ListAgenticDataSources", "GetAgenticDataSource"} {
		t.Run(action, func(t *testing.T) {
			for _, raw := range []string{`null`, `{}`, `[]`, `true`, `"source"`, `{"DataSourceUuid":null}`, `{"DataSourceUuid":" "}`, `{"DataSourceUuid":3}`, `{"DataSourceUuid":"source","State":2147483648}`, `{"DataSourceUuid":"source","State":-2147483649}`, `{"DataSourceUuid":"source","State":1.5}`, `{"DataSourceUuid":"source","State":"0"}`} {
				entity := raw
				if action == "ListAgenticDataSources" {
					entity = "[" + raw + "]"
				}
				result, err, _ := readGatewayResponse(gatewayResponse(200, `{"Success":true,"Data":`+entity+`}`), action, 1<<20, gatewayTestCredential(), map[string]string{"DataSourceUuid": "source"})
				gatewayAssertError(t, result, err, gatewayIncompatible)
			}
			for _, field := range []string{"Alias", "DbType", "EnvType", "InstanceSource", "RegionId", "CatalogUuid", "CatalogName"} {
				entity := fmt.Sprintf(`{"DataSourceUuid":"source",%q:42}`, field)
				if action == "ListAgenticDataSources" {
					entity = "[" + entity + "]"
				}
				result, err, _ := readGatewayResponse(gatewayResponse(200, `{"Success":true,"Data":`+entity+`}`), action, 1<<20, gatewayTestCredential(), map[string]string{"DataSourceUuid": "source"})
				gatewayAssertError(t, result, err, gatewayIncompatible)
			}
			for _, body := range []string{`{"Success":true}`, `{"Success":true,"Data":null}`, `{"Success":true,"Data":42}`} {
				result, err, _ := readGatewayResponse(gatewayResponse(200, body), action, 1<<20, gatewayTestCredential(), map[string]string{"DataSourceUuid": "source"})
				gatewayAssertError(t, result, err, gatewayIncompatible)
			}
		})
	}
	for _, tc := range []struct {
		action, body string
		status       int
		code         string
	}{
		{"GetAgenticDataSource", `{"Success":true,"Data":{"DataSourceUuid":"other"}}`, 200, gatewayIncompatible},
		{"GetAgenticDataSource", `{"Success":true,"Data":[{"DataSourceUuid":"source"}]}`, 200, gatewayIncompatible},
		{"ListAgenticDataSources", `{"Success":true,"Data":{"DataSourceUuid":"source"}}`, 200, gatewayIncompatible},
		{"GetAgenticDataSource", `{"Success":false,"Code":"Forbidden","Data":null}`, 200, gatewayDenied},
		{"GetAgenticDataSource", `{"Success":true,"Data":null}`, 403, gatewayDenied},
		{"GetAgenticDataSource", `{"Code":"DATA_SOURCE_NOT_FOUND","RequestId":"01A18566-AF8E-5639-94B0-EBFBEAD9CA19"}`, 404, gatewayIncompatible},
		{"ListAgenticDataSources", `{"Success":true,"Data":[],"NextToken":null}`, 200, gatewayIncompatible},
	} {
		result, err, retry := readGatewayResponse(gatewayResponse(tc.status, tc.body), tc.action, 1<<20, gatewayTestCredential(), map[string]string{"DataSourceUuid": "source"})
		gatewayAssertError(t, result, err, tc.code)
		if retry {
			t.Fatal("incompatible or denied request retried")
		}
	}
	result, err, _ := readGatewayResponse(gatewayResponse(200, `{"Success":true,"Data":[],"TotalCount":null}`), "ListAgenticDataSources", 1<<20, gatewayTestCredential(), nil)
	if err != nil || string(result.Data) != "[]" {
		t.Fatal("empty page rejected")
	}
}

func TestGatewayFixedActionsSigningAndParameters(t *testing.T) {
	page := GatewayPageOptions{PageSize: 73, NextToken: "  cursor+/=&雪  "}
	identity := "  id/with + & ?雪  "
	qualified := "  schema.[a + b]/雪  "
	tests := []struct {
		action string
		params map[string]string
		call   func(*Client) (*GatewayResult, error)
	}{
		{"ListAgenticCatalog", map[string]string{"MaxResults": "50"}, func(c *Client) (*GatewayResult, error) {
			return c.ListAgenticCatalog(context.Background(), GatewayPageOptions{})
		}},
		{"SearchAgenticCatalog", map[string]string{"MaxResults": "73", "NextToken": page.NextToken, "SearchKey": identity}, func(c *Client) (*GatewayResult, error) {
			return c.SearchAgenticCatalog(context.Background(), identity, page)
		}},
		{"ListAgenticDatabase", map[string]string{"MaxResults": "73", "NextToken": page.NextToken, "CatalogUuid": identity}, func(c *Client) (*GatewayResult, error) {
			return c.ListAgenticDatabase(context.Background(), identity, page)
		}},
		{"ListAgenticTableBaseInfo", map[string]string{"MaxResults": "73", "NextToken": page.NextToken, "DatabaseUuid": identity}, func(c *Client) (*GatewayResult, error) {
			return c.ListAgenticTableBaseInfo(context.Background(), identity, page)
		}},
		{"GetAgenticTableColumn", map[string]string{"DatabaseUuid": identity, "TableQualifiedName": qualified}, func(c *Client) (*GatewayResult, error) {
			return c.GetAgenticTableColumn(context.Background(), identity, qualified)
		}},
		{"GetAgenticTableIndex", map[string]string{"DatabaseUuid": identity, "TableQualifiedName": qualified}, func(c *Client) (*GatewayResult, error) {
			return c.GetAgenticTableIndex(context.Background(), identity, qualified)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			calls := 0
			c := gatewayTestClient(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPost || req.URL.Scheme != "https" || req.URL.Host != "dms.cn-shanghai.aliyuncs.com" || req.URL.Path != "/" {
					t.Fatalf("unexpected method or destination")
				}
				if req.Body != nil && req.Body != http.NoBody {
					t.Fatal("Agentic parameters must not be in the body")
				}
				want := url.Values{"Action": {tt.action}, "Version": {"2025-04-14"}}
				for key, value := range tt.params {
					want.Set(key, value)
				}
				if !reflect.DeepEqual(req.URL.Query(), want) || strings.Contains(req.URL.RawQuery, "+") {
					t.Fatalf("query = %v; want %v", req.URL.Query(), want)
				}
				if req.Header.Get("x-acs-action") != tt.action || req.Header.Get("x-acs-version") != "2025-04-14" ||
					req.Header.Get("x-acs-security-token") != "test-sts-token" || req.Header.Get("x-api-key") != "" {
					t.Fatal("wrong action/version/token headers")
				}
				gatewayAssertSignature(t, req, gatewayTestCredential())
				return gatewayResponse(200, `{"Success":true,"Data":[]}`), nil
			}, WithDMSUnit("never-forward-unit"), WithWorkspaceID("never-forward-workspace"), WithDataAgentEndpoint("never-use.example"))
			result, err := tt.call(c)
			if err != nil || string(result.Data) != "[]" || calls != 1 {
				t.Fatalf("result %v error %v calls %d", result, err, calls)
			}
		})
	}
}

func TestGatewayMetadataExtensionSigning(t *testing.T) {
	identity := "  id/with + & ?雪  "
	page := GatewayPageOptions{PageSize: 1, NextToken: " cursor+/=&雪 "}
	tests := []struct {
		name   string
		action string
		params map[string]string
		call   func(*Client) (*GatewayResult, error)
	}{
		{"catalog", "GetAgenticCatalog", map[string]string{"CatalogUuid": identity}, func(c *Client) (*GatewayResult, error) {
			return c.GetAgenticCatalog(context.Background(), identity)
		}},
		{"database", "GetAgenticDatabase", map[string]string{"DatabaseUuid": identity}, func(c *Client) (*GatewayResult, error) {
			return c.GetAgenticDatabase(context.Background(), identity)
		}},
		{"searchDefaults", "SearchAgenticDatabase", map[string]string{"SearchKey": identity, "MaxResults": "50"}, func(c *Client) (*GatewayResult, error) {
			return c.SearchAgenticDatabase(context.Background(), identity, GatewayDatabaseSearchOptions{}, GatewayPageOptions{})
		}},
		{"searchFilters", "SearchAgenticDatabase", map[string]string{"SearchKey": identity, "CatalogUuid": identity, "DbType": "POSTGRESQL", "EnvType": "dev", "MaxResults": "1", "NextToken": page.NextToken}, func(c *Client) (*GatewayResult, error) {
			return c.SearchAgenticDatabase(context.Background(), identity, GatewayDatabaseSearchOptions{CatalogUUID: identity, DbType: "POSTGRESQL", EnvType: "dev"}, page)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c := gatewayTestClient(func(req *http.Request) (*http.Response, error) {
				calls++
				want := url.Values{"Action": {tt.action}, "Version": {"2025-04-14"}}
				for key, value := range tt.params {
					want.Set(key, value)
				}
				if req.Method != http.MethodPost || req.URL.Host != "dms.cn-shanghai.aliyuncs.com" || !reflect.DeepEqual(req.URL.Query(), want) || (req.Body != nil && req.Body != http.NoBody) {
					t.Fatal("unexpected extension request")
				}
				gatewayAssertSignature(t, req, gatewayTestCredential())
				var data any = []any{}
				if tt.action == "GetAgenticCatalog" {
					data = map[string]string{"CatalogUuid": identity}
				} else if tt.action == "GetAgenticDatabase" {
					data = map[string]string{"DatabaseUuid": identity}
				}
				body, _ := json.Marshal(map[string]any{"Success": true, "Data": data, "RequestId": "extension-id", "NextToken": page.NextToken})
				return gatewayResponse(200, string(body)), nil
			}, WithDMSUnit("never-forward"), WithWorkspaceID("never-forward"))
			result, err := tt.call(c)
			if err != nil || calls != 1 || result.RequestID != "extension-id" {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if tt.action == "SearchAgenticDatabase" {
				if string(result.Data) != "[]" || result.NextToken != page.NextToken {
					t.Fatal("search cursor changed")
				}
			} else if result.Data[0] != '{' || result.NextToken != "" {
				t.Fatal("detail shape or pagination changed")
			}
		})
	}
}

func TestGatewayMetadataExtensionValidation(t *testing.T) {
	c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid extension request reached HTTP")
		return nil, nil
	}, WithStrictCredentialProvider(func() (*Credential, error) {
		t.Fatal("invalid extension request resolved credentials")
		return nil, nil
	}))
	for _, value := range []string{"", " \t"} {
		result, err := c.GetAgenticCatalog(context.Background(), value)
		gatewayAssertError(t, result, err, gatewayInvalid)
		result, err = c.GetAgenticDatabase(context.Background(), value)
		gatewayAssertError(t, result, err, gatewayInvalid)
		result, err = c.SearchAgenticDatabase(context.Background(), value, GatewayDatabaseSearchOptions{}, GatewayPageOptions{})
		gatewayAssertError(t, result, err, gatewayInvalid)
	}
	for _, filters := range []GatewayDatabaseSearchOptions{
		{CatalogUUID: " \t"}, {DbType: "mysql"}, {DbType: " MYSQL"}, {DbType: "MYSQL-8"},
		{DbType: "1MYSQL"}, {DbType: "数据库"}, {EnvType: "DEV"}, {EnvType: "production"}, {EnvType: "dev "},
	} {
		result, err := c.SearchAgenticDatabase(context.Background(), "db", filters, GatewayPageOptions{})
		gatewayAssertError(t, result, err, gatewayInvalid)
	}
	for _, env := range []string{"product", "pre", "dev", "test", "sit", "uat", "pet", "stag"} {
		if !GatewayEnvironmentTypeValid(env) {
			t.Fatalf("valid environment rejected: %s", env)
		}
	}
	for _, db := range []string{"MYSQL", "POSTGRESQL", "ALIYUN_ADB_MYSQL", "DB2"} {
		if !GatewayDatabaseTypeValid(db) {
			t.Fatalf("valid database type rejected: %s", db)
		}
	}
	for _, action := range []string{"GetAgenticCatalog", "GetAgenticDatabase", "SearchAgenticDatabase"} {
		for _, extra := range []string{"Action", "RoleArn", "Endpoint", "Confirm", "Host", "State"} {
			params := map[string]string{"CatalogUuid": "c"}
			if action == "GetAgenticDatabase" {
				params = map[string]string{"DatabaseUuid": "d"}
			} else if action == "SearchAgenticDatabase" {
				params = map[string]string{"SearchKey": "d", "MaxResults": "50"}
			}
			params[extra] = "untrusted"
			result, err := c.callGateway(context.Background(), action, params)
			gatewayAssertError(t, result, err, gatewayInvalid)
		}
	}
	for _, key := range []string{"CatalogUuid", "DbType", "EnvType"} {
		params := map[string]string{"SearchKey": "d", "MaxResults": "50", key: ""}
		result, err := c.callGateway(context.Background(), "SearchAgenticDatabase", params)
		gatewayAssertError(t, result, err, gatewayInvalid)
	}
}

func TestGatewayMetadataExtensionResponseContract(t *testing.T) {
	tests := []struct {
		name, action, data string
		params             map[string]string
		valid              bool
	}{
		{"catalog", "GetAgenticCatalog", `{"CatalogUuid":" c ","Description":null,"State":0,"Properties":{"Password":"secret"},"CatalogBizAttrs":{"HOST":"secret"},"StorageLocation":"secret"}`, map[string]string{"CatalogUuid": " c "}, true},
		{"database", "GetAgenticDatabase", `{"DatabaseUuid":"d","CatalogUuid":"c","EngineMeta":{"StorageCapacity":9223372036854775807,"Encoding":null,"Password":"secret"},"DatabaseBizAttrs":{"Password":"secret"},"Unknown":0.1234567890123456789}`, map[string]string{"DatabaseUuid": "d"}, true},
		{"search", "SearchAgenticDatabase", `[{"DatabaseUuid":"d","CatalogUuid":"c","EngineMeta":{"StorageCapacity":9007199254740993,"Password":"secret"}}]`, map[string]string{"CatalogUuid": "c"}, true},
		{"searchUnscoped", "SearchAgenticDatabase", `[{"DatabaseUuid":"d","CatalogUuid":"other"}]`, nil, true},
		{"searchCatalogAbsent", "SearchAgenticDatabase", `[{"DatabaseUuid":"d"}]`, map[string]string{"CatalogUuid": "c"}, true},
		{"searchCatalogNull", "SearchAgenticDatabase", `[{"DatabaseUuid":"d","CatalogUuid":null}]`, map[string]string{"CatalogUuid": "c"}, true},
		{"searchEmpty", "SearchAgenticDatabase", `[]`, nil, true},
		{"catalogMismatch", "GetAgenticCatalog", `{"CatalogUuid":"other"}`, map[string]string{"CatalogUuid": "c"}, false},
		{"catalogWhitespaceMismatch", "GetAgenticCatalog", `{"CatalogUuid":"c"}`, map[string]string{"CatalogUuid": " c "}, false},
		{"databaseMismatch", "GetAgenticDatabase", `{"DatabaseUuid":"other"}`, map[string]string{"DatabaseUuid": "d"}, false},
		{"searchMixedCatalogs", "SearchAgenticDatabase", `[{"DatabaseUuid":"d","CatalogUuid":"c"},{"DatabaseUuid":"e","CatalogUuid":"other"}]`, map[string]string{"CatalogUuid": "c"}, false},
		{"searchCatalogType", "SearchAgenticDatabase", `[{"DatabaseUuid":"d","CatalogUuid":42}]`, nil, false},
		{"detailArray", "GetAgenticCatalog", `[{"CatalogUuid":"c"}]`, map[string]string{"CatalogUuid": "c"}, false},
		{"databaseDetailArray", "GetAgenticDatabase", `[{"DatabaseUuid":"d"}]`, map[string]string{"DatabaseUuid": "d"}, false},
		{"searchObject", "SearchAgenticDatabase", `{"DatabaseUuid":"d"}`, nil, false},
		{"overflow", "GetAgenticDatabase", `{"DatabaseUuid":"d","EngineMeta":{"StorageCapacity":9223372036854775808}}`, map[string]string{"DatabaseUuid": "d"}, false},
		{"decimal", "GetAgenticDatabase", `{"DatabaseUuid":"d","EngineMeta":{"StorageCapacity":0.1234567890123456789}}`, map[string]string{"DatabaseUuid": "d"}, false},
	}
	for _, action := range []string{"GetAgenticCatalog", "GetAgenticDatabase", "SearchAgenticDatabase"} {
		for _, data := range []string{"null", "{}", `"text"`, "[null]"} {
			tests = append(tests, struct {
				name, action, data string
				params             map[string]string
				valid              bool
			}{action + data, action, data, nil, false})
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := gatewayResponse(200, `{"Success":true,"RequestId":"extension-id","Data":`+tt.data+`,"NextToken":" next ","MaxResults":50}`)
			result, err, retry := readGatewayResponse(resp, tt.action, 1<<20, gatewayTestCredential(), tt.params)
			if !tt.valid {
				failure := gatewayAssertError(t, result, err, gatewayIncompatible)
				if retry || failure.RequestID != "extension-id" {
					t.Fatal("incompatible response retried or lost request ID")
				}
				return
			}
			if err != nil || retry || result.RequestID != "extension-id" || strings.Contains(string(result.Data), "secret") {
				t.Fatalf("invalid filtered response: %v", err)
			}
			if tt.name == "database" && (!bytes.Contains(result.Data, []byte("9223372036854775807")) || !bytes.Contains(result.Data, []byte(`"Encoding":null`))) {
				t.Fatal("detail precision or NULL lost")
			}
			if tt.name == "search" && !bytes.Contains(result.Data, []byte("9007199254740993")) {
				t.Fatal("search precision lost")
			}
		})
	}
	for _, action := range []string{"GetAgenticCatalog", "GetAgenticDatabase", "SearchAgenticDatabase"} {
		for _, body := range []string{`{"Success":true}`, `{"Success":true,"Data":null,"ErrorCode":"Forbidden"}`} {
			result, err, retry := readGatewayResponse(gatewayResponse(200, body), action, 1024, gatewayTestCredential(), nil)
			code := gatewayIncompatible
			if strings.Contains(body, "Forbidden") {
				code = gatewayDenied
			}
			gatewayAssertError(t, result, err, code)
			if retry {
				t.Fatal("invalid or denied response retried")
			}
		}
	}
}

func TestGatewayInvalidArgumentsDoNotResolveCredentialsOrSend(t *testing.T) {
	c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid arguments reached HTTP")
		return nil, nil
	}, WithStrictCredentialProvider(func() (*Credential, error) {
		t.Fatal("invalid arguments reached provider")
		return nil, nil
	}))
	calls := []func() (*GatewayResult, error){
		func() (*GatewayResult, error) {
			return c.ListAgenticCatalog(context.Background(), GatewayPageOptions{PageSize: -1})
		},
		func() (*GatewayResult, error) {
			return c.ListAgenticCatalog(context.Background(), GatewayPageOptions{PageSize: 101})
		},
		func() (*GatewayResult, error) {
			return c.SearchAgenticCatalog(context.Background(), " \t", GatewayPageOptions{})
		},
		func() (*GatewayResult, error) {
			return c.ListAgenticDatabase(context.Background(), "", GatewayPageOptions{})
		},
		func() (*GatewayResult, error) {
			return c.ListAgenticTableBaseInfo(context.Background(), " ", GatewayPageOptions{})
		},
		func() (*GatewayResult, error) { return c.GetAgenticTableColumn(context.Background(), "db", "") },
		func() (*GatewayResult, error) { return c.GetAgenticTableIndex(context.Background(), "", "table") },
		func() (*GatewayResult, error) { return c.callGateway(context.Background(), "ExecuteScript", nil) },
		func() (*GatewayResult, error) {
			return c.callGateway(context.Background(), "ListAgenticCatalog", map[string]string{"MaxResults": "50", "Action": "ExecuteScript"})
		},
		func() (*GatewayResult, error) {
			return c.callGateway(context.Background(), "GetAgenticTableIndex", map[string]string{"DatabaseUuid": "db", "TableQualifiedName": "t", "NextToken": "x"})
		},
	}
	for _, call := range calls {
		result, err := call()
		gatewayAssertError(t, result, err, gatewayInvalid)
	}
}

func TestGatewayEndpointAndLimits(t *testing.T) {
	for _, endpoint := range []string{"", "https://dms.cn-shanghai.aliyuncs.com", "dms.cn-beijing.aliyuncs.com", "dms.cn-shanghai.aliyuncs.com/", "dms.cn-shanghai.aliyuncs.com:443", "dms.cn-shanghai.aliyuncs.com?x=1", "dms.cn-shanghai.aliyuncs.com#fragment", "user@dms.cn-shanghai.aliyuncs.com", "dms.cn-shanghai.aliyuncs.com.attacker.test", "localhost", "127.0.0.1", "DMS.cn-shanghai.aliyuncs.com", "dms-vpc.cn-shanghai.aliyuncs.com"} {
		t.Run(endpoint, func(t *testing.T) {
			c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid endpoint reached network")
				return nil, nil
			}, WithGatewayOptions(GatewayOptions{Endpoint: endpoint}))
			result, err := c.ListAgenticCatalog(context.Background(), GatewayPageOptions{})
			gatewayAssertError(t, result, err, gatewayInvalid)
		})
	}
	c := gatewayTestClient(nil)
	for _, limits := range []GatewayOptions{
		{RequestTimeout: -time.Second}, {MaxResponseBytes: -1}, {MaxResponseBytes: math.MaxInt64},
	} {
		limits.Endpoint = "dms.cn-shanghai.aliyuncs.com"
		c.gateway = limits
		if _, err := c.gatewayOptions(); err == nil {
			t.Fatal("invalid limits accepted")
		}
	}
	c.gateway = GatewayOptions{Endpoint: "dms.cn-shanghai.aliyuncs.com"}
	got, err := c.gatewayOptions()
	if err != nil || got.RequestTimeout != 30*time.Second || got.MaxResponseBytes != 4<<20 {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	for _, region := range []string{"", "cn-shanghai.attacker", "unverified-region"} {
		c.region = region
		c.gateway.Endpoint = "dms." + region + ".aliyuncs.com"
		if _, err := c.gatewayOptions(); err == nil {
			t.Fatal("unverified region accepted")
		}
	}
}

// The fixtures use the official models referenced in gateway.go. In particular:
// TableBaseInfo uses Name, not TableName; Index uses Primary/Unique and arrays
// ColumnNames/RealColumnNames, not IsPrimary/IsUnique or a guessed Columns model.
func TestGatewayWhitelistPrecisionNullAndWhitespace(t *testing.T) {
	tests := []struct{ action, data, want string }{
		{"ListAgenticCatalog", `[{"CatalogUuid":"  mc-1  ","Name":"\t name  ","Description":null,"State":0,"Properties":{"Password":"secret"},"CatalogBizAttrs":{"HOST":"private-query","SecurityToken":"secret"},"StorageLocation":"https://private-query","Unknown":123.1234567890123456789}]`, `[{"CatalogUuid":"  mc-1  ","Name":"\t name  ","Description":null,"State":0}]`},
		{"SearchAgenticCatalog", `[{"CatalogUuid":"mc-2","CatalogType":"JDBC","DataSourceUuid":"ds-2","DbType":"MYSQL","RegionId":"cn-shanghai","State":-1}]`, `[{"CatalogUuid":"mc-2","CatalogType":"JDBC","DataSourceUuid":"ds-2","DbType":"MYSQL","RegionId":"cn-shanghai","State":-1}]`},
		{"ListAgenticDatabase", `[{"DatabaseUuid":"md-1","CatalogUuid":"mc-1","CatalogName":"c","CatalogType":"JDBC","QualifiedName":"  db.public  ","Name":"public","Description":null,"SearchName":"db","RegionId":"cn-shanghai","DbType":"MYSQL","State":0,"EngineMeta":{"CatalogName":"def","Encoding":"utf8","SchemaName":"public","StorageCapacity":9007199254740993,"Password":"secret"},"DatabaseBizAttrs":{"Password":"secret"},"Properties":{"AccessSecret":"secret"}}]`, `[{"DatabaseUuid":"md-1","CatalogUuid":"mc-1","CatalogName":"c","CatalogType":"JDBC","QualifiedName":"  db.public  ","Name":"public","Description":null,"SearchName":"db","RegionId":"cn-shanghai","DbType":"MYSQL","State":0,"EngineMeta":{"CatalogName":"def","Encoding":"utf8","SchemaName":"public","StorageCapacity":9007199254740993}}]`},
		{"ListAgenticTableBaseInfo", `[{"QualifiedName":"  dbo.table  ","DatabaseUuid":"md-1","DatabaseQualifiedName":"db","Name":"table","CatalogType":"JDBC","TableType":"NORMAL","EngineMeta":{"DataBytes":9223372036854775807,"IndexBytes":9007199254740993,"NumRows":9007199254740995,"StorageCapacity":100,"Engine":"InnoDB","Encoding":null,"TableSchemaName":"dbo","CreateTime":"2026-01-02 03:04:05","LastDdlTime":null,"RefInfo":"secret","Checksum":"secret","Properties":{"AccessKey":"secret"}},"TableName":"wrong"}]`, `[{"QualifiedName":"  dbo.table  ","DatabaseUuid":"md-1","DatabaseQualifiedName":"db","Name":"table","CatalogType":"JDBC","TableType":"NORMAL","EngineMeta":{"DataBytes":9223372036854775807,"IndexBytes":9007199254740993,"NumRows":9007199254740995,"StorageCapacity":100,"Engine":"InnoDB","Encoding":null,"TableSchemaName":"dbo","CreateTime":"2026-01-02 03:04:05","LastDdlTime":null}}]`},
		{"GetAgenticTableColumn", `[{"ColumnName":" price ","ColumnType":"decimal(38,20)","Description":"0.12345678901234567890","Position":1,"EngineMeta":{"AutoIncrement":false,"DataLength":9007199254740993,"DataPrecision":38,"DataScale":20,"Encoding":null,"GenerationColumn":false,"Nullable":true,"DefaultValue":"secret","Extra":"secret","GenerationExpression":"secret","Password":"secret"}},{"ColumnName":"second","EngineMeta":null}]`, `[{"ColumnName":" price ","ColumnType":"decimal(38,20)","Description":"0.12345678901234567890","Position":1,"EngineMeta":{"AutoIncrement":false,"DataLength":9007199254740993,"DataPrecision":38,"DataScale":20,"Encoding":null,"GenerationColumn":false,"Nullable":true}},{"ColumnName":"second","EngineMeta":null}]`},
		{"GetAgenticTableIndex", `[{"IndexName":"idx","IndexType":"Unique","Description":null,"Primary":false,"Unique":true,"ColumnNames":[" name(10) ","id"],"RealColumnNames":[" name ","id"],"EngineMeta":{"Password":"secret"},"IsPrimary":true,"Columns":[{"secret":true}]}]`, `[{"IndexName":"idx","IndexType":"Unique","Description":null,"Primary":false,"Unique":true,"ColumnNames":[" name(10) ","id"],"RealColumnNames":[" name ","id"]}]`},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			resp := gatewayResponse(200, " \n\t"+`{"Success":true,"RequestId":"upstream-123","Data":`+tt.data+"} \r\n")
			result, err, retry := readGatewayResponse(resp, tt.action, 1<<20, gatewayTestCredential(), nil)
			if err != nil || retry {
				t.Fatalf("unexpected error %v / retry %v", err, retry)
			}
			decode := func(s string) interface{} {
				var v interface{}
				d := json.NewDecoder(strings.NewReader(s))
				d.UseNumber()
				if err := d.Decode(&v); err != nil {
					t.Fatal(err)
				}
				return v
			}
			if !reflect.DeepEqual(decode(string(result.Data)), decode(tt.want)) || result.RequestID != "upstream-123" {
				t.Fatalf("data = %s; want %s", result.Data, tt.want)
			}
			if strings.Contains(string(result.Data), "secret") || strings.Contains(string(result.Data), "private-query") {
				t.Fatal("extended attributes leaked")
			}
		})
	}
}

func TestGatewayRejectsIncompatibleJSONAndEntityTypes(t *testing.T) {
	tests := []struct{ action, body string }{
		{"ListAgenticCatalog", "not JSON https://private-query"},
		{"ListAgenticCatalog", `{"Success":true,"Data":[]} {}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[]} null`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[]} trailing`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[}`},
		{"ListAgenticCatalog", `{"Success":false,"Success":true,"Data":[]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":{},"Data":[]}`},
		{"ListAgenticCatalog", "{\"Success\":true,\"Data\":[{\"CatalogUuid\":\"\xff\"}]}"},
		{"ListAgenticCatalog", `null`},
		{"ListAgenticCatalog", `[]`},
		{"ListAgenticCatalog", `{}`},
		{"ListAgenticCatalog", `{"Success":true}`},
		{"ListAgenticCatalog", `{"Data":[]}`},
		{"ListAgenticCatalog", `{"Success":null,"Data":[]}`},
		{"ListAgenticCatalog", `{"Success":"true","Data":[]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":null}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":{}}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":{"CatalogUuid":"c"}}`},
		{"SearchAgenticCatalog", `{"Success":true,"Data":{"CatalogUuid":"c"}}`},
		{"ListAgenticDatabase", `{"Success":true,"Data":{"DatabaseUuid":"d"}}`},
		{"ListAgenticTableBaseInfo", `{"Success":true,"Data":{"QualifiedName":"t"}}`},
		{"GetAgenticTableColumn", `{"Success":true,"Data":{"ColumnName":"c"}}`},
		{"GetAgenticTableIndex", `{"Success":true,"Data":{"IndexName":"i"}}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[null]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[[]]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[{"Name":"missing identifier"}]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[{"CatalogUuid":null}]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[{"CatalogUuid":" \t"}]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[{"CatalogUuid":123}]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[{"CatalogUuid":"c","State":1.234567890123456789}]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[{"CatalogUuid":"c","State":2147483648}]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[{"CatalogUuid":"c","Name":{}}]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[],"NextToken":123}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[],"NextToken":null}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[],"MaxResults":"50"}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[],"RequestId":{}}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[],"ErrorCode":[]}`},
		{"ListAgenticCatalog", `{"Success":true,"Data":[],"ErrorMessage":{"secret":true}}`},
		{"ListAgenticDatabase", `{"Success":true,"Data":[{"CatalogUuid":"c"}]}`},
		{"ListAgenticDatabase", `{"Success":true,"Data":[{"DatabaseUuid":"db","EngineMeta":[]}]}`},
		{"ListAgenticTableBaseInfo", `{"Success":true,"Data":[{"TableName":"t"}]}`},
		{"ListAgenticTableBaseInfo", `{"Success":true,"Data":[{"QualifiedName":"t","EngineMeta":{"NumRows":"100"}}]}`},
		{"ListAgenticTableBaseInfo", `{"Success":true,"Data":[{"QualifiedName":"t","EngineMeta":{"DataBytes":9223372036854775808}}]}`},
		{"GetAgenticTableColumn", `{"Success":true,"Data":[{"ColumnName":"c","EngineMeta":{"Nullable":"true"}}]}`},
		{"GetAgenticTableIndex", `{"Success":true,"Data":[{"IndexName":"i","ColumnNames":"id"}]}`},
		{"GetAgenticTableIndex", `{"Success":true,"Data":[{"IndexName":"i","ColumnNames":[null]}]}`},
		{"GetAgenticTableIndex", `{"Success":true,"Data":[{"IndexName":"i","RealColumnNames":[3]}]}`},
		{"GetAgenticTableIndex", `{"Success":true,"Data":[{"IndexName":"i","Primary":1}]}`},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("%s/%d", tt.action, i), func(t *testing.T) {
			resp := gatewayResponse(200, tt.body)
			resp.Header.Set("x-acs-request-id", "header-id")
			result, err, retry := readGatewayResponse(resp, tt.action, 1<<20, gatewayTestCredential(), nil)
			failure := gatewayAssertError(t, result, err, gatewayIncompatible)
			if failure.RequestID != "header-id" || retry {
				t.Fatal("lost ID or retried incompatible data")
			}
		})
	}
}

func TestGatewayPaginationAndRequestIDCollection(t *testing.T) {
	for _, action := range []string{"ListAgenticCatalog", "SearchAgenticCatalog", "ListAgenticDatabase", "ListAgenticTableBaseInfo", "GetAgenticTableColumn", "GetAgenticTableIndex"} {
		t.Run(action, func(t *testing.T) {
			resp := gatewayResponse(200, `{"Success":true,"Data":[],"NextToken":"  opaque + /= token  ","pagination":{"next_token":"wrong"},"MaxResults":50}`)
			result, err, _ := readGatewayResponse(resp, action, 4096, gatewayTestCredential(), nil)
			if err != nil || string(result.Data) != "[]" {
				t.Fatalf("result %v, error %v", result, err)
			}
			want := ""
			if gatewayPaged(action) {
				want = "  opaque + /= token  "
			}
			if result.NextToken != want || result.RequestID != "" {
				t.Fatal("fabricated cursor or request ID")
			}
		})
	}
	calls := 0
	c := gatewayTestClient(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return gatewayResponse(200, `{"Success":true,"Data":[],"NextToken":" next+/= ","RequestId":"page-one"}`), nil
		}
		if req.URL.Query().Get("NextToken") != " next+/= " {
			t.Fatal("cursor altered")
		}
		resp := gatewayResponse(200, `{"Success":true,"Data":[],"NextToken":""}`)
		resp.Header.Set("x-acs-request-id", "page-two")
		return resp, nil
	})
	ctx, ids := WithRequestIDs(context.Background())
	first, err := c.ListAgenticCatalog(ctx, GatewayPageOptions{})
	if err != nil || calls != 1 {
		t.Fatal("automatically fetched extra pages")
	}
	second, err := c.ListAgenticCatalog(ctx, GatewayPageOptions{NextToken: first.NextToken})
	if err != nil || second.NextToken != "" || second.RequestID != "page-two" {
		t.Fatalf("second page: %v, %v", second, err)
	}
	want := []APIRequest{{Action: "ListAgenticCatalog", RequestID: "page-one"}, {Action: "ListAgenticCatalog", RequestID: "page-two"}}
	if !reflect.DeepEqual(ids.Snapshot(), want) {
		t.Fatalf("IDs = %+v", ids.Snapshot())
	}
}

func TestGatewayHTTPAndBusinessErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body, code string
		retry      bool
		calls      int
	}{
		{"unauthorized", 401, `{"Code":"InvalidAccessKeyId.NotFound","Message":"test-access-secret"}`, gatewayAuth, false, 1},
		{"forbidden", 403, `{"ErrorCode":"Forbidden.RAM","ErrorMessage":"https://private-query"}`, gatewayDenied, false, 1},
		{"forbiddenHTML", 403, `<html>test-sts-token</html>`, gatewayDenied, false, 1},
		{"authBusiness", 200, `{"Success":false,"ErrorCode":"InvalidSecurityToken.Expired"}`, gatewayAuth, false, 1},
		{"permissionBusiness", 200, `{"Success":false,"ErrorCode":"AccessDenied","ErrorMessage":"private-query"}`, gatewayDenied, false, 1},
		{"standardBusiness", 200, `{"Code":"SignatureDoesNotMatch","Message":"test-access-secret"}`, gatewayAuth, false, 1},
		{"contradiction", 200, `{"Success":true,"Data":[],"Code":"Forbidden","Message":"private-query"}`, gatewayDenied, false, 1},
		{"rateHTTP", 429, `not-json-private-query`, gatewayLimited, true, 2},
		{"rateBusiness", 200, `{"Success":false,"ErrorCode":"Throttling.User"}`, gatewayLimited, true, 2},
		{"badCursor", 400, `{"Code":"InvalidNextToken","Message":"private-query"}`, gatewayInvalid, false, 1},
		{"unsupportedAction", 400, `{"Code":"InvalidAction.NotFound","Message":"private-query"}`, gatewayIncompatible, false, 1},
		{"notFound", 404, `{"Message":"private-query"}`, gatewayIncompatible, false, 1},
		{"serverErrorNoAutomaticRetry", 503, `{"Code":"ServiceUnavailable","Message":"private-query"}`, gatewayUnavailable, true, 1},
		{"unknownBusiness", 200, `{"Success":false,"ErrorCode":"Unrecognized","ErrorMessage":"private-query"}`, gatewayIncompatible, false, 1},
		{"falseWithoutCode", 200, `{"Success":false,"Data":[]}`, gatewayIncompatible, false, 1},
		{"errorMessageOnly", 200, `{"Success":true,"ErrorMessage":"private-query","Data":[]}`, gatewayIncompatible, false, 1},
		{"embeddedStatus", 200, `{"Success":true,"HttpStatusCode":403,"Data":[]}`, gatewayDenied, false, 1},
		{"badEmbeddedStatus", 200, `{"Success":true,"HttpStatusCode":"403","Data":[]}`, gatewayIncompatible, false, 1},
		{"alternateErrorCode", 200, `{"Success":true,"error_code":"Forbidden","Data":[]}`, gatewayDenied, false, 1},
		{"conflictingSuccess", 200, `{"Success":true,"success":false,"Data":[]}`, gatewayIncompatible, false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
				calls++
				resp := gatewayResponse(tt.status, tt.body)
				resp.Header.Set("x-acs-request-id", fmt.Sprintf("request-%d", calls))
				resp.Header.Set("Retry-After", "0")
				return resp, nil
			})
			ctx, ids := WithRequestIDs(context.Background())
			result, err := c.ListAgenticCatalog(ctx, GatewayPageOptions{})
			failure := gatewayAssertError(t, result, err, tt.code)
			if failure.Retryable != tt.retry || calls != tt.calls || failure.RequestID != fmt.Sprintf("request-%d", calls) || len(ids.Snapshot()) != calls {
				t.Fatalf("failure %+v calls %d ids %+v", failure, calls, ids.Snapshot())
			}
		})
	}
}

func TestGatewaySanitizesRequestIDsAndErrors(t *testing.T) {
	for _, id := range []string{"https://private-query?SecurityToken=test-sts-token", "test-access-secret", "prefix-test-sts-token", "Authorization: secret", "bad\r\nheader", strings.Repeat("a", 129)} {
		c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
			body, _ := json.Marshal(map[string]interface{}{"Success": false, "ErrorCode": "Forbidden", "ErrorMessage": "test-access-secret", "RequestId": id})
			resp := gatewayResponse(200, string(body))
			resp.Header.Set("x-acs-request-id", id)
			return resp, nil
		})
		ctx, ids := WithRequestIDs(context.Background())
		result, err := c.ListAgenticCatalog(ctx, GatewayPageOptions{})
		failure := gatewayAssertError(t, result, err, gatewayDenied)
		if failure.RequestID != "" || len(ids.Snapshot()) != 0 {
			t.Fatal("unsafe request ID propagated")
		}
	}
	unsafe := &GatewayError{Code: "https://private-query", Message: "test-access-secret", RequestID: "test-sts-token"}
	if strings.Contains(unsafe.Error(), "test-") || strings.Contains(unsafe.Error(), "https://") {
		t.Fatal("Error() leaked externally constructed fields")
	}
}

func TestGatewayStrictCredentialsRefreshAndNoFallback(t *testing.T) {
	for _, test := range []string{"legacyOnly", "strictError", "strictNil", "emptyID", "emptySecret", "whitespaceID", "tokenHeaderInjection"} {
		t.Run(test, func(t *testing.T) {
			c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid credentials reached HTTP")
				return nil, nil
			}, WithCredentialProvider(func() *Credential { t.Fatal("legacy provider invoked"); return gatewayTestCredential() }))
			if test != "legacyOnly" {
				c.strictCredFn = func() (*Credential, error) {
					cred := gatewayTestCredential()
					switch test {
					case "strictError":
						return cred, errors.New("refresh failed https://private-query test-access-secret")
					case "strictNil":
						return nil, nil
					case "emptyID":
						cred.AccessKeyID = ""
					case "emptySecret":
						cred.AccessKeySecret = ""
					case "whitespaceID":
						cred.AccessKeyID = "  "
					case "tokenHeaderInjection":
						cred.SecurityToken = "abc\r\nAuthorization: secret"
					}
					return cred, nil
				}
			}
			result, err := c.ListAgenticCatalog(context.Background(), GatewayPageOptions{})
			gatewayAssertError(t, result, err, gatewayCredential)
		})
	}
	var generation, calls int
	c := gatewayTestClient(func(req *http.Request) (*http.Response, error) {
		calls++
		cred := &Credential{AccessKeyID: fmt.Sprintf("new-id-%d", generation), AccessKeySecret: "new-secret", SecurityToken: fmt.Sprintf("new-token-%d", generation)}
		gatewayAssertSignature(t, req, cred)
		if req.Header.Get("x-acs-security-token") != cred.SecurityToken {
			t.Fatal("old token used")
		}
		return gatewayResponse(200, `{"Success":true,"Data":[]}`), nil
	}, WithStrictCredentialProvider(func() (*Credential, error) {
		generation++
		if generation == 3 {
			return gatewayTestCredential(), errors.New("refresh failed test-access-secret")
		}
		return &Credential{AccessKeyID: fmt.Sprintf("new-id-%d", generation), AccessKeySecret: "new-secret", SecurityToken: fmt.Sprintf("new-token-%d", generation)}, nil
	}))
	for i := 0; i < 2; i++ {
		if _, err := c.ListAgenticCatalog(context.Background(), GatewayPageOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := c.ListAgenticCatalog(context.Background(), GatewayPageOptions{})
	gatewayAssertError(t, result, err, gatewayCredential)
	if calls != 2 {
		t.Fatal("refresh failure fell back to an old snapshot")
	}
	c.cred, c.strictCredFn, c.credFn = nil, nil, nil
	result, err = c.ListAgenticCatalog(context.Background(), GatewayPageOptions{})
	gatewayAssertError(t, result, err, gatewayCredential)
}

func TestGatewayResponseCapIncludesDecompressedErrors(t *testing.T) {
	for _, status := range []int{200, 403, 429} {
		for _, compressed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/gzip=%v", status, compressed), func(t *testing.T) {
				body := `{"Success":true,"Data":[],"Padding":"` + strings.Repeat("a", 10000) + `"}`
				var buffer bytes.Buffer
				if compressed {
					w := gzip.NewWriter(&buffer)
					_, _ = w.Write([]byte(body))
					_ = w.Close()
				} else {
					buffer.WriteString(body)
				}
				calls := 0
				c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
					calls++
					resp := gatewayResponse(status, buffer.String())
					resp.Header.Set("x-acs-request-id", "large-response-id")
					if compressed {
						resp.Header.Set("Content-Encoding", "gzip")
					}
					return resp, nil
				}, WithGatewayOptions(GatewayOptions{Endpoint: "dms.cn-shanghai.aliyuncs.com", MaxResponseBytes: 256}))
				result, err := c.ListAgenticCatalog(context.Background(), GatewayPageOptions{})
				failure := gatewayAssertError(t, result, err, gatewayTooLarge)
				if calls != 1 || failure.RequestID != "large-response-id" {
					t.Fatal("oversize error retried or ID lost")
				}
			})
		}
	}
	body := `{"Success":true,"Data":[]}`
	for _, compressed := range []bool{false, true} {
		var buffer bytes.Buffer
		if compressed {
			w := gzip.NewWriter(&buffer)
			_, _ = w.Write([]byte(body))
			_ = w.Close()
		} else {
			buffer.WriteString(body)
		}
		resp := gatewayResponse(200, buffer.String())
		if compressed {
			resp.Header.Set("Content-Encoding", "gzip")
		}
		result, err, _ := readGatewayResponse(resp, "ListAgenticCatalog", int64(len(body)), gatewayTestCredential(), nil)
		if err != nil || string(result.Data) != "[]" {
			t.Fatalf("exact limit rejected: %v", err)
		}
	}
	resp := gatewayResponse(200, "not a gzip https://private-query")
	resp.Header.Set("Content-Encoding", "gzip")
	result, err, _ := readGatewayResponse(resp, "ListAgenticCatalog", 256, gatewayTestCredential(), nil)
	gatewayAssertError(t, result, err, gatewayIncompatible)
	resp = gatewayResponse(200, body)
	resp.Header.Set("Content-Encoding", "br")
	result, err, _ = readGatewayResponse(resp, "ListAgenticCatalog", 256, gatewayTestCredential(), nil)
	gatewayAssertError(t, result, err, gatewayIncompatible)
}

func TestGatewayRedirectNeverFollowedAndDedicatedClient(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		calls := 0
		c := gatewayTestClient(func(req *http.Request) (*http.Response, error) {
			calls++
			if calls != 1 || req.URL.Host != "dms.cn-shanghai.aliyuncs.com" || req.Header.Get("Cookie") != "" {
				t.Fatal("redirect followed or cookie leaked")
			}
			resp := gatewayResponse(status, `{"Success":true,"Data":[]}`)
			resp.Header.Set("Location", "https://attacker.test/steal")
			return resp, nil
		})
		jar, _ := cookiejar.New(nil)
		u, _ := url.Parse("https://dms.cn-shanghai.aliyuncs.com")
		jar.SetCookies(u, []*http.Cookie{{Name: "secret", Value: "never-copy"}})
		c.http.Jar = jar
		c.http.Timeout = time.Nanosecond
		c.http.CheckRedirect = func(*http.Request, []*http.Request) error { t.Fatal("general redirect policy used"); return nil }
		result, err := c.ListAgenticCatalog(context.Background(), GatewayPageOptions{})
		gatewayAssertError(t, result, err, gatewayIncompatible)
		if calls != 1 {
			t.Fatalf("redirect calls = %d", calls)
		}
	}
}

func TestGatewayRetriesOnlyThrottleOrTransientNetworkOnce(t *testing.T) {
	for _, mode := range []string{"throttleThenOK", "networkThenOK", "networkTwice", "permanentNetwork", "bodyReset"} {
		t.Run(mode, func(t *testing.T) {
			calls, providerCalls := 0, 0
			nonces := make(map[string]bool)
			c := gatewayTestClient(func(req *http.Request) (*http.Response, error) {
				calls++
				nonce := req.Header.Get("x-acs-signature-nonce")
				if nonces[nonce] {
					t.Fatal("retry reused signature nonce")
				}
				nonces[nonce] = true
				switch mode {
				case "networkTwice":
					return nil, syscall.ECONNRESET
				case "permanentNetwork":
					return nil, errors.New("https://private-query test-access-secret")
				case "networkThenOK":
					if calls == 1 {
						return nil, syscall.ECONNRESET
					}
				case "bodyReset":
					if calls == 1 {
						resp := gatewayResponse(200, "")
						resp.Body = io.NopCloser(gatewayErrorReader{err: syscall.ECONNRESET})
						return resp, nil
					}
				case "throttleThenOK":
					if calls == 1 {
						resp := gatewayResponse(200, `{"Success":false,"ErrorCode":"Throttling.Api","RequestId":"throttle-id"}`)
						resp.Header.Set("Retry-After", "0")
						return resp, nil
					}
				}
				return gatewayResponse(200, `{"Success":true,"Data":[],"RequestId":"ok-id"}`), nil
			}, WithStrictCredentialProvider(func() (*Credential, error) { providerCalls++; return gatewayTestCredential(), nil }))
			result, err := c.ListAgenticCatalog(context.Background(), GatewayPageOptions{})
			if mode == "networkTwice" || mode == "permanentNetwork" {
				failure := gatewayAssertError(t, result, err, gatewayUnavailable)
				if failure.Retryable != (mode == "networkTwice") {
					t.Fatal("incorrect network retryability")
				}
			} else if err != nil || result.RequestID != "ok-id" {
				t.Fatalf("result %v, err %v", result, err)
			}
			wantCalls := 2
			if mode == "permanentNetwork" {
				wantCalls = 1
			}
			if calls != wantCalls || providerCalls != 1 {
				t.Fatalf("HTTP attempts=%d provider calls=%d", calls, providerCalls)
			}
		})
	}
}

type gatewayErrorReader struct{ err error }

func (r gatewayErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestGatewayRetryAfterBudget(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		value string
		want  time.Duration
	}{
		{"0", 0}, {"2", 2 * time.Second}, {now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second},
		{now.Add(-time.Second).Format(http.TimeFormat), 0}, {"invalid", 100 * time.Millisecond},
		{"99999999999999999999999999", time.Duration(math.MaxInt64)},
	} {
		if got := gatewayRetryDelay(tt.value, now); got != tt.want {
			t.Fatalf("Retry-After %s: %s != %s", tt.value, got, tt.want)
		}
	}
	for _, header := range []string{"10", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat), "99999999999999999999999999"} {
		calls := 0
		c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
			calls++
			resp := gatewayResponse(429, `{"Code":"Throttling"}`)
			resp.Header.Set("Retry-After", header)
			return resp, nil
		}, WithGatewayOptions(GatewayOptions{Endpoint: "dms.cn-shanghai.aliyuncs.com", RequestTimeout: 50 * time.Millisecond}))
		start := time.Now()
		result, err := c.ListAgenticCatalog(context.Background(), GatewayPageOptions{})
		gatewayAssertError(t, result, err, gatewayLimited)
		if calls != 1 || time.Since(start) > time.Second {
			t.Fatal("Retry-After exceeded total budget or was ignored")
		}
	}
	// A feasible server delay is actually honored, not merely parsed.
	var first time.Time
	calls := 0
	c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			first = time.Now()
			resp := gatewayResponse(429, `{}`)
			resp.Header.Set("Retry-After", "1")
			return resp, nil
		}
		if time.Since(first) < time.Second {
			t.Fatal("retried before Retry-After")
		}
		return gatewayResponse(200, `{"Success":true,"Data":[]}`), nil
	})
	if _, err := c.ListAgenticCatalog(context.Background(), GatewayPageOptions{}); err != nil || calls != 2 {
		t.Fatalf("calls %d, err %v", calls, err)
	}
}

func TestGatewayCancellationAndTotalHTTPBudget(t *testing.T) {
	for _, mode := range []string{"request", "body", "retry"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			var firstDeadline time.Time
			c := gatewayTestClient(func(req *http.Request) (*http.Response, error) {
				n := calls.Add(1)
				deadline, ok := req.Context().Deadline()
				if !ok {
					t.Fatal("HTTP request has no budget")
				}
				if n == 1 {
					firstDeadline = deadline
				} else if !deadline.Equal(firstDeadline) {
					t.Fatal("retry got a fresh budget")
				}
				if mode == "retry" && n == 1 {
					resp := gatewayResponse(429, `{}`)
					resp.Header.Set("Retry-After", "0")
					return resp, nil
				}
				if mode == "body" {
					resp := gatewayResponse(200, "")
					resp.Body = io.NopCloser(gatewayContextReader{ctx: req.Context()})
					return resp, nil
				}
				<-req.Context().Done()
				return nil, req.Context().Err()
			}, WithGatewayOptions(GatewayOptions{Endpoint: "dms.cn-shanghai.aliyuncs.com", RequestTimeout: 40 * time.Millisecond}))
			start := time.Now()
			result, err := c.ListAgenticCatalog(context.Background(), GatewayPageOptions{})
			failure := gatewayAssertError(t, result, err, gatewayUnavailable)
			want := int32(1)
			if mode == "retry" {
				want = 2
			}
			if calls.Load() != want || failure.Retryable || time.Since(start) > time.Second {
				t.Fatal("HTTP budget or cancellation violated")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	c := gatewayTestClient(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	done := make(chan error, 1)
	go func() { _, err := c.ListAgenticCatalog(ctx, GatewayPageOptions{}); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		gatewayAssertError(t, nil, err, gatewayUnavailable)
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not reach HTTP")
	}
	c.strictCredFn = func() (*Credential, error) { t.Fatal("already canceled call resolved credentials"); return nil, nil }
	result, err := c.ListAgenticCatalog(ctx, GatewayPageOptions{})
	gatewayAssertError(t, result, err, gatewayUnavailable)
}

type gatewayContextReader struct{ ctx context.Context }

func (r gatewayContextReader) Read([]byte) (int, error) { <-r.ctx.Done(); return 0, r.ctx.Err() }

func TestGatewayCancelDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	c := gatewayTestClient(func(*http.Request) (*http.Response, error) {
		calls++
		resp := gatewayResponse(429, `{}`)
		resp.Header.Set("Retry-After", "1")
		time.AfterFunc(20*time.Millisecond, cancel)
		return resp, nil
	})
	start := time.Now()
	result, err := c.ListAgenticCatalog(ctx, GatewayPageOptions{})
	gatewayAssertError(t, result, err, gatewayUnavailable)
	if calls != 1 || time.Since(start) > 500*time.Millisecond {
		t.Fatal("backoff ignored cancellation")
	}
}

func TestGatewayConcurrentStrictSnapshots(t *testing.T) {
	var generation atomic.Int32
	var calls atomic.Int32
	c := gatewayTestClient(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		token := req.Header.Get("x-acs-security-token")
		cred := &Credential{AccessKeyID: "id-" + token, AccessKeySecret: "secret-" + token, SecurityToken: token}
		gatewayAssertSignature(t, req, cred)
		return gatewayResponse(200, `{"Success":true,"Data":[]}`), nil
	}, WithStrictCredentialProvider(func() (*Credential, error) {
		token := fmt.Sprint(generation.Add(1))
		return &Credential{AccessKeyID: "id-" + token, AccessKeySecret: "secret-" + token, SecurityToken: token}, nil
	}))
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ListAgenticCatalog(context.Background(), GatewayPageOptions{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 12 {
		t.Fatal("concurrent calls lost")
	}
}
