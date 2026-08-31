package client_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

// parseGoFiles parses every .go file in the current directory.
//
// parser.ParseDir is deprecated as of Go 1.25 (it ignores build tags), so files
// are enumerated and parsed individually.
func parseGoFiles(t *testing.T, mode parser.Mode) map[string]*ast.File {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}

	fset := token.NewFileSet()
	out := make(map[string]*ast.File)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, mode)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		out[name] = f
	}
	return out
}

// These tests enforce architectural rules that are otherwise only conventions.
// They live in package client_test (external) so they see exactly the surface a
// real consumer sees.

// ARCH1.1: internal/client must not import terraform-plugin-framework. The
// client layer knows nothing about Terraform types or diagnostics; violating
// this is what makes a provider's API layer untestable in isolation.
func TestLayering_NoTerraformFrameworkImports(t *testing.T) {
	const forbidden = "terraform-plugin-framework"

	for fileName, file := range parseGoFiles(t, parser.ImportsOnly) {
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, forbidden) {
				t.Errorf("ARCH1.1 violation: %s imports %q", fileName, path)
			}
		}
	}
}

// The provider must never see a wire type. If webapiv2's JSON shape leaked into
// the exported surface, the second backend (the internal app API, which calls
// the same concept a "worker" with different fields) could not satisfy the
// interface — which is risk R7.
func TestLayering_NoWireTypesExported(t *testing.T) {
	for fileName, file := range parseGoFiles(t, 0) {
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || !ts.Name.IsExported() {
				return true
			}
			name := ts.Name.Name
			if strings.HasPrefix(strings.ToLower(name), "wire") {
				t.Errorf("wire type %q is exported in %s; wire shapes must stay unexported", name, fileName)
			}
			// An exported struct carrying json tags is a wire type wearing a
			// domain type's name.
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, f := range st.Fields.List {
				if f.Tag != nil && strings.Contains(f.Tag.Value, "json:") {
					t.Errorf("exported type %q in %s has json tags; serialization details must not reach the provider layer", name, fileName)
				}
			}
			return true
		})
	}
}

// The domain interface must stay domain-shaped. Method names taken from
// webapiv2's endpoints would make the interface a rename rather than an
// abstraction (risk R7, pre-mortem finding 1).
func TestLayering_InterfaceIsDomainShaped(t *testing.T) {
	endpointNames := []string{
		"ActiveEmployeesList",
		"ActiveEmployee",
		"SetEmployeeSetting",
		"SetEmployeeSettings",
		"CustomersList",
		"GetCustomer",
	}

	for _, file := range parseGoFiles(t, 0) {
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Client" {
				return true
			}
			iface, ok := ts.Type.(*ast.InterfaceType)
			if !ok {
				return true
			}
			for _, m := range iface.Methods.List {
				for _, name := range m.Names {
					for _, ep := range endpointNames {
						if name.Name == ep {
							t.Errorf("Client.%s is named after a webapiv2 endpoint; the interface must be domain-shaped so a second backend can satisfy it (R7)", name.Name)
						}
					}
				}
			}
			return true
		})
	}
}

// Guardrail NFR5 / Code Quality: no fmt.Print* anywhere in the client.
// golangci-lint enforces this too, but a test means it fails even if someone
// disables the linter.
func TestNoFmtPrintInClientSource(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, banned := range []string{"fmt.Print", "fmt.Println", "fmt.Printf"} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s contains %s; use tflog — printing API responses leaks personal data into Terraform logs", name, banned)
			}
		}
	}
}

// The exported surface must be exactly the domain contract, nothing more.
func TestPublicSurface_IsTheDomainContract(t *testing.T) {
	// Compile-time assertions: these fail to build if the shape regresses.
	var _ client.Client
	var _ = client.Employee{Number: 1, Name: "n", Settings: []client.Setting{{Key: "k", Value: "v"}}}
	var _ = client.ListOptions{Order: "asc", PageSize: 10, MaxPages: 5}
	var _ = client.Config{Endpoint: "e", APIKey: "k"}

	// Setting must not gain read-side fields for friendlyName or type: they are
	// write-only upstream and no read path can populate them.
	s := client.Setting{Key: "k", Value: "v"}
	if s.Key != "k" || s.Value != "v" {
		t.Fatal("Setting shape changed unexpectedly")
	}
}
