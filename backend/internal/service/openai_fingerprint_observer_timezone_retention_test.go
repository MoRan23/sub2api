package service

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
)

func TestFingerprintObserverTimezoneStringsDoNotRetainLargeBacking(t *testing.T) {
	// Every populated diagnostic string is short, but is a slice into the same
	// much larger allocation, just like unescaped values returned by gjson.
	backing := strings.Repeat("x", 1<<20) + "|complete|web_search|tools.0.user_location.timezone|Asia/Shanghai|2026-10-08|valid|converted|matched|approximate|CN|Shanghai|metadata|ingress|2026-10-08T12:00:00Z|route-key|direct|192.0.2.1|China|ready|probe|observed|"
	value := func(text string) string {
		start := strings.Index(backing, "|"+text+"|") + 1
		require.Positive(t, start)
		return backing[start : start+len(text)]
	}
	location := &RequestLocationObservation{
		Type: value("approximate"), Country: value("CN"), Region: value("Shanghai"),
		City: value("Shanghai"), Timezone: value("Asia/Shanghai"),
	}
	scan := &TimezoneScanResult{ScanStatus: value("complete"), Items: []TimezoneScanItem{{
		Source: value("web_search"), Path: value("tools.0.user_location.timezone"),
		Value: value("Asia/Shanghai"), CurrentDate: value("2026-10-08"), Current: true,
		Status: value("valid"), Reason: value("observed"), EnvironmentSource: value("metadata"), Location: location,
	}}}
	entry := FingerprintObservationEntry{
		TimezoneTarget: value("Asia/Shanghai"), TimezoneComparisonStatus: value("matched"),
		InboundTimezoneObservations: scan, OutboundTimezoneObservations: scan,
		TimezoneConversions: []TimezoneConversion{{
			Source: value("web_search"), Path: value("tools.0.user_location.timezone"),
			Original: value("Asia/Shanghai"), Output: value("Asia/Shanghai"),
			DateBefore: value("2026-10-08"), DateAfter: value("2026-10-08"),
			Status: value("converted"), Reason: value("observed"), TimeBasis: value("ingress"),
			ReceivedAt: value("2026-10-08T12:00:00Z"), EnvironmentSource: value("metadata"),
			LocationBefore: location, LocationAfter: location,
		}},
		EgressLocation: &OpenAIEgressLocationSnapshot{
			RouteKey: value("route-key"), RouteType: value("direct"), IPAddress: value("192.0.2.1"),
			Country: value("China"), CountryCode: value("CN"), Region: value("Shanghai"),
			City: value("Shanghai"), Timezone: value("Asia/Shanghai"), Status: value("ready"),
			Source: value("probe"), Reason: value("observed"),
		},
	}

	t.Run("record", func(t *testing.T) {
		observer := &fingerprintObserver{ring: make([]FingerprintObservationEntry, 1)}
		observer.setEnabled(true)
		require.Equal(t, uint64(1), observer.record(entry))
		want := entry
		want.SequenceID = 1
		require.Equal(t, want, observer.ring[0])
		assertFingerprintTimezoneDoesNotReferenceBacking(t, observer.ring[0], backing)
	})

	t.Run("populate", func(t *testing.T) {
		wasEnabled := IsFingerprintObservationEnabled()
		SetFingerprintObservationEnabled(true)
		t.Cleanup(func() { SetFingerprintObservationEnabled(wasEnabled) })
		state := &RequestTimezoneState{
			Target: *location, Inbound: scan, Conversions: entry.TimezoneConversions, EgressLocation: entry.EgressLocation,
		}
		var populated FingerprintObservationEntry
		populateFingerprintObservationTimezones(&populated, state, nil, nil)
		require.Equal(t, entry.TimezoneTarget, populated.TimezoneTarget)
		require.Equal(t, scan, populated.InboundTimezoneObservations)
		require.Equal(t, entry.TimezoneConversions, populated.TimezoneConversions)
		require.Equal(t, entry.EgressLocation, populated.EgressLocation)
		assertFingerprintTimezoneDoesNotReferenceBacking(t, populated, backing)
	})
}

func TestFingerprintObserverTimezoneCloneOwnsParsedLocationStrings(t *testing.T) {
	body := []byte(`{"padding":"` + strings.Repeat("x", 1<<20) + `","tools":[{"type":"web_search","user_location":{"type":"approximate","country":"CN","region":"Shanghai","city":"Shanghai","timezone":"Asia/Shanghai"}}]}`)
	scan := scanOpenAIRequestTimezonesWithSource(body, false)
	require.Equal(t, "complete", scan.result.ScanStatus)
	require.Len(t, scan.result.Items, 1)
	owned := cloneFingerprintTimezoneScan(&scan.result)
	require.Equal(t, &scan.result, owned)
	input, output := scan.result.Items[0], owned.Items[0]
	for _, pair := range [][2]string{
		{input.Value, output.Value},
		{input.Location.Type, output.Location.Type},
		{input.Location.Country, output.Location.Country},
		{input.Location.Region, output.Location.Region},
		{input.Location.City, output.Location.City},
		{input.Location.Timezone, output.Location.Timezone},
	} {
		require.NotEmpty(t, pair[0])
		require.False(t, unsafe.StringData(pair[0]) == unsafe.StringData(pair[1]), "retained value %q aliases parsed request storage", pair[0])
	}
}

func assertFingerprintTimezoneDoesNotReferenceBacking(t *testing.T, value any, backing string) {
	t.Helper()
	start := uintptr(unsafe.Pointer(unsafe.StringData(backing)))
	end := start + uintptr(len(backing))
	var inspect func(reflect.Value, string)
	inspect = func(value reflect.Value, path string) {
		switch value.Kind() {
		case reflect.String:
			if value.Len() != 0 {
				address := uintptr(unsafe.Pointer(unsafe.StringData(value.String())))
				require.False(t, address >= start && address < end, "%s retains oversized backing storage", path)
			}
		case reflect.Pointer, reflect.Interface:
			if !value.IsNil() {
				inspect(value.Elem(), path)
			}
		case reflect.Struct:
			for i := 0; i < value.NumField(); i++ {
				if value.Type().Field(i).IsExported() {
					inspect(value.Field(i), path+"."+value.Type().Field(i).Name)
				}
			}
		case reflect.Slice:
			for i := 0; i < value.Len(); i++ {
				inspect(value.Index(i), path+"[]")
			}
		}
	}
	inspect(reflect.ValueOf(value), "entry")
	// The pointer ranges are compared only; keeping the string alive ensures the
	// test never compares against storage that GC could already have reclaimed.
	runtime.KeepAlive(backing)
}
