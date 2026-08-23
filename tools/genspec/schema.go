package main

import (
	"encoding/json"
	"go/types"
	"reflect"
	"strings"
)

type openAPI struct {
	OpenAPI    string                           `json:"openapi"`
	Info       info                             `json:"info"`
	Paths      map[string]map[string]*operation `json:"paths"`
	Components components                       `json:"components"`
}

type info struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

type components struct {
	Schemas map[string]*schema `json:"schemas"`
}

type operation struct {
	OperationID string              `json:"operationId"`
	Tags        []string            `json:"tags,omitempty"`
	Parameters  []parameter         `json:"parameters,omitempty"`
	RequestBody *requestBody        `json:"requestBody,omitempty"`
	Responses   map[string]response `json:"responses"`
	Extensions  map[string]any      `json:"-"`
}

// MarshalJSON writes the extensions inline, where OpenAPI expects x- keys.
func (o operation) MarshalJSON() ([]byte, error) {
	type plain operation
	encoded, err := json.Marshal(plain(o))
	if err != nil {
		return nil, err
	}
	if len(o.Extensions) == 0 {
		return encoded, nil
	}
	var merged map[string]any
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for key, value := range o.Extensions {
		merged[key] = value
	}
	return json.Marshal(merged)
}

type parameter struct {
	Name     string  `json:"name"`
	In       string  `json:"in"`
	Required bool    `json:"required,omitempty"`
	Schema   *schema `json:"schema,omitempty"`
	Rest     bool    `json:"x-rest-of-path,omitempty"`
}

type requestBody struct {
	Required bool                 `json:"required,omitempty"`
	Content  map[string]mediaType `json:"content"`
}

type response struct {
	Description string               `json:"description"`
	Content     map[string]mediaType `json:"content,omitempty"`
}

type mediaType struct {
	Schema *schema `json:"schema,omitempty"`
}

type schema struct {
	Ref        string             `json:"$ref,omitempty"`
	Type       string             `json:"type,omitempty"`
	Format     string             `json:"format,omitempty"`
	Items      *schema            `json:"items,omitempty"`
	Properties map[string]*schema `json:"properties,omitempty"`
	// JSON objects have no order of their own, and declaration order is the order to read in.
	PropertyOrder        []string `json:"x-property-order,omitempty"`
	Columns              []string `json:"x-columns,omitempty"`
	Render               string   `json:"x-render,omitempty"`
	Required             []string `json:"required,omitempty"`
	Description          string   `json:"description,omitempty"`
	AdditionalProperties *schema  `json:"additionalProperties,omitempty"`
	Enum                 []string `json:"enum,omitempty"`
}

// schemaSet describes a type once however many endpoints use it.
type schemaSet struct {
	byName map[string]*schema
	seen   map[string]bool
}

func (s *schemaSet) add(t types.Type) *schema {
	built := s.build(t, 0)
	if built == nil {
		return nil
	}
	return built
}

func (s *schemaSet) build(t types.Type, depth int) *schema {
	if t == nil || depth > 12 {
		return nil
	}

	switch typed := t.(type) {
	case *types.Pointer:
		return s.build(typed.Elem(), depth)
	case *types.Named:
		return s.named(typed, depth)
	case *types.Alias:
		return s.build(types.Unalias(typed), depth)
	case *types.Basic:
		return basicSchema(typed)
	case *types.Slice:
		if isByteSlice(typed) {
			return &schema{Type: "string", Format: "byte"}
		}
		return &schema{Type: "array", Items: s.build(typed.Elem(), depth+1)}
	case *types.Array:
		return &schema{Type: "array", Items: s.build(typed.Elem(), depth+1)}
	case *types.Map:
		return &schema{Type: "object", AdditionalProperties: s.build(typed.Elem(), depth+1)}
	case *types.Struct:
		return s.structSchema(typed, depth)
	case *types.Interface:
		return &schema{}
	}
	return nil
}

func (s *schemaSet) named(t *types.Named, depth int) *schema {
	obj := t.Obj()
	if obj.Pkg() == nil {
		return s.build(t.Underlying(), depth)
	}

	// time.Time is a struct, but nobody wants its fields.
	if obj.Pkg().Path() == "time" && obj.Name() == "Time" {
		return &schema{Type: "string", Format: "date-time"}
	}
	if obj.Pkg().Path() == "time" && obj.Name() == "Duration" {
		return &schema{Type: "string", Description: "Duration, such as 30s or 1h0m0s"}
	}
	if obj.Pkg().Path() == "mime/multipart" && obj.Name() == "FileHeader" {
		return &schema{Type: "string", Format: "binary"}
	}

	if _, ok := t.Underlying().(*types.Struct); !ok {
		return s.build(t.Underlying(), depth)
	}

	name := instantiatedName(t)
	if !s.seen[name] {
		s.seen[name] = true
		// Registered before its fields are walked, so a type holding itself terminates.
		s.byName[name] = &schema{Type: "object"}
		built := s.structSchema(t.Underlying().(*types.Struct), depth+1)
		if built != nil {
			built.Render = renderKind(obj.Name())
			s.byName[name] = built
		}
	}
	return &schema{Ref: "#/components/schemas/" + name}
}

func (s *schemaSet) structSchema(t *types.Struct, depth int) *schema {
	out := &schema{Type: "object", Properties: map[string]*schema{}}
	s.fields(t, depth, out)
	if len(out.Properties) == 0 {
		return &schema{Type: "object"}
	}
	return out
}

func (s *schemaSet) fields(t *types.Struct, depth int, out *schema) {
	for i := 0; i < t.NumFields(); i++ {
		field := t.Field(i)
		tag := parseTag(t.Tag(i))

		if field.Embedded() && tag.name == "" {
			// An embedded struct's fields belong to the outer object.
			if embedded, ok := underlyingStruct(field.Type()); ok {
				s.fields(embedded, depth, out)
				continue
			}
		}
		if !field.Exported() || tag.skip {
			continue
		}

		name := tag.name
		if name == "" {
			name = field.Name()
		}
		built := s.build(field.Type(), depth+1)
		if built == nil {
			continue
		}
		if isScalar(built) && !tag.hidden {
			out.Columns = append(out.Columns, name)
		}
		out.Properties[name] = built
		out.PropertyOrder = append(out.PropertyOrder, name)
		if tag.required {
			out.Required = append(out.Required, name)
		}
		if len(tag.enum) > 0 {
			built.Enum = append([]string(nil), tag.enum...)
		}
	}
}

func underlyingStruct(t types.Type) (*types.Struct, bool) {
	switch typed := t.(type) {
	case *types.Pointer:
		return underlyingStruct(typed.Elem())
	case *types.Named:
		st, ok := typed.Underlying().(*types.Struct)
		return st, ok
	case *types.Struct:
		return typed, true
	}
	return nil, false
}

type fieldTag struct {
	name     string
	skip     bool
	required bool
	hidden   bool
	enum     []string
}

func parseTag(raw string) fieldTag {
	tag := fieldTag{}
	parsed := reflect.StructTag(raw)

	jsonTag := parsed.Get("json")
	if jsonTag == "-" {
		tag.skip = true
		return tag
	}
	if jsonTag != "" {
		tag.name = strings.Split(jsonTag, ",")[0]
	}
	binding := parsed.Get("binding")
	if strings.Contains(binding, "required") {
		tag.required = true
	}
	for _, rule := range strings.Split(binding, ",") {
		if values := strings.TrimPrefix(rule, "oneof="); values != rule {
			tag.enum = strings.Fields(values)
		}
	}
	if strings.TrimSpace(parsed.Get("cli")) == "-" {
		tag.hidden = true
	}
	return tag
}

func basicSchema(t *types.Basic) *schema {
	switch {
	case t.Info()&types.IsBoolean != 0:
		return &schema{Type: "boolean"}
	case t.Info()&types.IsInteger != 0:
		return &schema{Type: "integer"}
	case t.Info()&types.IsFloat != 0:
		return &schema{Type: "number"}
	case t.Info()&types.IsString != 0:
		return &schema{Type: "string"}
	}
	return &schema{}
}

func isByteSlice(t *types.Slice) bool {
	basic, ok := t.Elem().(*types.Basic)
	return ok && basic.Kind() == types.Byte
}

// isScalar reports whether a value fits in a table cell.
func isScalar(s *schema) bool {
	switch s.Type {
	case "string", "integer", "number", "boolean":
		return true
	}
	return false
}

// instantiatedName keeps a list of deployments and a list of backups as separate schemas.
func instantiatedName(t *types.Named) string {
	obj := t.Obj()
	name := schemaName(obj.Pkg().Path(), obj.Name())
	args := t.TypeArgs()
	if args == nil || args.Len() == 0 {
		return name
	}
	parts := make([]string, 0, args.Len())
	for i := 0; i < args.Len(); i++ {
		parts = append(parts, argName(args.At(i)))
	}
	return name + "Of" + strings.Join(parts, "And")
}

func argName(t types.Type) string {
	switch typed := t.(type) {
	case *types.Pointer:
		return argName(typed.Elem())
	case *types.Slice:
		return argName(typed.Elem()) + "s"
	case *types.Named:
		return typed.Obj().Name()
	case *types.Basic:
		return strings.ToUpper(typed.Name()[:1]) + typed.Name()[1:]
	}
	return "Value"
}

func renderKind(typeName string) string {
	switch typeName {
	case "List":
		return "list"
	case "Item":
		return "item"
	case "Message":
		return "message"
	}
	return ""
}

func schemaName(pkgPath, name string) string {
	parts := strings.Split(pkgPath, "/")
	return parts[len(parts)-1] + "." + name
}
