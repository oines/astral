// Package httpcontract keeps the runtime HTTP routes and generated OpenAPI on
// the same declarations. Stream endpoints retain their own transport handlers.
package httpcontract

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	core "github.com/oines/astral-core"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/transport"
)

type Empty struct{}
type OK struct {
	OK bool `json:"ok"`
}
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}
type Contract struct {
	Paths   map[string]any
	Schemas map[string]any
}

func New() *Contract { return &Contract{Paths: map[string]any{}, Schemas: map[string]any{}} }
func (c *Contract) Document() map[string]any {
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Astral API", "version": "0.1.0", "description": "Owner, bound Agent Gateway and authenticated Daemon interfaces. Streams use separate clients."},
		"paths": c.Paths, "components": map[string]any{"schemas": c.Schemas, "securitySchemes": map[string]any{
			"desktop":    map[string]any{"type": "apiKey", "in": "header", "name": "Authorization", "description": "Desktop TOKEN; private runtime identity accepted only from loopback."},
			"webSession": map[string]any{"type": "apiKey", "in": "cookie", "name": "astral_session"},
			"daemon":     map[string]any{"type": "http", "scheme": "bearer"},
		}}}
}
func (c *Contract) Merge(other *Contract) {
	for k, v := range other.Paths {
		c.Paths[k] = v
	}
	for k, v := range other.Schemas {
		c.Schemas[k] = v
	}
}

type Operation struct {
	Method, Path, ID, Tag string
	Description           string
	Request, Response     reflect.Type
	Status                int
	Auth                  bool
	ContentType           string
	Query                 []Parameter
}
type Parameter struct {
	Name        string
	Type        string
	Required    bool
	Description string
}

var pathParameter = regexp.MustCompile(`\{([^}]+)\}`)

func (c *Contract) Describe(o Operation) {
	if o.Status == 0 {
		o.Status = http.StatusOK
	}
	if o.ContentType == "" {
		o.ContentType = "application/json"
	}
	responseSchema := map[string]any{}
	if o.Response != nil {
		responseSchema = c.schema(o.Response)
	}
	if o.ContentType != "application/json" {
		responseSchema = map[string]any{"type": "string"}
		if o.ContentType != "text/event-stream" {
			responseSchema["format"] = "binary"
		}
	}
	responses := map[string]any{
		http.StatusText(o.Status): nil,
		"default":                 map[string]any{"description": "Operation error", "content": map[string]any{"application/json": map[string]any{"schema": c.schema(reflect.TypeFor[Error]())}}},
	}
	delete(responses, http.StatusText(o.Status))
	responses[statusString(o.Status)] = map[string]any{"description": http.StatusText(o.Status), "content": map[string]any{o.ContentType: map[string]any{"schema": responseSchema}}}
	operation := map[string]any{"operationId": o.ID, "tags": []string{o.Tag}, "responses": responses}
	if o.Description != "" {
		operation["description"] = o.Description
	}
	if o.Auth {
		operation["security"] = []any{map[string]any{"desktop": []string{}}, map[string]any{"webSession": []string{}}}
	}
	parameters := []any{}
	for _, match := range pathParameter.FindAllStringSubmatch(o.Path, -1) {
		parameters = append(parameters, map[string]any{"name": match[1], "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
	}
	for _, p := range o.Query {
		kind := p.Type
		if kind == "" {
			kind = "string"
		}
		parameter := map[string]any{"name": p.Name, "in": "query", "required": p.Required, "schema": map[string]any{"type": kind}}
		if p.Description != "" {
			parameter["description"] = p.Description
		}
		parameters = append(parameters, parameter)
	}
	if len(parameters) > 0 {
		operation["parameters"] = parameters
	}
	if o.Request != nil && o.Request != reflect.TypeFor[Empty]() {
		operation["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": c.schema(o.Request)}}}
	}
	path, ok := c.Paths[o.Path].(map[string]any)
	if !ok {
		path = map[string]any{}
		c.Paths[o.Path] = path
	}
	path[strings.ToLower(o.Method)] = operation
}
func statusString(status int) string {
	return string([]byte{byte(status/100) + '0', byte(status/10%10) + '0', byte(status%10) + '0'})
}
func (c *Contract) schema(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		return c.schema(t.Elem())
	}
	if t == reflect.TypeFor[json.RawMessage]() {
		return map[string]any{}
	}
	if t == reflect.TypeFor[time.Time]() {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{"type": "array", "items": c.schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": c.schema(t.Elem())}
	case reflect.Struct:
		name := strings.ReplaceAll(strings.TrimPrefix(t.PkgPath(), "github.com/oines/"), "/", "_") + "_" + t.Name()
		name = strings.ReplaceAll(name, "-", "_")
		if t.Name() == "" {
			return c.structSchema(t)
		}
		if _, ok := c.Schemas[name]; !ok {
			c.Schemas[name] = map[string]any{}
			c.Schemas[name] = c.structSchema(t)
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	default:
		return map[string]any{}
	}
}
func (c *Contract) structSchema(t reflect.Type) map[string]any {
	properties := map[string]any{}
	required := []string{}
	var fields func(reflect.Type)
	fields = func(value reflect.Type) {
		for i := 0; i < value.NumField(); i++ {
			f := value.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			embedded := f.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if f.Anonymous && tag[0] == "" && embedded.Kind() == reflect.Struct {
				fields(embedded)
				continue
			}
			name := tag[0]
			if name == "" {
				name = f.Name
			}
			schema := c.schema(f.Type)
			if enum := f.Tag.Get("enum"); enum != "" {
				schema["enum"] = strings.Split(enum, ",")
			}
			if description := f.Tag.Get("description"); description != "" {
				schema["description"] = description
			}
			properties[name] = schema
			if !strings.Contains(f.Tag.Get("json"), "omitempty") {
				required = append(required, name)
			}
		}
	}
	fields(t)
	result := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}
func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func Decode(r *http.Request, target any) error {
	if r.Header.Get("Content-Type") != "" && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return domain.Invalid("expected application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, (1<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domain.Invalid("invalid request body: " + err.Error())
	}
	if decoder.Decode(new(any)) != io.EOF {
		return domain.Invalid("request must contain one JSON object")
	}
	return nil
}
func Failure(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusInternalServerError, "operation_failed", "The operation failed. Check Server diagnostics."
	var typed *domain.Error
	var remote *transport.Error
	switch {
	case errors.As(err, &typed):
		status, code, message = typed.Status, typed.Code, typed.Message
	case errors.As(err, &remote):
		status, code, message = http.StatusBadGateway, remote.Code, remote.Message
		if remote.Outcome == "unknown" {
			code = "outcome_unknown"
		}
	case errors.Is(err, core.ErrBusy):
		status, code, message = 409, "conflict", "This Session is busy."
	case errors.Is(err, core.ErrClosed):
		status, code, message = 503, "offline", "This Session is closed."
	case errors.Is(err, core.ErrNotFound):
		status, code, message = 404, "not_found", "The history record is unavailable."
	case errors.Is(err, core.ErrConflict):
		status, code, message = 409, "conflict", "The record changed. Reload before continuing."
	case errors.Is(err, core.ErrNotContinuable):
		status, code, message = 409, "conflict", "This Session needs a new input before it can continue."
	}
	JSON(w, status, Error{Code: code, Message: message, RequestID: r.Header.Get("X-Request-ID")})
}
func Register[Request, Response any](contract *Contract, mux *http.ServeMux, operation Operation, handler func(http.ResponseWriter, *http.Request, Request) (Response, error)) {
	operation.Request, operation.Response = reflect.TypeFor[Request](), reflect.TypeFor[Response]()
	contract.Describe(operation)
	mux.HandleFunc(operation.Method+" "+operation.Path, func(w http.ResponseWriter, r *http.Request) {
		var request Request
		if operation.Request != reflect.TypeFor[Empty]() {
			if err := Decode(r, &request); err != nil {
				Failure(w, r, err)
				return
			}
		}
		response, err := handler(w, r, request)
		if err != nil {
			Failure(w, r, err)
			return
		}
		status := operation.Status
		if status == 0 {
			status = 200
		}
		JSON(w, status, response)
	})
}
