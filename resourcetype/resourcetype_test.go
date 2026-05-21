package resourcetype_test

import (
	"testing"

	commonv1 "instant.dev/proto/common/v1"

	"instant.dev/common/resourcetype"
)

func TestToProto_Roundtrip(t *testing.T) {
	cases := []struct {
		in   string
		want commonv1.ResourceType
	}{
		{resourcetype.Postgres, commonv1.ResourceType_RESOURCE_TYPE_POSTGRES},
		{resourcetype.Redis, commonv1.ResourceType_RESOURCE_TYPE_REDIS},
		{resourcetype.MongoDB, commonv1.ResourceType_RESOURCE_TYPE_MONGODB},
		{"webhook", commonv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED},
		{"", commonv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED},
	}
	for _, c := range cases {
		got := resourcetype.ToProto(c.in)
		if got != c.want {
			t.Errorf("ToProto(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFromProto(t *testing.T) {
	cases := []struct {
		in   commonv1.ResourceType
		want string
	}{
		{commonv1.ResourceType_RESOURCE_TYPE_POSTGRES, resourcetype.Postgres},
		{commonv1.ResourceType_RESOURCE_TYPE_REDIS, resourcetype.Redis},
		{commonv1.ResourceType_RESOURCE_TYPE_MONGODB, resourcetype.MongoDB},
		{commonv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED, ""},
	}
	for _, c := range cases {
		got := resourcetype.FromProto(c.in)
		if got != c.want {
			t.Errorf("FromProto(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Roundtrip: every recognized string -> proto -> string must be preserved.
func TestRoundtrip(t *testing.T) {
	for _, s := range []string{resourcetype.Postgres, resourcetype.Redis, resourcetype.MongoDB} {
		got := resourcetype.FromProto(resourcetype.ToProto(s))
		if got != s {
			t.Errorf("roundtrip %q -> %q", s, got)
		}
	}
}
