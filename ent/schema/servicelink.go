package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ServiceLink stores a manual link between a docker container and an
// external service: an Uptime Kuma monitor ID, an Nginx Proxy Manager
// proxy host, or an Authelia access-rule domain. The container+service
// pair is unique: a container can be linked to each service at most once.
type ServiceLink struct {
	ent.Schema
}

func (ServiceLink) Fields() []ent.Field {
	return []ent.Field{
		field.String("container"),
		field.Enum("service").Values("uptime_kuma", "npm", "authelia"),
		field.String("reference"),
		field.String("live_state").Optional().Default(""),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ServiceLink) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("container", "service").Unique(),
	}
}
