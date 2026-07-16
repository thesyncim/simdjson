package simdjson

type typedDecShape uint8

const (
	typedDecShapeNone typedDecShape = iota
	typedDecShapeInt64String
	typedDecShapeSliceStruct
	typedDecShapeRecord
)

func compileTypedDecShape(fields []typedField) typedDecShape {
	switch len(fields) {
	case 2:
		switch {
		case fields[0].op == typedOpInt64 && fields[1].op == typedOpString:
			return typedDecShapeInt64String
		case fields[0].op == typedOpSlice && fields[1].op == typedOpStruct:
			return typedDecShapeSliceStruct
		}
	case 5:
		if fields[0].op == typedOpInt64 && fields[1].op == typedOpBool &&
			fields[2].op == typedOpString && fields[3].op == typedOpString &&
			fields[4].op == typedOpArray {
			return typedDecShapeRecord
		}
	}
	return typedDecShapeNone
}
