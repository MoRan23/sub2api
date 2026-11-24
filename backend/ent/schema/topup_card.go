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

type TopupCard struct{ ent.Schema }

func (TopupCard) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "topup_cards"}}
}
func (TopupCard) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("sku_id"),
		field.String("code_hash").MaxLen(64).Unique().Immutable(),
		field.String("code").Optional().Nillable().Sensitive().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("ciphertext").Default("").Sensitive().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("masked_code").MaxLen(32),
		field.String("status").MaxLen(20).Default("available"),
		field.Int64("order_id").Optional().Nillable(),
		field.Time("issued_at").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}
func (TopupCard) Indexes() []ent.Index {
	return []ent.Index{index.Fields("sku_id", "status", "id"), index.Fields("order_id", "status")}
}
