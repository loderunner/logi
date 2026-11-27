package logstore

import "fmt"

// Filter chooses the rows a query keeps. A nil Filter keeps every row.
//
// Only logstore implements Filter, because only logstore knows the column
// names that keys are stored under. Callers build filters from the types in
// this file.
//
// A Filter is an immutable value, so it can be shared between goroutines and
// passed to a tea.Cmd without cloning.
type Filter interface {
	// where returns a SQL condition and its bound arguments. cols maps each
	// key the store has seen to its column name. A key that isn't in cols has
	// no value in any row.
	where(cols map[string]string) (string, []any)
}

// Match keeps the rows whose value for Key is exactly Value, compared
// case-sensitively. Key is the flattened key, such as "http.method".
//
// A row without a value for Key has "", as in Row.Values, so Match{Key, ""}
// also keeps rows where Key is missing, null or an empty string. A Key that
// the store hasn't seen yet matches no row, unless Value is "". It starts
// matching once the key appears.
type Match struct {
	Key   string
	Value string
}

func (m Match) where(cols map[string]string) (string, []any) {
	name, ok := cols[m.Key]
	if !ok {
		if m.Value == "" {
			return "TRUE", nil
		}

		return "FALSE", nil
	}

	// The column name is generated, so it needs no escaping beyond the quotes.
	return fmt.Sprintf(`COALESCE("%s", '') = ?`, name), []any{m.Value}
}

// condition is a SQL boolean expression and its bound arguments. An empty SQL
// keeps every row.
type condition struct {
	sql  string
	args []any
}

// render turns f into a condition. A nil f keeps every row.
func render(f Filter, cols map[string]string) condition {
	if f == nil {
		return condition{}
	}

	sql, args := f.where(cols)

	return condition{sql: sql, args: args}
}

// whereClause joins the conditions with AND behind " WHERE ", with a leading
// space. Each condition is parenthesized, because it may contain an OR. It
// returns nothing when every condition is empty. The arguments come back in
// the order of the conditions.
func whereClause(conds ...condition) (string, []any) {
	var (
		sql  string
		args []any
	)

	for _, c := range conds {
		if c.sql == "" {
			continue
		}

		if sql == "" {
			sql = " WHERE "
		} else {
			sql += " AND "
		}

		sql += "(" + c.sql + ")"
		args = append(args, c.args...)
	}

	return sql, args
}
