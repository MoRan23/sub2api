package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// OpenAIOAuthSyncSession stores the account-owned root session used by
// synchronous OpenAI OAuth requests. It is deliberately separate from
// accounts.extra so scheduler/account cache updates cannot overwrite it.
type OpenAIOAuthSyncSession struct {
	ent.Schema
}

func (OpenAIOAuthSyncSession) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "openai_oauth_sync_sessions"}}
}

func (OpenAIOAuthSyncSession) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (OpenAIOAuthSyncSession) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("account_id").Unique(),
		field.String("session_id").
			MaxLen(64).
			NotEmpty().
			Unique().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
	}
}
