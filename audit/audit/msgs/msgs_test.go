package msgs

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmihailenco/msgpack/v4"
)

func TestMarshalPrep(t *testing.T) {
	t.Parallel()

	now := time.Now()

	n := func() time.Time {
		return now
	}

	tests := []struct {
		name string
		msg  Msg
		want [3]any
		err  bool
	}{
		{
			name: "Error: Type not specified",
			msg: Msg{
				now: n,
			},
			want: [3]any{},
			err:  true,
		},
		{
			name: "DataPlane",
			msg: Msg{
				now:  n,
				Type: DataPlane,
				Record: Record{
					OperationCategories: []OperationCategory{UserManagement, Authentication, UserManagement},
					CallerAccessLevels:  []string{"Level1", "Level2", "Level1"},
				},
			},
			want: [3]any{
				DataPlane.String(),
				[1][2]any{
					[2]any{
						n().Unix(), // timestamp in seconds
						Record{
							OperationCategories: []OperationCategory{UserManagement, Authentication},
							CallerAccessLevels:  []string{"Level1", "Level2"},
						},
					},
				},
				genevaTF,
			},
		},
		{
			name: "ControlPlane",
			msg: Msg{
				now:  n,
				Type: ControlPlane,
				Record: Record{
					OperationCategories: []OperationCategory{UserManagement, Authentication, UserManagement},
					CallerAccessLevels:  []string{"Level1", "Level2", "Level1"},
				},
			},
			want: [3]any{
				ControlPlane.String(),
				[1][2]any{
					[2]any{
						n().Unix(), // timestamp in seconds
						Record{
							OperationCategories: []OperationCategory{UserManagement, Authentication},
							CallerAccessLevels:  []string{"Level1", "Level2"},
						},
					},
				},
				genevaTF,
			},
		},
		{
			name: "ControlPlane, but message should not be compacted",
			msg: Msg{
				now:  n,
				Type: ControlPlane,
				Record: Record{
					OperationCategories: []OperationCategory{UserManagement, Authentication},
					CallerAccessLevels:  []string{"Level1", "Level2"},
				},
			},
			want: [3]any{
				ControlPlane.String(),
				[1][2]any{
					[2]any{
						n().Unix(), // timestamp in seconds
						Record{
							OperationCategories: []OperationCategory{UserManagement, Authentication},
							CallerAccessLevels:  []string{"Level1", "Level2"},
						},
					},
				},
				genevaTF,
			},
		},
		{
			name: "HeartBeat",
			msg: Msg{
				now:  n,
				Type: Heartbeat,
				Heartbeat: HeartbeatMsg{
					Language: "go",
				},
			},
			want: [3]any{
				Heartbeat.String(),
				[1][2]any{
					[2]any{
						n().Unix(), // timestamp in seconds
						HeartbeatMsg{
							Language: "go",
						},
					},
				},
				genevaTF,
			},
		},
	}

	for _, test := range tests {
		got, err := marshalPrep(test.msg)
		switch {
		case err == nil && test.err:
			t.Errorf("TestMarshalPrep(%s): got err == nil, want err != nil", test.name)
			continue
		case err != nil && !test.err:
			t.Errorf("TestMarshalPrep(%s): got err == %s, want err == nil", test.name, err)
			continue
		case err != nil:
			continue
		}

		assert.Equal(t, test.want, got)
	}
}

var bmp [3]any

func BenchmarkMarshalPrep(b *testing.B) {
	b.ReportAllocs()

	msg := Msg{
		Type: DataPlane,
		Record: Record{
			OperationCategories: []OperationCategory{UserManagement, Authentication, UserManagement},
			CallerAccessLevels:  []string{"Level1", "Level2", "Level1"},
		},
	}

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		v, _ := marshalPrep(msg)
		bmp = v // Prevent the compiler from optimizing out the function call.
	}
}

func TestParseAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  Addr
		err   bool
	}{
		{"Valid IPv4", "192.168.0.1", Addr{netip.MustParseAddr("192.168.0.1")}, false},
		{"Valid IPv6", "::1", Addr{netip.MustParseAddr("::1")}, false},
		{"Invalid IP", "invalid-ip", Addr{}, true},
	}

	for _, test := range tests {
		got, err := ParseAddr(test.input)
		if test.err {
			assert.Error(t, err)
			continue
		}
		assert.NoError(t, err)
		assert.Equal(t, test.want, got)
	}
}

func TestMustParseAddr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		wantPanic bool
	}{
		{"Valid IP", "127.0.0.1", false},
		{"Invalid IP", "bad-ip", true},
	}

	for _, test := range tests {
		if test.wantPanic {
			assert.Panics(t, func() { MustParseAddr(test.input) })
			continue
		}
		assert.NotPanics(t, func() {
			addr := MustParseAddr(test.input)
			assert.Equal(t, netip.MustParseAddr(test.input), addr.NetIP())
		})
	}
}

func TestAddrMarshalUnmarshalMsgpack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		addr    Addr
		wantErr bool
	}{
		{"Valid IPv4", Addr{netip.MustParseAddr("192.168.0.1")}, false},
		{"Valid IPv6", Addr{netip.MustParseAddr("::1")}, false},
	}

	for _, test := range tests {
		data, err := test.addr.MarshalMsgpack()
		require.NoError(t, err)

		var got Addr
		err = got.UnmarshalMsgpack(data)
		require.NoError(t, err)
		assert.Equal(t, test.addr, got)
	}
}

func TestAddrUnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		jsonStr string
		want    Addr
		wantErr bool
	}{
		{"Valid IPv4", `"192.168.0.1"`, Addr{netip.MustParseAddr("192.168.0.1")}, false},
		{"Valid IPv6", `"::1"`, Addr{netip.MustParseAddr("::1")}, false},
		{"Invalid IP", `"bad-ip"`, Addr{}, true},
	}

	for _, test := range tests {
		var got Addr
		err := got.UnmarshalJSON([]byte(test.jsonStr))
		if test.wantErr {
			assert.Error(t, err)
			continue
		}
		assert.NoError(t, err)
		assert.Equal(t, test.want, got)

	}
}

// genNumericArgWants generates a slice of numeric arguments and their corresponding expected values.
func genNumericArgWant[T ~uint16](length int) ([]T, []T) {
	var args, wants []T
	for i := 0; i < length; i++ {
		args = append(args, T(i))
		wants = append(wants, T(i))
	}
	return args, wants
}

func TypeMarshalUnmarshalMsgpack[T ~uint16](t *testing.T, length int) {
	args, wants := genNumericArgWant[T](length)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		want := wants[i]

		marshaller := any((arg)).(msgpack.Marshaler)
		// Marshal
		data, err := marshaller.MarshalMsgpack()
		if err != nil {
			panic(err)
		}

		// Unmarshal
		var got T
		unmarshaller := any((&got)).(msgpack.Unmarshaler)

		err = unmarshaller.UnmarshalMsgpack(data)
		if err != nil {
			panic(err)
		}

		if want != got {
			t.Errorf("%T marshal/unmarshal: got = %v, want = %v", want, got, want)
		}
	}
}

func TestMsgPackMarshalUnmarshal(t *testing.T) {
	t.Parallel()

	TypeMarshalUnmarshalMsgpack[OperationType](t, len(_OperationType_index)-1)
	TypeMarshalUnmarshalMsgpack[OperationCategory](t, len(_OperationCategory_index)-1)
	TypeMarshalUnmarshalMsgpack[OperationResult](t, len(_OperationResult_index)-1)
	TypeMarshalUnmarshalMsgpack[CallerIdentityType](t, len(_CallerIdentityType_index)-1)
}

func TypeMarshalUnmarshalJSON[T ~uint16](t *testing.T, length int) {
	args, wants := genNumericArgWant[T](length)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		want := wants[i]

		// We when do JSON (which is only for tests), we marshal to a string in msgpack and then
		// remarshal to []byte, then to JSON. So we fake that here by getting access to the string
		// representation of the value to marshal it.
		a := any(arg)
		stringer := a.(fmt.Stringer)

		data, err := json.Marshal(stringer.String())
		if err != nil {
			panic(err)
		}

		// Unmarshal
		var got T

		mt := any((&got)).(json.Unmarshaler)

		err = mt.UnmarshalJSON(data)
		if err != nil {
			panic(err)
		}

		if want != got {
			t.Errorf("%T JSON marshal/unmarshal: got = %v, want = %v", want, got, want)
		}
	}
}

func TestJSONMarshalUnmarshal(t *testing.T) {
	t.Parallel()

	TypeMarshalUnmarshalJSON[OperationType](t, len(_OperationType_index)-1)
	TypeMarshalUnmarshalJSON[OperationCategory](t, len(_OperationCategory_index)-1)
	TypeMarshalUnmarshalJSON[OperationResult](t, len(_OperationResult_index)-1)
	TypeMarshalUnmarshalJSON[CallerIdentityType](t, len(_CallerIdentityType_index)-1)
}

func TestRecordValidate(t *testing.T) {
	testCases := []struct {
		name           string
		auditRecord    Record
		expectedResult error
	}{
		{
			name:        "Missing OperationName",
			auditRecord: Record{
				// Missing operation name.
			},
			expectedResult: fmt.Errorf("operation name is required"),
		},
		{
			name: "Missing OperationCategories",
			auditRecord: Record{
				OperationName: "Operation",
				// Missing operation categories.
			},
			expectedResult: fmt.Errorf("at least one operation category is required"),
		},
		{
			name: "Missing OperationCategoryDescription",
			auditRecord: Record{
				OperationName:       "Operation",
				OperationCategories: []OperationCategory{OCOther},
			},
			expectedResult: fmt.Errorf("operation category description is required for category OCOther"),
		},
		{
			name: "Missing OperationCategoryDescription when OperationResult is Failure",
			auditRecord: Record{
				OperationName:       "Operation",
				OperationCategories: []OperationCategory{UserManagement},
				OperationResult:     Failure,
			},
			expectedResult: fmt.Errorf("operation result description is required for failed operations"),
		},
		{
			name: "Missing OperationAccessLevel",
			auditRecord: Record{
				OperationName:       "Operation",
				OperationCategories: []OperationCategory{UserManagement},
				OperationResult:     Success,
			},
			expectedResult: fmt.Errorf("operation access level is required"),
		},
		{
			name: "Missing CallerAgent",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
			},
			expectedResult: fmt.Errorf("caller agent is required"),
		},
		{
			name: "Missing CallerAgent",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
			},
			expectedResult: fmt.Errorf("at least one caller identity is required"),
		},
		{
			name: "Missing Missing CallerIdentities.CallerIdentityEntry",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: nil},
			},
			expectedResult: fmt.Errorf("at least one identity is required for identity type %v", UPN),
		},
		{
			name: "Missing CallerIdentities.CallerIdentityEntry",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: nil},
			},
			expectedResult: fmt.Errorf("at least one identity is required for identity type %v", UPN),
		},
		{
			name: "Missing CallerIdentities.CallerIdentityEntry.Identity",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: []CallerIdentityEntry{{"", "desc"}}},
			},
			expectedResult: fmt.Errorf("identity is required"),
		},
		{
			name: "Missing CallerIdentities.CallerIdentityEntry.Description",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: []CallerIdentityEntry{{"id", ""}}},
			},
			expectedResult: fmt.Errorf("description is required"),
		},
		{
			name: "CallerIPAddr is invalid",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: []CallerIdentityEntry{{"id", "desc"}}},
			},
			expectedResult: fmt.Errorf("caller IP address is required"),
		},
		{
			name: "CallerIPAddr is unspecified",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: []CallerIdentityEntry{{"id", "desc"}}},
				CallerIpAddress:      MustParseAddr("::"), // unspecified IP address.
			},
			expectedResult: fmt.Errorf("caller IP address cannot be unspecified"),
		},
		{
			name: "CallerIPAddr is loopback",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: []CallerIdentityEntry{{"id", "desc"}}},
				CallerIpAddress:      MustParseAddr("127.0.0.1"), // loopback IP address.
			},
			expectedResult: fmt.Errorf("caller IP address cannot be loopback"),
		},
		{
			name: "CallerIPAddr is multicast",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: []CallerIdentityEntry{{"id", "desc"}}},
				CallerIpAddress:      MustParseAddr("224.0.0.1"), // multicast IP address.
			},
			expectedResult: fmt.Errorf("caller IP address cannot be multicast"),
		},
		{
			name: "CallerAccessLevels is empty",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: []CallerIdentityEntry{{"id", "desc"}}},
				CallerIpAddress:      MustParseAddr("192.168.0.1"),
			},
			expectedResult: fmt.Errorf("at least one caller access level is required"),
		},
		{
			name: "CallerAccessLevels has empty entry",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: []CallerIdentityEntry{{"id", "desc"}}},
				CallerIpAddress:      MustParseAddr("192.168.0.1"),
				CallerAccessLevels:   []string{""},
			},
			expectedResult: fmt.Errorf("caller access level cannot be empty"),
		},
		{
			name: "Missing TargetResources",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: []CallerIdentityEntry{{"id", "desc"}}},
				CallerIpAddress:      MustParseAddr("192.168.0.1"),
				CallerAccessLevels:   []string{"access level"},
			},
			expectedResult: fmt.Errorf("at least one target resource is required"),
		},
		{
			name: "TargetResources.ResourceType is empty",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: []CallerIdentityEntry{{"id", "desc"}}},
				CallerIpAddress:      MustParseAddr("192.168.0.1"),
				CallerAccessLevels:   []string{"access level"},
				TargetResources:      map[string][]TargetResourceEntry{"": {{"Name", "Cluster", "DataCenter", "Region"}}},
			},
			expectedResult: fmt.Errorf("target resource type cannot be empty"),
		},
		{
			name: "Invalid TargetResources.TargetResourceEntry.Name",
			auditRecord: Record{
				OperationName:        "Operation",
				OperationCategories:  []OperationCategory{UserManagement},
				OperationResult:      Success,
				OperationAccessLevel: "some level",
				CallerAgent:          "some agent",
				CallerIdentities:     map[CallerIdentityType][]CallerIdentityEntry{UPN: []CallerIdentityEntry{{"id", "desc"}}},
				CallerIpAddress:      MustParseAddr("192.168.0.1"),
				CallerAccessLevels:   []string{"access level"},
				TargetResources:      map[string][]TargetResourceEntry{"key": {{"", "Cluster", "DataCenter", "Region"}}},
			},
			expectedResult: fmt.Errorf("target resource name is required"),
		},
		{
			name: "Valid AuditRecord",
			auditRecord: Record{
				CallerIpAddress:            MustParseAddr("192.168.0.1"),
				CallerIdentities:           map[CallerIdentityType][]CallerIdentityEntry{UPN: {{"user1@domain.com", "Description"}}},
				OperationCategories:        []OperationCategory{UserManagement},
				TargetResources:            map[string][]TargetResourceEntry{"ResourceType": {{"Name", "Cluster", "DataCenter", "Region"}}},
				CallerAccessLevels:         []string{"access level"},
				OperationAccessLevel:       "AccessLevel",
				OperationName:              "Operation",
				OperationResultDescription: "ResultDescription",
				CallerAgent:                "Agent",
			},
			expectedResult: nil,
		},
	}

	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			err := test.auditRecord.Validate()
			switch {
			case err == nil && test.expectedResult != nil:
				t.Errorf("Expected error: %v, but got no error", test.expectedResult)
			case err != nil && test.expectedResult == nil:
				t.Errorf("Expected no error, but got error: %v", err)
			case err != nil && test.expectedResult != nil && err.Error() != test.expectedResult.Error():
				t.Errorf("Expected error: %v, but got error: %v", test.expectedResult, err)
			}
		})
	}
}

func TestRecordClone(t *testing.T) {
	original := Record{
		OperationCategories: []OperationCategory{UserManagement},
		CallerIdentities: map[CallerIdentityType][]CallerIdentityEntry{
			UPN: {
				{Identity: "user1@domain.com", Description: "User 1"},
			},
		},
		TargetResources: map[string][]TargetResourceEntry{
			"ResourceType1": {
				{Name: "Resource1"},
			},
		},
		CustomData: map[string]any{
			"Key1": "Value1",
		},
	}

	cloned := original.Clone()

	// Check if the cloned AuditRecord is not the same as the original
	assert.Equal(t, original, cloned)

	// Modify the cloned AuditRecord's fields, it should not affect the original
	cloned.OperationCategories = nil
	cloned.CallerIdentities[UPN][0].Identity = "modified"
	cloned.TargetResources["ResourceType1"][0].Name = "Modified"
	cloned.CustomData["Key1"] = "Modified"

	// Check if the cloned AuditRecord's fields are deeply cloned
	assert.NotEqual(t, original.OperationCategories, cloned.OperationCategories)
	assert.NotEqual(t, original.CallerIdentities, cloned.CallerIdentities)
	assert.NotEqual(t, original.TargetResources, cloned.TargetResources)
	assert.NotEqual(t, original.CustomData, cloned.CustomData)

}

func TestCloneCallerIdentities(t *testing.T) {
	original := map[CallerIdentityType][]CallerIdentityEntry{
		UPN: {
			{Identity: "user1@domain.com", Description: "User 1"},
			{Identity: "user2@domain.com", Description: "User 2"},
		},
		PUID: {
			{Identity: "12345", Description: "User 3"},
		},
	}

	cloned := cloneCallerIdentities(original)
	// Check if the cloned map contains the same entries as the original map
	assert.Equal(t, original, cloned)

	cloned[UPN][0].Identity = "modified"
	assert.NotEqual(t, original, cloned)

	delete(cloned, PUID)
	if _, ok := original[PUID]; !ok {
		t.Errorf("original map should not be affected by deleting an entry from the cloned map")
	}
}

func TestCloneTargetResources(t *testing.T) {
	original := map[string][]TargetResourceEntry{
		"ResourceType1": {
			{Name: "Resource1"},
			{Name: "Resource2"},
		},
		"ResourceType2": {
			{Name: "Resource3"},
		},
	}

	cloned := cloneTargetResources(original)

	// Check if the cloned map contains the same entries as the original map
	assert.Equal(t, original, cloned)

	// Modify the cloned map, it should not affect the original map
	cloned["ResourceType1"][0].Name = "Modified"

	// Check if the cloned map is not the same as the original map
	assert.NotEqual(t, original, cloned)
}

func TestCloneCustomData(t *testing.T) {
	original := map[string]any{
		"Key1": "Value1",
		"Key2": 123,
	}

	cloned := cloneCustomData(original)

	// Check if the cloned map contains the same entries as the original map
	assert.Equal(t, original, cloned)

	// Modify the cloned map, it should not affect the original map
	cloned["Key1"] = "Modified"

	// Check if the cloned map is not the same as the original map
	assert.NotEqual(t, original, cloned)
}

func TestUnstring(t *testing.T) {
	tests := []struct {
		value  string
		result OperationType
	}{
		{value: "UnknownOperationType", result: UnknownOperationType},
		{value: "Read", result: Read},
		{value: "Update", result: Update},
		{value: "Create", result: Create},
		{value: "Delete", result: Delete},
		{value: "Bad", result: UnknownOperationType},
	}

	for _, test := range tests {
		assert.Equal(t, test.result, unString[uint8, OperationType](test.value, _OperationType_name, _OperationType_index[:]))
	}
}

func TestBytesToStr(t *testing.T) {
	b := []byte("Update")
	s := bytesToStr(b)
	assert.Equal(t, "Update", s)
}
