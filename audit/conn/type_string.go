// Code generated by "stringer -type=Type -linecomment"; DO NOT EDIT.

package conn

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[TypeUnknown-0]
	_ = x[TypeNoOP-1]
	_ = x[TypeDomainSocket-2]
	_ = x[TypeTCP-3]
}

const _Type_name = "UnknownNoOPUnixDomainSocketTCP"

var _Type_index = [...]uint8{0, 7, 11, 27, 30}

func (i Type) String() string {
	if i >= Type(len(_Type_index)-1) {
		return "Type(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _Type_name[_Type_index[i]:_Type_index[i+1]]
}
