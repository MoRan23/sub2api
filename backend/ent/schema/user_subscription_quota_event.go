package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UserSubscriptionQuotaEvent stores one total-quota subscription event snapshot.
type UserSubscriptionQuotaEvent struct {
	ent.Schema
}

func (UserSubscriptionQuotaEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_subscription_quota_events"},
	}
}

func (UserSubscriptionQuotaEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_subscription_id"),
		field.Float("quota_total_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("quota_used_usd").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Time("starts_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("expires_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("source_kind").
			MaxLen(32),
		field.String("source_ref").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UserSubscriptionQuotaEvent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("subscription", UserSubscription.Type).
			Ref("quota_events").
			Field("user_subscription_id").
			Unique().
			Required(),
	}
}

func (UserSubscriptionQuotaEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_subscription_id"),
		index.Fields("expires_at"),
		index.Fields("user_subscription_id", "expires_at"),
		index.Fields("user_subscription_id", "expires_at", "id"),
	}
}
