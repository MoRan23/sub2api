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

type TopupSKU struct{ ent.Schema }

func (TopupSKU) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "topup_skus"}}
}
func (TopupSKU) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("product_id"),
		field.String("name").MaxLen(100).NotEmpty(),
		field.Float("price").SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		field.Bool("enabled").Default(false),
		field.Int("sort_order").Default(0),
		field.Time("archived_at").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}
func (TopupSKU) Indexes() []ent.Index { return []ent.Index{index.Fields("product_id", "sort_order")} }
