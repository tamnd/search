---
title: "The SQL interface"
description: "Query a .sx file with a SELECT surface: MATCH, structured predicates, ORDER BY, bind parameters, and the Go API."
weight: 60
---

The engine carries a small SQL SELECT surface so you can query an index with familiar syntax. It runs entirely in-process: no network, no SQL server, no separate driver. A `MATCH` predicate compiles to a full-text query, and the structured predicates become filters, all against the `.sx` file.

## From the CLI

`sx sql` takes a single SELECT statement.

```
sx sql products.sx "SELECT _id, title, score FROM products WHERE title MATCH 'running' ORDER BY score DESC LIMIT 10"
```

```
_id  title                        score
---  ---------------------------  ------
p1   Red running shoes            0.2877
p3   Lightweight running jacket   0.2877
```

Output formats are `table` (default), `json`, `jsonl`, and `csv`:

```
sx sql products.sx "SELECT * FROM products WHERE category = 'footwear'" --format csv
```

Bind parameters use `?` for positional and `:name` for named. Pass named binds with repeated `-v name=value`:

```
sx sql products.sx "SELECT _id FROM products WHERE price > :min" -v min=50
```

## The supported statement

The surface is a single SELECT. The shape it supports:

```sql
SELECT <columns> FROM <table>
[WHERE <predicate>]
[ORDER BY <col> [ASC|DESC] [, ...]]
[LIMIT n] [OFFSET n]
```

The table name is a label; there is one logical table per index. Constructs outside the supported subset return an unsupported-SQL error.

### Columns and pseudo-columns

`SELECT *` expands to `_id` followed by every stored field in mapping order. You can list specific columns and alias them with `AS`.

Three pseudo-columns expose hit metadata:

- `_id` (or just listing it) is the external id.
- `score`, `rank`, or `_score` is the relevance score.
- `rowid` or `_docid` is the internal doc-id.

```sql
SELECT _id, title AS name, score FROM products WHERE title MATCH 'shoes'
```

### MATCH: full-text predicates

`MATCH` is how you reach the full-text engine from SQL. The value is a query string in the same compact syntax as [full-text search](/guides/full-text-search/).

A field-level MATCH scopes bare terms to that field:

```sql
SELECT _id FROM products WHERE title MATCH 'running shoes'
```

A table-level MATCH (the target is the table name) searches every indexed text field, FTS5-style: the query is parsed once per text field and OR-combined, so a document matches when any of its text fields satisfy it.

```sql
SELECT _id FROM products WHERE products MATCH 'running'
```

A MATCH predicate contributes to the relevance score. A query with no MATCH is a pure structured filter and scores 0.

### Structured predicates

The structured operators map onto the query model:

| SQL | Becomes |
|---|---|
| `field = v` | term match (or an exact numeric range) |
| `field != v` | everything except `field = v` |
| `field < v`, `<=`, `>`, `>=` | a range |
| `field BETWEEN lo AND hi` | an inclusive range |
| `field IN (a, b, c)` | OR of term matches |
| `field LIKE 'abc%'` | a prefix (or a wildcard for richer patterns) |

`LIKE` with a single trailing `%` and no other wildcard is a prefix; otherwise `%` maps to `*` and `_` maps to `?`.

```sql
SELECT _id, price FROM products
WHERE title MATCH 'shoes'
  AND price BETWEEN 50 AND 150
  AND brand IN ('acme', 'globex')
ORDER BY price ASC
LIMIT 20
```

Predicates combine with `AND`, `OR`, and `NOT`. A scoring leaf (a MATCH) becomes a scoring clause; a structured leaf becomes a non-scoring filter, so filters narrow results without disturbing the ranking.

### Sorting and paging

`ORDER BY` sorts by a stored or doc-values field, or by the score pseudo-column. With no `ORDER BY` the result is ranked by relevance descending. `LIMIT` and `OFFSET` page the result.

```sql
SELECT _id, created FROM products
WHERE products MATCH 'jacket'
ORDER BY created DESC
LIMIT 10 OFFSET 20
```

Sorting on a field requires that field to carry doc-values, which is the default for keyword, numeric, date, boolean, and geo fields. See [facets and sorting](/guides/facets-and-sorting/) for how doc-values drive sort.

## From Go

The SQL surface is also a library. Wrap an open `search.DB` with `sqlengine.Open` and query it through an interface modeled on `database/sql`.

```go
import (
	"context"

	"github.com/tamnd/search/sqlengine"
)

sdb, err := sqlengine.Open(db) // does not take ownership; Close leaves db open
if err != nil {
	log.Fatal(err)
}
defer sdb.Close()

rows, err := sdb.Query(context.Background(),
	"SELECT _id, title, score FROM products WHERE title MATCH ? ORDER BY score DESC LIMIT 10",
	"running")
if err != nil {
	log.Fatal(err)
}
defer rows.Close()

for rows.Next() {
	row := rows.Row()                 // map[string]any keyed by output column
	log.Printf("%v  %v", row["_id"], row["score"])
}
```

`rows.Columns()` returns the output column names in projection order. `rows.Row()` returns the current row as a map; `rows.Scan(&a, &b, ...)` copies columns into `*any` destinations in order. `QueryRow` runs a SELECT expected to return at most one row.

Bind parameters work the same as the CLI: `?` positional, `:name` with `sqlengine.Named("name", value)`.

```go
rows, err := sdb.Query(ctx,
	"SELECT _id FROM products WHERE price > :min",
	sqlengine.Named("min", 50.0))
```
