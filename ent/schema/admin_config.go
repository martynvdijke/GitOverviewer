package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// AdminConfig holds application-wide admin settings (singleton row).
type AdminConfig struct {
	ent.Schema
}

func (AdminConfig) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").Default(1), // singleton: always ID 1
		field.String("otel_endpoint").Optional(),
		field.Bool("traces_enabled").Default(false),
		field.Bool("metrics_enabled").Default(false),
		field.Bool("logs_enabled").Default(false),
		field.String("log_severity").Default("warning"),
		// Gotify push notification settings for the deploy subsystem.
		// Managed from the admin panel; env vars (GOTIFY_URL/GOTIFY_TOKEN)
		// remain the fallback when unset.
		field.String("gotify_url").Optional(),
		field.String("gotify_token").Optional(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
