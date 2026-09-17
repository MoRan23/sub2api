package tlsfingerprint

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func completeTestProfile() *Profile {
	return &Profile{
		Name: "test", EnableGREASE: true,
		CipherSuites: []uint16{0x1301, 0x1302}, Curves: []uint16{29, 23},
		PointFormats: []uint16{0, 1}, SignatureAlgorithms: []uint16{0x0403, 0x0804},
		ALPNProtocols: []string{"http/1.1", "h2"}, SupportedVersions: []uint16{0x0304, 0x0303},
		KeyShareGroups: []uint16{29, 23}, PSKModes: []uint16{1, 0}, Extensions: []uint16{0, 10},
	}
}

func TestProfileCloneOwnsEverySlice(t *testing.T) {
	var absent *Profile
	require.Nil(t, absent.Clone())
	original := completeTestProfile()
	clone := original.Clone()
	require.Equal(t, original, clone)
	require.NotSame(t, original, clone)
	want := clone.CacheKey()
	fields := reflect.ValueOf(original).Elem()
	for i := 0; i < fields.NumField(); i++ {
		field := fields.Field(i)
		if field.Kind() == reflect.Slice {
			require.Positive(t, field.Len(), "test must populate %s", fields.Type().Field(i).Name)
			first := reflect.New(field.Type().Elem()).Elem()
			first.Set(field.Index(0))
			field.Index(0).Set(field.Index(1))
			field.Index(1).Set(first)
		}
	}
	original.Name = "edited"
	original.EnableGREASE = false
	require.Equal(t, want, clone.CacheKey(), "the transport snapshot must not change with its source")
}

func TestProfileCacheKeyIncludesEveryFieldAndSliceOrder(t *testing.T) {
	baseline := completeTestProfile()
	want := baseline.CacheKey()
	require.Len(t, want, 64)
	require.Equal(t, want, baseline.Clone().CacheKey())
	var absent *Profile
	require.NotEqual(t, absent.CacheKey(), (&Profile{}).CacheKey())
	fields := reflect.ValueOf(baseline).Elem()
	for i := 0; i < fields.NumField(); i++ {
		t.Run(fields.Type().Field(i).Name, func(t *testing.T) {
			changed := baseline.Clone()
			field := reflect.ValueOf(changed).Elem().Field(i)
			switch field.Kind() {
			case reflect.String:
				field.SetString("another")
			case reflect.Bool:
				field.SetBool(!field.Bool())
			case reflect.Slice:
				first := reflect.New(field.Type().Elem()).Elem()
				first.Set(field.Index(0))
				field.Index(0).Set(field.Index(1))
				field.Index(1).Set(first)
			default:
				t.Fatalf("add cache identity test for %s", field.Type())
			}
			require.NotEqual(t, want, changed.CacheKey())
		})
	}
}
