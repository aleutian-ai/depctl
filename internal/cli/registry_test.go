package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRegistryListBuiltinOnly(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"registry", "list"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("registry list: %v", err)
	}
	if !strings.Contains(out.String(), "grpc-go") {
		t.Errorf("output missing grpc-go seed manifest:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "6 manifests loaded") {
		t.Errorf("unexpected summary:\n%s", out.String())
	}
}

func TestRegistryListWithUserOverride(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)

	userDir, err := userRegistryDirPath()
	if err != nil {
		t.Fatalf("userRegistryDirPath: %v", err)
	}
	writeFile(t, userDir, "custom.yaml", `
apiVersion: depctl.dev/v1alpha1
kind: KnowledgePackage
metadata:
  name: my-custom-lib
match:
  ecosystems: [go]
  packages: [example.com/my-lib]
version:
  strategy: manual
sources:
  - id: repository
    type: git
    url: https://example.com/my-lib
    authority: 100
`)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"registry", "list"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("registry list: %v", err)
	}
	if !strings.Contains(out.String(), "my-custom-lib") {
		t.Errorf("output missing user-added manifest:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "7 manifests loaded") {
		t.Errorf("unexpected summary:\n%s", out.String())
	}
}
