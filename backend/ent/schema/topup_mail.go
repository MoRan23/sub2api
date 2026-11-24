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

type TopupMail struct{ ent.Schema }

func (TopupMail) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "topup_mails"}}
}
func (TopupMail) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("order_id").Unique(),
		field.String("recipient").MaxLen(255),
		field.String("locale").MaxLen(20).Default("zh"),
		field.String("status").MaxLen(20).Default("queued"),
		field.Int("attempts").Default(0),
		field.Time("available_at").Default(time.Now),
		field.Time("lease_until").Optional().Nillable(),
		field.String("claim_token").MaxLen(64).Default(""),
		field.String("message_id").MaxLen(255),
		field.String("last_error").MaxLen(255).Default(""),
		field.Time("sent_at").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}
func (TopupMail) Indexes() []ent.Index { return []ent.Index{index.Fields("status", "available_at")} }
