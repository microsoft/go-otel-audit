// Code generated by "stringer -type=Type -linecomment"; DO NOT EDIT.

package msgs

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[ATUnknown-0]
	_ = x[DataPlane-1]
	_ = x[ControlPlane-2]
	_ = x[Heartbeat-3]
	_ = x[Diagnostic-4]
}

const _Type_name = "ATUnknownAsmAuditDPAsmAuditCPAsmAuditHBAsmAuditDG"

var _Type_index = [...]uint8{0, 9, 19, 29, 39, 49}

func (i Type) String() string {
	if i >= Type(len(_Type_index)-1) {
		return "Type(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _Type_name[_Type_index[i]:_Type_index[i+1]]
}
