package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"time"
)

// OpenAIOAuthDailySessionAffinity persists the random first slot assignment
// for an API key and logical client session. The row is updated at UTC+8 day
// boundaries while old generations remain usable by active connections.
type OpenAIOAuthDailySessionAffinity struct{ ent.Schema }

func (OpenAIOAuthDailySessionAffinity) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "openai_oauth_daily_session_affinities"}}
}
func (OpenAIOAuthDailySessionAffinity) Mixin() []ent.Mixin { return []ent.Mixin{mixins.TimeMixin{}} }
func (OpenAIOAuthDailySessionAffinity) Fields() []ent.Field {
	text := map[string]string{dialect.Postgres: "text"}
	return []ent.Field{
		field.Int64("account_id"),
		field.Int64("api_key_id").Default(0),
		field.String("logical_session_key").MaxLen(255).SchemaType(text),
		field.String("business_date").MaxLen(10),
		field.String("generation").MaxLen(64).SchemaType(text),
		field.Int("slot_index"),
		field.String("stream_session_id").MaxLen(64).SchemaType(text),
		field.Time("last_seen_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Bool("active").Default(true),
	}
}
func (OpenAIOAuthDailySessionAffinity) Indexes() []ent.Index {
	return []ent.Index{index.Fields("account_id", "api_key_id", "logical_session_key").Unique(), index.Fields("account_id", "business_date")}
}
