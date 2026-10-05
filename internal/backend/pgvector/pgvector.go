// Package pgvector implements backend.VectorBackend on PostgreSQL with the
// pgvector extension (VEC-014). Each namespace is its own table, so
// ragctl's data never shares a table with anything else in the same
// database (e.g. a Mem0 server's own memories table).
package pgvector

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aleutian-ai/ragctl/internal/backend"
)

// ErrExtensionMissing means the vector extension isn't installed and this
// database role can't install it.
var ErrExtensionMissing = errors.New("pgvector: the vector extension isn't installed")

// ErrDimensionMismatch means a namespace's table exists with a different
// vector dimension than requested (e.g. the embedding model changed).
var ErrDimensionMismatch = errors.New("pgvector: dimension mismatch")

const defaultTopK = 10

// Adapter is a VectorBackend backed by one Postgres database.
type Adapter struct {
	pool *pgxpool.Pool
}

// pools holds one connection pool per database per process. ragctl builds
// a backend per operation; a fresh pool each time would leak connections.
var (
	poolsMu sync.Mutex
	pools   = map[string]*pgxpool.Pool{}
)

// New returns an Adapter for the database at dsn (a postgres:// URL or
// key=value string). password, when non-empty, overrides any password in
// dsn, so it can come from an environment variable rather than config.
// No connection is made until first use.
func New(dsn, password string) (*Adapter, error) {
	key := dsn + "\x00" + password
	poolsMu.Lock()
	defer poolsMu.Unlock()
	if p, ok := pools[key]; ok {
		return &Adapter{pool: p}, nil
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgvector: parse connection string: %w", err)
	}
	if password != "" {
		cfg.ConnConfig.Password = password
	}
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("pgvector: create connection pool: %w", err)
	}
	pools[key] = p
	return &Adapter{pool: p}, nil
}

// Name identifies this backend.
func (a *Adapter) Name() string { return "pgvector" }

// Capabilities reports vector search, metadata filtering, and
// delete-by-filter. No keyword or hybrid search: Postgres full-text
// search isn't wired in, and faking it would be worse than not having it.
func (a *Adapter) Capabilities(ctx context.Context) (backend.Capabilities, error) {
	return backend.Capabilities{VectorSearch: true, MetadataFilter: true, DeleteByFilter: true}, nil
}

// Health checks the database is reachable.
func (a *Adapter) Health(ctx context.Context) error {
	if err := a.pool.Ping(ctx); err != nil {
		return fmt.Errorf("pgvector: database unreachable: %w", err)
	}
	return nil
}

// EnsureNamespace creates ns's table and indexes if missing. Idempotent.
// It only tries CREATE EXTENSION when the extension is actually missing,
// since a shared database often doesn't grant that privilege.
func (a *Adapter) EnsureNamespace(ctx context.Context, ns backend.Namespace) error {
	if ns.Distance != "" && !strings.EqualFold(ns.Distance, "cosine") {
		return fmt.Errorf("pgvector: unsupported distance %q (only cosine)", ns.Distance)
	}
	if ns.Dimensions <= 0 {
		return fmt.Errorf("pgvector: namespace %q needs a positive dimension", ns.Name)
	}

	var installed bool
	if err := a.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')`).Scan(&installed); err != nil {
		return fmt.Errorf("pgvector: check extension: %w", err)
	}
	if !installed {
		if _, err := a.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
			return fmt.Errorf("%w and couldn't be created (ask a database admin to run CREATE EXTENSION vector): %v", ErrExtensionMissing, err)
		}
	}

	table := tableIdent(ns.Name)
	var existing *string
	err := a.pool.QueryRow(ctx,
		`SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = to_regclass($1) AND attname = 'embedding'`,
		table).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("pgvector: inspect table %s: %w", table, err)
	}
	want := fmt.Sprintf("vector(%d)", ns.Dimensions)
	if existing != nil {
		if *existing != want {
			return fmt.Errorf("%w: table %s has %s, want %s", ErrDimensionMismatch, table, *existing, want)
		}
		return nil
	}

	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id          text NOT NULL,
			embedding   %s NOT NULL,
			ecosystem   text NOT NULL,
			dependency  text NOT NULL,
			version     text NOT NULL,
			generation  text NOT NULL,
			source_type text NOT NULL,
			authority   integer NOT NULL,
			PRIMARY KEY (generation, id)
		)`, table, want),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (id)`, tableIdent(ns.Name+"_id"), table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (dependency, version)`, tableIdent(ns.Name+"_dep_version"), table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (generation)`, tableIdent(ns.Name+"_generation"), table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s USING hnsw (embedding vector_cosine_ops)`, tableIdent(ns.Name+"_embedding"), table),
	}
	for _, s := range stmts {
		if _, err := a.pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("pgvector: create namespace %s: %w", table, err)
		}
	}
	return nil
}

// Upsert writes or overwrites req.Points in one transaction, so a failure
// (e.g. a vector of the wrong dimension) leaves nothing half-written.
func (a *Adapter) Upsert(ctx context.Context, req backend.UpsertRequest) error {
	if len(req.Points) == 0 {
		return nil
	}
	table := tableIdent(req.Namespace)
	stmt := fmt.Sprintf(`INSERT INTO %s (id, embedding, ecosystem, dependency, version, generation, source_type, authority)
		VALUES ($1, $2::vector, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (generation, id) DO UPDATE SET embedding = EXCLUDED.embedding, ecosystem = EXCLUDED.ecosystem,
			dependency = EXCLUDED.dependency, version = EXCLUDED.version,
			source_type = EXCLUDED.source_type, authority = EXCLUDED.authority`, table)

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgvector: begin upsert: %w", err)
	}
	defer tx.Rollback(ctx)

	batch := &pgx.Batch{}
	for _, p := range req.Points {
		m := p.Metadata
		batch.Queue(stmt, p.ID, vectorLiteral(p.Vector), m.Ecosystem, m.Dependency, m.Version, m.Generation, m.SourceType, m.Authority)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("pgvector: upsert into %s: %w", table, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgvector: commit upsert: %w", err)
	}
	return nil
}

// Delete removes points matching req.IDs (in every generation) or
// req.Filter (a union). A request with no IDs and an empty filter
// deletes nothing.
func (a *Adapter) Delete(ctx context.Context, req backend.DeleteRequest) error {
	where, args := filterClause(req.Filter, 1)
	var conds []string
	if where != "" {
		conds = append(conds, "("+where+")")
	}
	if len(req.IDs) > 0 {
		args = append(args, req.IDs)
		conds = append(conds, fmt.Sprintf("id = ANY($%d)", len(args)))
	}
	if len(conds) == 0 {
		return nil
	}
	table := tableIdent(req.Namespace)
	if _, err := a.pool.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s`, table, strings.Join(conds, " OR ")), args...); err != nil {
		return fmt.Errorf("pgvector: delete from %s: %w", table, err)
	}
	return nil
}

// Query returns the TopK points nearest req.Vector by cosine distance,
// constrained by req.Filter. Score is cosine similarity (1 - distance),
// so higher is better, matching the other backends.
func (a *Adapter) Query(ctx context.Context, req backend.QueryRequest) (backend.QueryResult, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = defaultTopK
	}
	args := []any{vectorLiteral(req.Vector)}
	where, fargs := filterClause(req.Filter, 2)
	args = append(args, fargs...)
	sql := fmt.Sprintf(`SELECT id, ecosystem, dependency, version, generation, source_type, authority, embedding <=> $1::vector AS dist FROM %s`, tableIdent(req.Namespace))
	if where != "" {
		sql += " WHERE " + where
	}
	args = append(args, topK)
	sql += fmt.Sprintf(" ORDER BY dist LIMIT $%d", len(args))

	rows, err := a.pool.Query(ctx, sql, args...)
	if err != nil {
		return backend.QueryResult{}, fmt.Errorf("pgvector: query %s: %w", req.Namespace, err)
	}
	defer rows.Close()

	var res backend.QueryResult
	for rows.Next() {
		var p backend.ScoredPoint
		var dist float64
		m := &p.Metadata
		if err := rows.Scan(&p.ID, &m.Ecosystem, &m.Dependency, &m.Version, &m.Generation, &m.SourceType, &m.Authority, &dist); err != nil {
			return backend.QueryResult{}, fmt.Errorf("pgvector: read query row: %w", err)
		}
		p.Score = float32(1 - dist)
		res.Points = append(res.Points, p)
	}
	if err := rows.Err(); err != nil {
		return backend.QueryResult{}, fmt.Errorf("pgvector: query %s: %w", req.Namespace, err)
	}
	return res, nil
}

// Count reports exactly how many points in namespace match filter.
func (a *Adapter) Count(ctx context.Context, namespace string, filter *backend.Filter) (int, error) {
	where, args := filterClause(filter, 1)
	sql := fmt.Sprintf(`SELECT count(*) FROM %s`, tableIdent(namespace))
	if where != "" {
		sql += " WHERE " + where
	}
	var n int
	if err := a.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("pgvector: count %s: %w", namespace, err)
	}
	return n, nil
}

// filterClause builds an AND of f's non-empty fields, numbering
// placeholders from first. Returns "" when nothing filters.
func filterClause(f *backend.Filter, first int) (string, []any) {
	if f == nil {
		return "", nil
	}
	var conds []string
	var args []any
	add := func(col, val string) {
		if val == "" {
			return
		}
		args = append(args, val)
		conds = append(conds, fmt.Sprintf("%s = $%d", col, first+len(args)-1))
	}
	add("ecosystem", f.Ecosystem)
	add("dependency", f.Dependency)
	add("version", f.Version)
	add("generation", f.Generation)
	return strings.Join(conds, " AND "), args
}

// tableIdent quotes a namespace name as a Postgres identifier.
func tableIdent(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

// vectorLiteral renders v in pgvector's text input format, "[1,2,3]".
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
