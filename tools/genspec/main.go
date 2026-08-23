// Command genspec writes the agent's OpenAPI description by reading the agent: routes from the
// router, bodies from whatever each handler binds, responses from whatever typed value it writes.
// Nothing is annotated, so the description cannot claim something the code does not do.
//
//	go run ./tools/genspec -o internal/api/openapi.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

func main() {
	root := flag.String("root", ".", "agent module root")
	out := flag.String("o", "", "write here instead of stdout")
	flag.Parse()

	spec, err := build(*root)
	if err != nil {
		log.Fatal(err)
	}

	encoded, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	encoded = append(encoded, '\n')

	if *out == "" {
		_, _ = os.Stdout.Write(encoded)
		return
	}
	if err := os.WriteFile(*out, encoded, 0644); err != nil {
		log.Fatal(err)
	}
}

type handler struct {
	decl *ast.FuncDecl
	pkg  *packages.Package
}

func build(root string) (*openAPI, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
		Dir: root,
	}
	pkgs, err := packages.Load(cfg, "./internal/api", "./internal/auth")
	if err != nil {
		return nil, err
	}
	var api *packages.Package
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			return nil, fmt.Errorf("loading %s: %v", pkg.PkgPath, pkg.Errors[0])
		}
		if strings.HasSuffix(pkg.PkgPath, "/internal/api") {
			api = pkg
		}
	}
	if api == nil || len(api.Syntax) == 0 {
		return nil, fmt.Errorf("no packages loaded from %s", root)
	}

	handlers := indexHandlers(pkgs)

	routes, err := readRoutes(api, handlers)
	if err != nil {
		return nil, err
	}

	permissions := indexPermissions(pkgs)
	schemas := &schemaSet{byName: map[string]*schema{}, seen: map[string]bool{}}

	spec := &openAPI{
		OpenAPI: "3.1.0",
		Info: info{
			Title:       "FlatRun Agent API",
			Description: "Generated from the agent's routes and the types its handlers bind and return.",
			Version:     readVersion(root),
		},
		Paths:      map[string]map[string]*operation{},
		Components: components{Schemas: schemas.byName},
	}

	for _, r := range routes {
		op := &operation{
			OperationID: operationID(r),
			Tags:        []string{familyOf(r.Path)},
			Responses:   map[string]response{"200": {Description: "Success"}},
		}
		if r.Permission != "" {
			op.Extensions = map[string]any{"x-permission": permissions[r.Permission]}
		}
		for _, param := range pathParams(r.Path) {
			op.Parameters = append(op.Parameters, parameter{
				Name: param, In: "path", Required: true,
				Schema: &schema{Type: "string"}, Rest: strings.Contains(r.Path, "*"+param),
			})
		}

		if fn := handlers[r.Handler]; fn != nil {
			if handlerCalls(fn.decl, "planRequested") {
				if op.Extensions == nil {
					op.Extensions = map[string]any{}
				}
				op.Extensions["x-plan-supported"] = true
			}
			if bound, contentType := boundRequestType(fn.pkg, fn.decl); bound != nil {
				if ref := schemas.add(bound); ref != nil {
					op.RequestBody = &requestBody{
						Required: true,
						Content:  map[string]mediaType{contentType: {Schema: ref}},
					}
				}
			}
			if returned, status := typedResponse(fn.pkg, fn.decl); returned != nil {
				if ref := schemas.add(returned); ref != nil {
					if status != "200" {
						delete(op.Responses, "200")
					}
					op.Responses[status] = response{
						Description: "Success",
						Content:     map[string]mediaType{"application/json": {Schema: ref}},
					}
				}
			}
			for _, q := range queryParams(fn.pkg, fn.decl) {
				op.Parameters = append(op.Parameters, parameter{
					Name: q, In: "query", Schema: &schema{Type: "string"},
				})
			}
		}

		path := openAPIPath(r.Path)
		if spec.Paths[path] == nil {
			spec.Paths[path] = map[string]*operation{}
		}
		spec.Paths[path][strings.ToLower(r.Method)] = op
	}

	return spec, nil
}

func handlerCalls(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch called := call.Fun.(type) {
		case *ast.Ident:
			found = found || called.Name == name
		case *ast.SelectorExpr:
			found = found || called.Sel.Name == name
		}
		return !found
	})
	return found
}

func readVersion(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return "0.0.0"
	}
	return strings.TrimSpace(string(raw))
}

// route is one registration read out of the router.
type route struct {
	Method     string
	Path       string
	Handler    string
	Permission string
}

// Endpoints the agent's own components call, not part of the interface it offers.
var skipPrefixes = []string{"/internal", "/_internal", "/security/events/ingest", "/traffic/ingest"}

var httpMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true}

// readRoutes walks the router setup rather than matching its text, so a new group, a trailing
// comment or two handlers sharing a method name cannot silently drop an endpoint.
func readRoutes(api *packages.Package, handlers map[string]*handler) ([]route, error) {
	setup := findFunc(api, "setupRoutes")
	if setup == nil {
		return nil, fmt.Errorf("setupRoutes not found in internal/api")
	}

	var routes []route
	seen := map[string]bool{}

	// A function handed a router group registers routes under whatever prefix the caller gave
	// it, so following the group into it is the only way those endpoints are seen at all.
	var walk func(fn *ast.FuncDecl, prefixes map[string]string, depth int)
	walk = func(fn *ast.FuncDecl, prefixes map[string]string, depth int) {
		if fn == nil || depth > 4 {
			return
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.AssignStmt:
				if name, prefix, ok := groupAssignment(node, prefixes); ok {
					prefixes[name] = prefix
				}
			case *ast.CallExpr:
				if r, ok := routeCall(api, node, prefixes); ok {
					if skip(r.Path) {
						return true
					}
					key := r.Method + " " + r.Path
					if seen[key] {
						return true
					}
					seen[key] = true
					routes = append(routes, r)
					return true
				}
				if target, bound, ok := delegated(api, handlers, node, prefixes); ok {
					walk(target, bound, depth+1)
				}
			}
			return true
		})
	}
	walk(setup, map[string]string{}, 0)

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes, nil
}

// delegated resolves a call that hands a router group to another function, returning that
// function and the prefix its parameter stands for.
func delegated(api *packages.Package, handlers map[string]*handler, call *ast.CallExpr, prefixes map[string]string) (*ast.FuncDecl, map[string]string, bool) {
	index := -1
	prefix := ""
	for i, arg := range call.Args {
		ident, ok := arg.(*ast.Ident)
		if !ok {
			continue
		}
		if known, ok := prefixes[ident.Name]; ok {
			index, prefix = i, known
			break
		}
	}
	if index < 0 {
		return nil, nil, false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, nil, false
	}
	fn, ok := api.TypesInfo.Uses[sel.Sel].(*types.Func)
	if !ok {
		return nil, nil, false
	}
	signature, ok := fn.Type().(*types.Signature)
	if !ok || signature.Recv() == nil {
		return nil, nil, false
	}
	target, ok := handlers[receiverName(signature.Recv().Type())+"."+fn.Name()]
	if !ok || target.decl.Type.Params == nil {
		return nil, nil, false
	}

	// The name the group goes by inside the function it was handed to.
	position := 0
	for _, field := range target.decl.Type.Params.List {
		for _, name := range field.Names {
			if position == index {
				return target.decl, map[string]string{name.Name: prefix}, true
			}
			position++
		}
	}
	return nil, nil, false
}

// groupAssignment reads `x := parent.Group("/prefix")`, which is how every path prefix is set.
func groupAssignment(stmt *ast.AssignStmt, prefixes map[string]string) (string, string, bool) {
	if len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
		return "", "", false
	}
	name, ok := stmt.Lhs[0].(*ast.Ident)
	if !ok {
		return "", "", false
	}
	call, ok := stmt.Rhs[0].(*ast.CallExpr)
	if !ok {
		return "", "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Group" || len(call.Args) == 0 {
		return "", "", false
	}
	segment, ok := stringLiteral(call.Args[0])
	if !ok {
		return "", "", false
	}
	// The parent's own prefix, when the group hangs off another group.
	parent := ""
	if ident, ok := sel.X.(*ast.Ident); ok {
		parent = prefixes[ident.Name]
	}
	return name.Name, parent + segment, true
}

// routeCall reads `group.GET("/path", middleware..., handler)`.
func routeCall(api *packages.Package, call *ast.CallExpr, prefixes map[string]string) (route, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !httpMethods[sel.Sel.Name] || len(call.Args) < 2 {
		return route{}, false
	}
	group, ok := sel.X.(*ast.Ident)
	if !ok {
		return route{}, false
	}
	prefix, known := prefixes[group.Name]
	if !known {
		return route{}, false
	}
	path, ok := stringLiteral(call.Args[0])
	if !ok {
		return route{}, false
	}

	r := route{Method: sel.Sel.Name, Path: prefix + path}
	r.Handler = handlerKey(api, call.Args[len(call.Args)-1])
	ast.Inspect(call, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && strings.HasPrefix(ident.Name, "Perm") {
			r.Permission = ident.Name
		}
		return true
	})
	return r, true
}

// handlerKey names a handler by its receiver as well as its method, since Server and half a dozen
// managers each have a Delete.
func handlerKey(api *packages.Package, expr ast.Expr) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	fn, ok := api.TypesInfo.Uses[sel.Sel].(*types.Func)
	if !ok {
		return sel.Sel.Name
	}
	signature, ok := fn.Type().(*types.Signature)
	if !ok || signature.Recv() == nil {
		return fn.Name()
	}
	return receiverName(signature.Recv().Type()) + "." + fn.Name()
}

func receiverName(t types.Type) string {
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return ""
}

func findFunc(pkg *packages.Package, name string) *ast.FuncDecl {
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
				return fn
			}
		}
	}
	return nil
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

func skip(path string) bool {
	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(strings.TrimPrefix(path, "/api"), prefix) {
			return true
		}
	}
	// A websocket carries no JSON body or response to describe.
	return strings.HasSuffix(path, "/stream") || strings.HasSuffix(path, "/interactive")
}

func indexHandlers(pkgs []*packages.Package) map[string]*handler {
	handlers := map[string]*handler{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
					continue
				}
				recv := pkg.TypesInfo.TypeOf(fn.Recv.List[0].Type)
				handlers[receiverName(recv)+"."+fn.Name.Name] = &handler{decl: fn, pkg: pkg}
			}
		}
	}
	return handlers
}

func boundRequestType(api *packages.Package, fn *ast.FuncDecl) (types.Type, string) {
	var found types.Type
	contentType := "application/json"
	ast.Inspect(fn, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "ShouldBindJSON" && sel.Sel.Name != "BindJSON" && sel.Sel.Name != "ShouldBind") {
			return true
		}
		if sel.Sel.Name == "ShouldBind" {
			contentType = "multipart/form-data"
		}
		if len(call.Args) != 1 {
			return true
		}
		if t := api.TypesInfo.TypeOf(call.Args[0]); t != nil {
			found = t
		}
		return false
	})
	return found, contentType
}

// typedResponse is what a handler writes on success. A handler answering with gin.H describes
// nothing and is left undescribed.
func typedResponse(api *packages.Package, fn *ast.FuncDecl) (types.Type, string) {
	var found types.Type
	code := "200"
	ast.Inspect(fn, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "JSON" {
			return true
		}
		status, ok := successStatus(call.Args[0])
		if !ok {
			return true
		}
		t := api.TypesInfo.TypeOf(call.Args[1])
		if t == nil || isGinH(t) {
			return true
		}
		found, code = t, status
		return false
	})
	return found, code
}

var successStatuses = map[string]string{"StatusOK": "200", "StatusCreated": "201", "StatusAccepted": "202"}

func successStatus(expr ast.Expr) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	code, ok := successStatuses[sel.Sel.Name]
	return code, ok
}

func isGinH(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Name() == "H" && named.Obj().Pkg().Path() == "github.com/gin-gonic/gin"
}

// queryParams are the query keys a handler reads.
func queryParams(api *packages.Package, fn *ast.FuncDecl) []string {
	seen := map[string]bool{}
	var names []string
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Query" && sel.Sel.Name != "DefaultQuery" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		name, err := strconv.Unquote(lit.Value)
		if err != nil || seen[name] {
			return true
		}
		seen[name] = true
		names = append(names, name)
		return true
	})
	sort.Strings(names)
	return names
}

// A route parameter is either :name or *name, the second matching the rest of the path.
func paramName(segment string) (string, bool) {
	if strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "*") {
		return segment[1:], true
	}
	return "", false
}

func pathParams(path string) []string {
	var params []string
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if name, ok := paramName(segment); ok {
			params = append(params, name)
		}
	}
	return params
}

func openAPIPath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if name, ok := paramName(segment); ok {
			segments[i] = "{" + name + "}"
		}
	}
	return strings.Join(segments, "/")
}

func familyOf(path string) string {
	trimmed := strings.Trim(strings.TrimPrefix(path, "/api"), "/")
	if trimmed == "" {
		return "root"
	}
	return strings.Split(trimmed, "/")[0]
}

func operationID(r route) string {
	parts := []string{strings.ToLower(r.Method)}
	for _, segment := range strings.Split(strings.Trim(strings.TrimPrefix(r.Path, "/api"), "/"), "/") {
		if segment == "" {
			continue
		}
		if name, ok := paramName(segment); ok {
			parts = append(parts, "by-"+name)
			continue
		}
		parts = append(parts, segment)
	}
	return strings.Join(parts, "-")
}

// indexPermissions reads what each Perm constant is actually set to, rather than deriving it from
// the name: PermAPIKeysWrite is "apikeys:write", which no reading of the name produces.
func indexPermissions(pkgs []*packages.Package) map[string]string {
	values := map[string]string{}
	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			constant, ok := scope.Lookup(name).(*types.Const)
			if !ok || !strings.HasPrefix(name, "Perm") || constant.Val() == nil {
				continue
			}
			values[name] = strings.Trim(constant.Val().String(), `"`)
		}
	}
	return values
}
