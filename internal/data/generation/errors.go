package generation

import "errors"

// ErrAcquisition wraps a failure materializing a KnowledgeSource's content
// (e.g. a bad Git ref). Wrapped via fmt.Errorf("%w: ...", ErrAcquisition)
// so callers can classify a Build failure with errors.Is.
var ErrAcquisition = errors.New("generation: acquisition failed")

// ErrNormalization wraps a failure turning acquired content into
// KnowledgeObjects.
var ErrNormalization = errors.New("generation: normalization failed")

// ErrReplication wraps a failure embedding or upserting a generation's
// staged chunks into a vector backend (Replicate).
var ErrReplication = errors.New("generation: replication failed")
