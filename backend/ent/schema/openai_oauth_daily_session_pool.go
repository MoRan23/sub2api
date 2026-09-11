package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
)

// OpenAIOAuthDailySessionPool contains the three daily streaming roots and
// one independent synchronous root for a real OAuth credential owner.
type OpenAIOAuthDailySessionPool struct{ ent.Schema }

func (OpenAIOAuthDailySessionPool) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "openai_oauth_daily_session_pools"}}
}
func (OpenAIOAuthDailySessionPool) Mixin() []ent.Mixin { return []ent.Mixin{mixins.TimeMixin{}} }
func (OpenAIOAuthDailySessionPool) Fields() []ent.Field {
	text := map[string]string{dialect.Postgres: "text"}
	return []ent.Field{
		field.Int64("account_id"),
		field.String("business_date").MaxLen(10),
		field.String("generation").MaxLen(64).SchemaType(text),
		field.String("stream_session_0").MaxLen(64).SchemaType(text),
		field.String("stream_session_1").MaxLen(64).SchemaType(text),
		field.String("stream_session_2").MaxLen(64).SchemaType(text),
		field.String("sync_session").MaxLen(64).SchemaType(text),
		field.Int("active_streams").Default(0),
	}
}
func (OpenAIOAuthDailySessionPool) Indexes() []ent.Index {
	return []ent.Index{index.Fields("account_id", "business_date").Unique()}
}
