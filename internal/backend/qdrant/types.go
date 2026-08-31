package qdrant

import "aleutian-ai/ragctl/internal/backend"

// payloadIDField stores the original ragctl point ID (e.g. a chunk ID)
// in the Qdrant payload — Qdrant's own point ID is a derived UUID
// (pointID), not directly usable as ragctl's identity, so query results
// need this to report the caller's original ID back.
const payloadIDField = "_id"

type createCollectionRequest struct {
	Vectors vectorParams `json:"vectors"`
}

type vectorParams struct {
	Size     int    `json:"size"`
	Distance string `json:"distance"`
}

type qdrantPoint struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

type upsertRequest struct {
	Points []qdrantPoint `json:"points"`
}

type pointsSelector struct {
	Points []string      `json:"points,omitempty"`
	Filter *qdrantFilter `json:"filter,omitempty"`
}

type searchRequest struct {
	Vector      []float32     `json:"vector"`
	Limit       int           `json:"limit"`
	Filter      *qdrantFilter `json:"filter,omitempty"`
	WithPayload bool          `json:"with_payload"`
}

type searchResultItem struct {
	ID      any            `json:"id"`
	Score   float32        `json:"score"`
	Payload map[string]any `json:"payload"`
}

type searchResponse struct {
	Result []searchResultItem `json:"result"`
}

type matchCondition struct {
	Key   string     `json:"key"`
	Match matchValue `json:"match"`
}

type matchValue struct {
	Value string `json:"value"`
}

type qdrantFilter struct {
	Must []matchCondition `json:"must,omitempty"`
}

// filterFrom converts a backend.Filter's non-empty fields into a Qdrant
// "must all match" filter, or nil if every field is empty.
func filterFrom(f *backend.Filter) *qdrantFilter {
	var must []matchCondition
	add := func(key, val string) {
		if val != "" {
			must = append(must, matchCondition{Key: key, Match: matchValue{Value: val}})
		}
	}
	add("ecosystem", f.Ecosystem)
	add("dependency", f.Dependency)
	add("version", f.Version)
	add("generation", f.Generation)
	if len(must) == 0 {
		return nil
	}
	return &qdrantFilter{Must: must}
}

// payloadFrom builds the Qdrant payload for a point: its mandatory
// metadata fields plus the original ragctl point ID.
func payloadFrom(m backend.PointMetadata, originalID string) map[string]any {
	return map[string]any{
		payloadIDField: originalID,
		"ecosystem":    m.Ecosystem,
		"dependency":   m.Dependency,
		"version":      m.Version,
		"generation":   m.Generation,
		"source_type":  m.SourceType,
		"authority":    m.Authority,
	}
}

// originalID recovers the ragctl point ID payloadFrom stashed.
func originalID(payload map[string]any) string {
	return strField(payload, payloadIDField)
}

// metadataFrom decodes a Qdrant search result's payload back into a
// backend.PointMetadata.
func metadataFrom(payload map[string]any) backend.PointMetadata {
	return backend.PointMetadata{
		Ecosystem:  strField(payload, "ecosystem"),
		Dependency: strField(payload, "dependency"),
		Version:    strField(payload, "version"),
		Generation: strField(payload, "generation"),
		SourceType: strField(payload, "source_type"),
		Authority:  intField(payload, "authority"),
	}
}

func strField(payload map[string]any, key string) string {
	v, _ := payload[key].(string)
	return v
}

// intField reads an int field that arrived as JSON — encoding/json
// decodes numbers into float64 when the target is map[string]any.
func intField(payload map[string]any, key string) int {
	v, _ := payload[key].(float64)
	return int(v)
}
