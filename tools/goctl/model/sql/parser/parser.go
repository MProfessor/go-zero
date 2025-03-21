package parser

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tal-tech/go-zero/core/collection"
	"github.com/tal-tech/go-zero/tools/goctl/model/sql/converter"
	"github.com/tal-tech/go-zero/tools/goctl/model/sql/model"
	"github.com/tal-tech/go-zero/tools/goctl/model/sql/util"
	su "github.com/tal-tech/go-zero/tools/goctl/util"
	"github.com/tal-tech/go-zero/tools/goctl/util/console"
	"github.com/tal-tech/go-zero/tools/goctl/util/stringx"
	"github.com/zeromicro/ddl-parser/parser"
)

const timeImport = "time.Time"

type (
	// Table describes a mysql table
	Table struct {
		Name        stringx.String
		Db          stringx.String
		PrimaryKey  Primary
		UniqueIndex map[string][]*Field
		Fields      []*Field
	}

	// Primary describes a primary key
	Primary struct {
		Field
		AutoIncrement bool
	}

	// Field describes a table field
	Field struct {
		NameOriginal    string
		Name            stringx.String
		DataType        string
		Comment         string
		SeqInIndex      int
		OrdinalPosition int
	}

	// KeyType types alias of int
	KeyType int
)

func parseNameOriginal(ts []*parser.Table) (nameOriginals [][]string) {
	var columns []string

	for _, t := range ts {
		columns = []string{}
		for _, c := range t.Columns {
			columns = append(columns, c.Name)
		}
		nameOriginals = append(nameOriginals, columns)
	}
	return
}

// Parse parses ddl into golang structure
func Parse(filename, database string) ([]*Table, error) {
	p := parser.NewParser()
	ts, err := p.From(filename)
	if err != nil {
		return nil, err
	}

	nameOriginals := parseNameOriginal(ts)

	tables := GetSafeTables(ts)
	indexNameGen := func(column ...string) string {
		return strings.Join(column, "_")
	}

	prefix := filepath.Base(filename)
	var list []*Table
	for indexTable, e := range tables {
		columns := e.Columns

		var (
			primaryColumnSet = collection.NewSet()

			primaryColumn string
			uniqueKeyMap  = make(map[string][]string)
			normalKeyMap  = make(map[string][]string)
		)

		for _, column := range columns {
			if column.Constraint != nil {
				if column.Constraint.Primary {
					primaryColumnSet.AddStr(column.Name)
				}

				if column.Constraint.Unique {
					indexName := indexNameGen(column.Name, "unique")
					uniqueKeyMap[indexName] = []string{column.Name}
				}

				if column.Constraint.Key {
					indexName := indexNameGen(column.Name, "idx")
					uniqueKeyMap[indexName] = []string{column.Name}
				}
			}
		}

		for _, e := range e.Constraints {
			if len(e.ColumnPrimaryKey) > 1 {
				return nil, fmt.Errorf("%s: unexpected join primary key", prefix)
			}

			if len(e.ColumnPrimaryKey) == 1 {
				primaryColumn = e.ColumnPrimaryKey[0]
				primaryColumnSet.AddStr(e.ColumnPrimaryKey[0])
			}

			if len(e.ColumnUniqueKey) > 0 {
				list := append([]string(nil), e.ColumnUniqueKey...)
				list = append(list, "unique")
				indexName := indexNameGen(list...)
				uniqueKeyMap[indexName] = e.ColumnUniqueKey
			}
		}

		if primaryColumnSet.Count() > 1 {
			return nil, fmt.Errorf("%s: unexpected join primary key", prefix)
		}

		primaryKey, fieldM, err := convertColumns(columns, primaryColumn)
		if err != nil {
			return nil, err
		}

		var fields []*Field
		// sort
		for indexColumn, c := range columns {
			field, ok := fieldM[c.Name]
			if ok {
				field.NameOriginal = nameOriginals[indexTable][indexColumn]
				fields = append(fields, field)
			}
		}

		var (
			uniqueIndex = make(map[string][]*Field)
			normalIndex = make(map[string][]*Field)
		)

		for indexName, each := range uniqueKeyMap {
			for _, columnName := range each {
				uniqueIndex[indexName] = append(uniqueIndex[indexName], fieldM[columnName])
			}
		}

		for indexName, each := range normalKeyMap {
			for _, columnName := range each {
				normalIndex[indexName] = append(normalIndex[indexName], fieldM[columnName])
			}
		}

		checkDuplicateUniqueIndex(uniqueIndex, e.Name)

		list = append(list, &Table{
			Name:        stringx.From(e.Name),
			Db:          stringx.From(database),
			PrimaryKey:  primaryKey,
			UniqueIndex: uniqueIndex,
			Fields:      fields,
		})
	}

	return list, nil
}

func checkDuplicateUniqueIndex(uniqueIndex map[string][]*Field, tableName string) {
	log := console.NewColorConsole()
	uniqueSet := collection.NewSet()
	for k, i := range uniqueIndex {
		var list []string
		for _, e := range i {
			list = append(list, e.Name.Source())
		}

		joinRet := strings.Join(list, ",")
		if uniqueSet.Contains(joinRet) {
			log.Warning("[checkDuplicateUniqueIndex]: table %s: duplicate unique index %s", tableName, joinRet)
			delete(uniqueIndex, k)
			continue
		}

		uniqueSet.AddStr(joinRet)
	}
}

func convertColumns(columns []*parser.Column, primaryColumn string) (Primary, map[string]*Field, error) {
	var (
		primaryKey Primary
		fieldM     = make(map[string]*Field)
		log        = console.NewColorConsole()
	)

	for _, column := range columns {
		if column == nil {
			continue
		}

		var (
			comment       string
			isDefaultNull bool
		)

		if column.Constraint != nil {
			comment = column.Constraint.Comment
			isDefaultNull = !column.Constraint.NotNull
			if !column.Constraint.NotNull && column.Constraint.HasDefaultValue {
				isDefaultNull = false
			}

			if column.Name == primaryColumn {
				isDefaultNull = false
			}
		}

		dataType, err := converter.ConvertDataType(column.DataType.Type(), isDefaultNull)
		if err != nil {
			return Primary{}, nil, err
		}

		if column.Constraint != nil {
			if column.Name == primaryColumn {
				if !column.Constraint.AutoIncrement && dataType == "int64" {
					log.Warning("[convertColumns]: The primary key %q is recommended to add constraint `AUTO_INCREMENT`", column.Name)
				}
			} else if column.Constraint.NotNull && !column.Constraint.HasDefaultValue {
				log.Warning("[convertColumns]: The column %q is recommended to add constraint `DEFAULT`", column.Name)
			}
		}

		var field Field
		field.Name = stringx.From(column.Name)
		field.DataType = dataType
		field.Comment = util.TrimNewLine(comment)

		if field.Name.Source() == primaryColumn {
			primaryKey = Primary{
				Field: field,
			}
			if column.Constraint != nil {
				primaryKey.AutoIncrement = column.Constraint.AutoIncrement
			}
		}

		fieldM[field.Name.Source()] = &field
	}
	return primaryKey, fieldM, nil
}

// ContainsTime returns true if contains golang type time.Time
func (t *Table) ContainsTime() bool {
	for _, item := range t.Fields {
		if item.DataType == timeImport {
			return true
		}
	}
	return false
}

// ConvertDataType converts mysql data type into golang data type
func ConvertDataType(table *model.Table) (*Table, error) {
	isPrimaryDefaultNull := table.PrimaryKey.ColumnDefault == nil && table.PrimaryKey.IsNullAble == "YES"
	primaryDataType, err := converter.ConvertStringDataType(table.PrimaryKey.DataType, isPrimaryDefaultNull)
	if err != nil {
		return nil, err
	}

	var reply Table
	reply.UniqueIndex = map[string][]*Field{}
	reply.Name = stringx.From(table.Table)
	reply.Db = stringx.From(table.Db)
	seqInIndex := 0
	if table.PrimaryKey.Index != nil {
		seqInIndex = table.PrimaryKey.Index.SeqInIndex
	}

	reply.PrimaryKey = Primary{
		Field: Field{
			Name:            stringx.From(table.PrimaryKey.Name),
			DataType:        primaryDataType,
			Comment:         table.PrimaryKey.Comment,
			SeqInIndex:      seqInIndex,
			OrdinalPosition: table.PrimaryKey.OrdinalPosition,
		},
		AutoIncrement: strings.Contains(table.PrimaryKey.Extra, "auto_increment"),
	}

	fieldM, err := getTableFields(table)
	if err != nil {
		return nil, err
	}

	for _, each := range fieldM {
		reply.Fields = append(reply.Fields, each)
	}
	sort.Slice(reply.Fields, func(i, j int) bool {
		return reply.Fields[i].OrdinalPosition < reply.Fields[j].OrdinalPosition
	})

	uniqueIndexSet := collection.NewSet()
	log := console.NewColorConsole()
	for indexName, each := range table.UniqueIndex {
		sort.Slice(each, func(i, j int) bool {
			if each[i].Index != nil {
				return each[i].Index.SeqInIndex < each[j].Index.SeqInIndex
			}
			return false
		})

		if len(each) == 1 {
			one := each[0]
			if one.Name == table.PrimaryKey.Name {
				log.Warning("[ConvertDataType]: table q%, duplicate unique index with primary key:  %q", table.Table, one.Name)
				continue
			}
		}

		var list []*Field
		var uniqueJoin []string
		for _, c := range each {
			list = append(list, fieldM[c.Name])
			uniqueJoin = append(uniqueJoin, c.Name)
		}

		uniqueKey := strings.Join(uniqueJoin, ",")
		if uniqueIndexSet.Contains(uniqueKey) {
			log.Warning("[ConvertDataType]: table %q, duplicate unique index %q", table.Table, uniqueKey)
			continue
		}

		uniqueIndexSet.AddStr(uniqueKey)
		reply.UniqueIndex[indexName] = list
	}

	return &reply, nil
}

func getTableFields(table *model.Table) (map[string]*Field, error) {
	fieldM := make(map[string]*Field)
	for _, each := range table.Columns {
		isDefaultNull := each.ColumnDefault == nil && each.IsNullAble == "YES"
		dt, err := converter.ConvertStringDataType(each.DataType, isDefaultNull)
		if err != nil {
			return nil, err
		}
		columnSeqInIndex := 0
		if each.Index != nil {
			columnSeqInIndex = each.Index.SeqInIndex
		}

		field := &Field{
			NameOriginal:    each.Name,
			Name:            stringx.From(each.Name),
			DataType:        dt,
			Comment:         each.Comment,
			SeqInIndex:      columnSeqInIndex,
			OrdinalPosition: each.OrdinalPosition,
		}
		fieldM[each.Name] = field
	}
	return fieldM, nil
}

// GetSafeTables escapes the golang keywords from sql tables.
func GetSafeTables(tables []*parser.Table) []*parser.Table {
	var list []*parser.Table
	for _, t := range tables {
		table := GetSafeTable(t)
		list = append(list, table)
	}

	return list
}

// GetSafeTable escapes the golang keywords from sql table.
func GetSafeTable(table *parser.Table) *parser.Table {
	table.Name = su.EscapeGolangKeyword(table.Name)
	for _, c := range table.Columns {
		c.Name = su.EscapeGolangKeyword(c.Name)
	}

	for _, e := range table.Constraints {
		var uniqueKeys, primaryKeys []string
		for _, u := range e.ColumnUniqueKey {
			uniqueKeys = append(uniqueKeys, su.EscapeGolangKeyword(u))
		}
		for _, p := range e.ColumnPrimaryKey {
			primaryKeys = append(primaryKeys, su.EscapeGolangKeyword(p))
		}
		e.ColumnUniqueKey = uniqueKeys
		e.ColumnPrimaryKey = primaryKeys
	}
	return table
}
