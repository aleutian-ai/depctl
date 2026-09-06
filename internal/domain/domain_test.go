package domain

import "testing"

func TestKnowledgeObjectValidateRejectsEmptySourceURI(t *testing.T) {
	obj := KnowledgeObject{ID: "obj_1", SourceURI: "", TrustClass: TrustRepository}
	if err := obj.Validate(); err == nil {
		t.Error("Validate() = nil, want error for empty SourceURI")
	}
}

func TestKnowledgeObjectValidateRejectsEmptyTrustClass(t *testing.T) {
	obj := KnowledgeObject{ID: "obj_1", SourceURI: "https://example.com/repo", TrustClass: ""}
	if err := obj.Validate(); err == nil {
		t.Error("Validate() = nil, want error for empty TrustClass")
	}
}

func TestKnowledgeObjectValidatePassesWithBothFieldsSet(t *testing.T) {
	obj := KnowledgeObject{ID: "obj_1", SourceURI: "https://example.com/repo", TrustClass: TrustRepository}
	if err := obj.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}
