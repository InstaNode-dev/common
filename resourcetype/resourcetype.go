package resourcetype

import commonv1 "instant.dev/proto/common/v1"

const (
	Postgres = "postgres"
	Redis    = "redis"
	MongoDB  = "mongodb"
	Monitor  = "monitor"
)

// ToProto converts a string resource type to the proto enum value.
func ToProto(rt string) commonv1.ResourceType {
	switch rt {
	case Postgres:
		return commonv1.ResourceType_RESOURCE_TYPE_POSTGRES
	case Redis:
		return commonv1.ResourceType_RESOURCE_TYPE_REDIS
	case MongoDB:
		return commonv1.ResourceType_RESOURCE_TYPE_MONGODB
	default:
		return commonv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED
	}
}

// FromProto converts a proto enum value to the string resource type.
func FromProto(rt commonv1.ResourceType) string {
	switch rt {
	case commonv1.ResourceType_RESOURCE_TYPE_POSTGRES:
		return Postgres
	case commonv1.ResourceType_RESOURCE_TYPE_REDIS:
		return Redis
	case commonv1.ResourceType_RESOURCE_TYPE_MONGODB:
		return MongoDB
	default:
		return ""
	}
}
