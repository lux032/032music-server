package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// schemaTokens 只规范化词间空白、关键字大小写和标识符引号。
// 字符串字面量逐字保留，注释/无法识别的引用保持拒绝，避免抹掉 CHECK 或表达式语义。
func schemaTokens(query string, identifiers map[string]bool) (string, error) {
	var tokens []string
	for i := 0; i < len(query); {
		c := query[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' {
			i++
			continue
		}
		if c == '\'' {
			start := i
			i++
			closed := false
			for i < len(query) {
				if query[i] == '\'' {
					i++
					if i < len(query) && query[i] == '\'' {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return "", fmt.Errorf("unterminated literal")
			}
			tokens = append(tokens, query[start:i])
			continue
		}
		if c == '"' || c == '`' || c == '[' {
			start := i
			end := c
			if c == '[' {
				end = ']'
			}
			i++
			var word strings.Builder
			closed := false
			for i < len(query) {
				if query[i] == end {
					i++
					if end != ']' && i < len(query) && query[i] == end {
						word.WriteByte(end)
						i++
						continue
					}
					closed = true
					break
				}
				word.WriteByte(query[i])
				i++
			}
			w := word.String()
			if !closed || w == "" {
				return "", fmt.Errorf("invalid quoted identifier")
			}
			folded := asciiLower(w)
			if identifiers[folded] && !sqliteKeywords[folded] {
				tokens = append(tokens, "id:"+folded)
			} else {
				tokens = append(tokens, "qid:"+query[start:i])
			}
			continue
		}
		if c >= '0' && c <= '9' {
			start := i
			i++
			for i < len(query) && ((query[i] >= '0' && query[i] <= '9') || query[i] == '.') {
				i++
			}
			tokens = append(tokens, "number:"+query[start:i])
			continue
		}
		if sqliteIdentifierByte(c) {
			start := i
			i++
			for i < len(query) && sqliteIdentifierByte(query[i]) {
				i++
			}
			tokens = append(tokens, "id:"+asciiLower(query[start:i]))
			continue
		}
		if i+1 < len(query) && (query[i:i+2] == "/*" || query[i:i+2] == "--") {
			return "", fmt.Errorf("schema 注释需严格匹配")
		}
		tokens = append(tokens, string(c))
		i++
	}
	return strings.Join(tokens, "\x00"), nil
}
func pragmaSignature(ctx context.Context, db *sql.DB, query string) (string, error) {
	rows, e := db.QueryContext(ctx, query)
	if e != nil {
		return "", e
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		return "", e
	}
	all := [][]any{}
	for rows.Next() {
		values := make([]any, len(cols))
		args := make([]any, len(cols))
		for i := range values {
			args[i] = &values[i]
		}
		if e = rows.Scan(args...); e != nil {
			return "", e
		}
		all = append(all, values)
	}
	raw, e := json.Marshal(all)
	if e != nil {
		return "", e
	}
	return string(raw), rows.Err()
}
func schemaObjects(ctx context.Context, db *sql.DB, reference map[string]string, identifiers map[string]map[string]bool) (map[string]string, error) {
	rows, e := db.QueryContext(ctx, `SELECT type,name,tbl_name,sql FROM sqlite_schema`)
	if e != nil {
		return nil, e
	}
	type object struct {
		typ, name, table string
		query            sql.NullString
	}
	objects := []object{}
	for rows.Next() {
		var o object
		if e = rows.Scan(&o.typ, &o.name, &o.table, &o.query); e != nil {
			rows.Close()
			return nil, e
		}
		objects = append(objects, o)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	tables := map[string]bool{}
	if reference == nil {
		for _, o := range objects {
			if o.typ == "table" {
				tables[o.name] = true
			}
		}
	} else {
		for key := range reference {
			parts := strings.Split(key, ":")
			if len(parts) == 3 && parts[0] == "table" {
				tables[parts[1]] = true
			}
		}
	}
	for _, o := range objects {
		if o.name == "backup_restore_receipt" && o.typ == "table" && o.table == o.name && o.query.String == `CREATE TABLE backup_restore_receipt (sha256 TEXT PRIMARY KEY) STRICT` {
			tables[o.name] = true
		}
	}
	out := map[string]string{}
	for _, o := range objects {
		typ, name, table, query := o.typ, o.name, o.table, o.query
		lower := asciiLower(name)
		if strings.HasPrefix(lower, "sqlite_") || strings.HasPrefix(asciiLower(table), "sqlite_") {
			valid := false
			if strings.HasPrefix(name, "sqlite_autoindex_") {
				valid = typ == "index" && !query.Valid && tables[table]
			}
			if name == "sqlite_sequence" {
				valid = typ == "table" && table == name && query.String == "CREATE TABLE sqlite_sequence(name,seq)"
			}
			standard := map[string]string{"sqlite_stat1": "CREATE TABLE sqlite_stat1(tbl,idx,stat)", "sqlite_stat2": "CREATE TABLE sqlite_stat2(tbl,idx,sampleno,sample)", "sqlite_stat3": "CREATE TABLE sqlite_stat3(tbl,idx,neq,nlt,ndlt,sample)", "sqlite_stat4": "CREATE TABLE sqlite_stat4(tbl,idx,neq,nlt,ndlt,sample)"}
			if ddl, ok := standard[name]; ok {
				valid = typ == "table" && table == name && query.String == ddl
			}
			if !valid {
				return nil, fmt.Errorf("不可信 SQLite 内部对象: %s", name)
			}
			continue
		}
		if name == "backup_restore_receipt" {
			if typ != "table" || table != name || query.String != `CREATE TABLE backup_restore_receipt (sha256 TEXT PRIMARY KEY) STRICT` {
				return nil, fmt.Errorf("不可信恢复 receipt")
			}
			continue
		}
		words := strings.Fields(strings.ToUpper(schemaComments.ReplaceAllString(query.String, " ")))
		i := 1
		for i < len(words) && (words[i] == "UNIQUE" || words[i] == "TEMP" || words[i] == "TEMPORARY" || words[i] == "VIRTUAL") {
			i++
		}
		if len(words) < 2 || words[0] != "CREATE" || i >= len(words) || strings.ToLower(words[i]) != typ {
			return nil, fmt.Errorf("schema 类型与 SQL 不一致: %s", name)
		}
		signature := query.String
		virtual := false
		for _, w := range words[:i] {
			if w == "VIRTUAL" {
				virtual = true
			}
		}
		if (typ == "table" || typ == "index") && !virtual {
			// DDL token 比对补充 PRAGMA 无法表达的 CHECK、生成列表达式、排序规则、
			// STRICT/WITHOUT ROWID、部分索引 WHERE 与表达式索引内容，不放宽语义。
			normalized, normalizeErr := schemaTokens(query.String, identifiers[table])
			if normalizeErr == nil {
				signature = normalized
			} // 含注释或复杂引号时保守保留完整 SQL 严格匹配。
			pragmas := []string{}
			if typ == "table" {
				pragmas = []string{"table_xinfo", "foreign_key_list"}
			} else {
				pragmas = []string{"index_xinfo"}
			}
			for _, pragma := range pragmas {
				v, err := pragmaSignature(ctx, db, "PRAGMA "+pragma+"("+quoteSQL(name)+")")
				if err != nil {
					return nil, err
				}
				signature += "\n" + v
			}
		}
		out[typ+":"+name+":"+table] = signature
	}
	return out, nil
}

// SQLite 按字节识别高位标识符，不使用 Unicode 空白或 UTF-8 大小写折叠。
func sqliteIdentifierByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c >= 0x80
}
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// SQLite 官方关键字集合，另包含表达式字面量 TRUE/FALSE。
var sqliteKeywords = func() map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.Fields(`ABORT ACTION ADD AFTER ALL ALTER ALWAYS ANALYZE AND AS ASC ATTACH AUTOINCREMENT BEFORE BEGIN BETWEEN BY CASCADE CASE CAST CHECK COLLATE COLUMN COMMIT CONFLICT CONSTRAINT CREATE CROSS CURRENT CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP DATABASE DEFAULT DEFERRABLE DEFERRED DELETE DESC DETACH DISTINCT DO DROP EACH ELSE END ESCAPE EXCEPT EXCLUDE EXCLUSIVE EXISTS EXPLAIN FAIL FILTER FIRST FOLLOWING FOR FOREIGN FROM FULL GENERATED GLOB GROUP GROUPS HAVING IF IGNORE IMMEDIATE IN INDEX INDEXED INITIALLY INNER INSERT INSTEAD INTERSECT INTO IS ISNULL JOIN KEY LAST LEFT LIKE LIMIT MATCH MATERIALIZED NATURAL NO NOT NOTHING NOTNULL NULL NULLS OF OFFSET ON OR ORDER OTHERS OUTER OVER PARTITION PLAN PRAGMA PRECEDING PRIMARY QUERY RAISE RANGE RECURSIVE REFERENCES REGEXP REINDEX RELEASE RENAME REPLACE RESTRICT RETURNING RIGHT ROLLBACK ROW ROWS SAVEPOINT SELECT SET TABLE TEMP TEMPORARY THEN TIES TO TRANSACTION TRIGGER UNBOUNDED UNION UNIQUE UPDATE USING VACUUM VALUES VIEW VIRTUAL WHEN WHERE WINDOW WITH WITHOUT TRUE FALSE`) {
		out[asciiLower(word)] = true
	}
	return out
}()

// 只信任参考库中真实存在且属于当前对象目标表的名称，避免 DQS 字符串回退被归一化。
func referenceSchemaIdentifiers(ctx context.Context, db *sql.DB) (map[string]map[string]bool, error) {
	rows, e := db.QueryContext(ctx, `SELECT type,name,tbl_name FROM sqlite_schema WHERE type IN ('table','index')`)
	if e != nil {
		return nil, e
	}
	type object struct{ typ, name, table string }
	var objects []object
	for rows.Next() {
		var o object
		if e = rows.Scan(&o.typ, &o.name, &o.table); e != nil {
			rows.Close()
			return nil, e
		}
		objects = append(objects, o)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := map[string]map[string]bool{}
	for _, o := range objects {
		if out[o.table] == nil {
			out[o.table] = map[string]bool{}
		}
		out[o.table][asciiLower(o.name)] = true
	}
	for _, o := range objects {
		if o.typ != "table" {
			continue
		}
		rows, e := db.QueryContext(ctx, `SELECT name FROM pragma_table_xinfo(?)`, o.name)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var name string
			if e = rows.Scan(&name); e != nil {
				rows.Close()
				return nil, e
			}
			out[o.table][asciiLower(name)] = true
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
