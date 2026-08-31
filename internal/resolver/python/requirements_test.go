package python

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRequirementsExactPin(t *testing.T) {
	deps, warnings := parseRequirements([]byte("pydantic==2.11.7\n"))
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if len(deps) != 1 || deps[0].Dependency.Name != "pydantic" || deps[0].Version != "2.11.7" {
		t.Errorf("deps = %+v, want pydantic==2.11.7", deps)
	}
	if !deps[0].Dependency.Direct {
		t.Error("Direct = false, want true (requirements.txt entries are always direct)")
	}
}

func TestParseRequirementsRangeIsUnresolved(t *testing.T) {
	deps, warnings := parseRequirements([]byte("pydantic>=2.10\n"))
	if len(deps) != 0 {
		t.Errorf("deps = %+v, want none (range never silently resolved)", deps)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "pydantic") {
		t.Errorf("warnings = %v, want one naming pydantic", warnings)
	}
}

func TestParseRequirementsBareNameIsUnresolved(t *testing.T) {
	deps, warnings := parseRequirements([]byte("pydantic\n"))
	if len(deps) != 0 {
		t.Errorf("deps = %+v, want none", deps)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want one", warnings)
	}
}

func TestParseRequirementsCommentsAndBlankLinesIgnored(t *testing.T) {
	deps, warnings := parseRequirements([]byte("# a comment\n\npydantic==2.11.7\n\n  \n"))
	if len(deps) != 1 {
		t.Fatalf("deps = %+v, want 1", deps)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

func TestParseRequirementsIncludeDirectiveSkippedWithWarning(t *testing.T) {
	deps, warnings := parseRequirements([]byte("-r base.txt\npydantic==2.11.7\n"))
	if len(deps) != 1 {
		t.Fatalf("deps = %+v, want 1 (include line skipped, not recursed)", deps)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "base.txt") {
		t.Errorf("warnings = %v, want one naming base.txt", warnings)
	}
}

func TestResolveRequirementsTxtEndToEnd(t *testing.T) {
	root := t.TempDir()
	content := "pydantic==2.11.7\nrequests>=2.30\n"
	if err := os.WriteFile(filepath.Join(root, "requirements.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("write requirements.txt: %v", err)
	}

	res, err := resolveRequirementsTxt(root)
	if err != nil {
		t.Fatalf("resolveRequirementsTxt: %v", err)
	}
	if len(res.Dependencies) != 1 || res.Dependencies[0].Dependency.Name != "pydantic" {
		t.Fatalf("Dependencies = %+v, want just pydantic", res.Dependencies)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want 1 (requests range)", res.Warnings)
	}
}
