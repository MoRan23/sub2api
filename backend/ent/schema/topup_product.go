package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"time"
)

type TopupProduct struct{ ent.Schema }

func (TopupProduct) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "topup_products"}}
}
func (TopupProduct) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").MaxLen(100).NotEmpty(),
		field.String("description").Default("").SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("instructions").Default("").SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Bool("enabled").Default(false),
		field.Int("sort_order").Default(0),
		field.Time("archived_at").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}
func (TopupProduct) Indexes() []ent.Index { return []ent.Index{index.Fields("enabled", "sort_order")} }
