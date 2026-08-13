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
	"regexp"
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

// route is one registration read out of the router.
type route struct {
	Method     string
	Path       string
	Handler    string
	Permission string
	Group      string
}

// groupPrefix is what each router group prepends to the paths registered on it.
var groupPrefix = map[string]string{
	"api":          "",
	"protected":    "",
	"setupGroup":   "/setup",
	"guarded":      "/setup",
	"usersGroup":   "/users",
	"apiKeysGroup": "/apikeys",
	"dnsGroup":     "/dns",
	"clusterGroup": "/cluster",
}

// Endpoints the agent's own components call, not part of the interface it offers.
var skipPrefixes = []string{"/internal", "/_internal", "/security/events/ingest", "/traffic/ingest"}

var routePattern = regexp.MustCompile(`\b(\w+)\.(GET|POST|PUT|DELETE|PATCH)\(\s*"([^"]+)"(.*)`)
var permPattern = regexp.MustCompile(`auth\.(Perm\w+)`)
var handlerPattern = regexp.MustCompile(`\.(\w+)\s*\)\s*$`)

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

	routes, err := readRoutes(api)
	if err != nil {
		return nil, err
	}

	handlers := indexHandlers(pkgs)
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
			op.Extensions = map[string]any{"x-permission": permissionValue(r.Permission)}
		}
		for _, param := range pathParams(r.Path) {
			op.Parameters = append(op.Parameters, parameter{
				Name: param, In: "path", Required: true,
				Schema: &schema{Type: "string"},
			})
		}

		if fn := handlers[r.Handler]; fn != nil {
			if bound := boundRequestType(fn.pkg, fn.decl); bound != nil {
				if ref := schemas.add(bound); ref != nil {
					op.RequestBody = &requestBody{
						Required: true,
						Content:  map[string]mediaType{"application/json": {Schema: ref}},
					}
				}
			}
			if returned := typedResponse(fn.pkg, fn.decl); returned != nil {
				if ref := schemas.add(returned); ref != nil {
					op.Responses["200"] = response{
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

func readVersion(root string) string {
	raw, err := os.ReadFile(root + "/VERSION")
	if err != nil {
		return "0.0.0"
	}
	return strings.TrimSpace(string(raw))
}

// readRoutes reads the registrations out of the source, since building the router needs a live host.
func readRoutes(api *packages.Package) ([]route, error) {
	var file string
	for _, f := range api.GoFiles {
		if strings.HasSuffix(f, "server.go") {
			file = f
		}
	}
	if file == "" {
		return nil, fmt.Errorf("server.go not found in internal/api")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	var routes []route
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		match := routePattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		group, method, path, rest := match[1], match[2], match[3], match[4]
		prefix, known := groupPrefix[group]
		if !known {
			continue
		}
		full := prefix + path
		if skip(full) {
			continue
		}
		key := method + " " + full
		if seen[key] {
			continue
		}
		seen[key] = true

		r := route{Method: method, Path: full, Group: group}
		if m := permPattern.FindStringSubmatch(rest); m != nil {
			r.Permission = m[1]
		}
		if m := handlerPattern.FindStringSubmatch(strings.TrimSpace(rest)); m != nil {
			r.Handler = m[1]
		}
		routes = append(routes, r)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes, nil
}

func skip(path string) bool {
	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	// A websocket carries no JSON body or response to describe.
	return strings.HasSuffix(path, "/stream") || strings.HasSuffix(path, "/interactive")
}

type handler struct {
	decl *ast.FuncDecl
	pkg  *packages.Package
}

func indexHandlers(pkgs []*packages.Package) map[string]*handler {
	handlers := map[string]*handler{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil {
					continue
				}
				// The API package wins a shared name: that is where a route's handler lives.
				if existing, taken := handlers[fn.Name.Name]; taken &&
					strings.HasSuffix(existing.pkg.PkgPath, "/internal/api") {
					continue
				}
				handlers[fn.Name.Name] = &handler{decl: fn, pkg: pkg}
			}
		}
	}
	return handlers
}

func boundRequestType(api *packages.Package, fn *ast.FuncDecl) types.Type {
	var found types.Type
	ast.Inspect(fn, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "ShouldBindJSON" && sel.Sel.Name != "BindJSON") {
			return true
		}
		if len(call.Args) != 1 {
			return true
		}
		unary, ok := call.Args[0].(*ast.UnaryExpr)
		if !ok {
			return true
		}
		if t := api.TypesInfo.TypeOf(unary.X); t != nil {
			found = t
		}
		return false
	})
	return found
}

// typedResponse is what a handler writes on success. A handler answering with gin.H describes
// nothing and is left undescribed.
func typedResponse(api *packages.Package, fn *ast.FuncDecl) types.Type {
	var found types.Type
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
		if !isStatusOK(call.Args[0]) {
			return true
		}
		t := api.TypesInfo.TypeOf(call.Args[1])
		if t == nil || isGinH(t) {
			return true
		}
		found = t
		return false
	})
	return found
}

func isStatusOK(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "StatusOK"
}

func isGinH(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Name() == "H" && strings.HasSuffix(named.Obj().Pkg().Path(), "gin")
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

func pathParams(path string) []string {
	var params []string
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if strings.HasPrefix(segment, ":") {
			params = append(params, strings.TrimPrefix(segment, ":"))
		}
	}
	return params
}

func openAPIPath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			segments[i] = "{" + strings.TrimPrefix(segment, ":") + "}"
		}
	}
	return "/api" + strings.Join(segments, "/")
}

func familyOf(path string) string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return "root"
	}
	return strings.Split(trimmed, "/")[0]
}

func operationID(r route) string {
	parts := []string{strings.ToLower(r.Method)}
	for _, segment := range strings.Split(strings.Trim(r.Path, "/"), "/") {
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, ":") {
			parts = append(parts, "by-"+strings.TrimPrefix(segment, ":"))
			continue
		}
		parts = append(parts, segment)
	}
	return strings.Join(parts, "-")
}

func permissionValue(constant string) string {
	// PermDeploymentsWrite reads as deployments:write.
	trimmed := strings.TrimPrefix(constant, "Perm")
	for i := 1; i < len(trimmed); i++ {
		if trimmed[i] >= 'A' && trimmed[i] <= 'Z' {
			return strings.ToLower(trimmed[:i]) + ":" + strings.ToLower(trimmed[i:])
		}
	}
	return strings.ToLower(trimmed)
}
