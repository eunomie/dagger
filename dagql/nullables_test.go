package dagql

import (
	"encoding/json"
	"reflect"
	"testing"

	"gotest.tools/v3/assert"
)

func TestOptionalJSONRoundTripSetsValid(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(Opt(NewString("hello")))
	assert.NilError(t, err)

	var out Optional[String]
	err = json.Unmarshal(payload, &out)
	assert.NilError(t, err)
	assert.Assert(t, out.Valid)
	assert.Equal(t, string(out.Value), "hello")

	out = Opt(NewString("stale"))
	err = json.Unmarshal([]byte("null"), &out)
	assert.NilError(t, err)
	assert.Assert(t, !out.Valid)
	assert.Equal(t, string(out.Value), "")
}

func TestNullableJSONRoundTripSetsValid(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(NonNull(NewString("hello")))
	assert.NilError(t, err)

	var out Nullable[String]
	err = json.Unmarshal(payload, &out)
	assert.NilError(t, err)
	assert.Assert(t, out.Valid)
	assert.Equal(t, string(out.Value), "hello")

	out = NonNull(NewString("stale"))
	err = json.Unmarshal([]byte("null"), &out)
	assert.NilError(t, err)
	assert.Assert(t, !out.Valid)
	assert.Equal(t, string(out.Value), "")
}

func TestDynamicOptionalJSONRoundTripSetsValid(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(DynamicOptional{
		Elem:  Int(0),
		Value: Int(5),
		Valid: true,
	})
	assert.NilError(t, err)

	out := DynamicOptional{Elem: Int(0)}
	err = json.Unmarshal(payload, &out)
	assert.NilError(t, err)
	assert.Assert(t, out.Valid)
	val, ok := out.Value.(Int)
	assert.Assert(t, ok)
	assert.Equal(t, int(val), 5)

	out = DynamicOptional{
		Elem:  Int(0),
		Value: Int(99),
		Valid: true,
	}
	err = json.Unmarshal([]byte("null"), &out)
	assert.NilError(t, err)
	assert.Assert(t, !out.Valid)
	assert.Assert(t, out.Value == nil)
	_, ok = out.Elem.(Int)
	assert.Assert(t, ok)
}

func TestDynamicNullableJSONRoundTripSetsValid(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(DynamicNullable{
		Elem:  NewString(""),
		Value: NewString("hello"),
		Valid: true,
	})
	assert.NilError(t, err)

	out := DynamicNullable{Elem: NewString("")}
	err = json.Unmarshal(payload, &out)
	assert.NilError(t, err)
	assert.Assert(t, out.Valid)
	val, ok := out.Value.(String)
	assert.Assert(t, ok)
	assert.Equal(t, string(val), "hello")

	out = DynamicNullable{
		Elem:  NewString(""),
		Value: NewString("stale"),
		Valid: true,
	}
	err = json.Unmarshal([]byte("null"), &out)
	assert.NilError(t, err)
	assert.Assert(t, !out.Valid)
	assert.Assert(t, out.Value == nil)
	_, ok = out.Elem.(String)
	assert.Assert(t, ok)
}

func TestAppendAssignNullElement(t *testing.T) {
	t.Parallel()

	t.Run("nullable element type reads as null", func(t *testing.T) {
		t.Parallel()

		dst := Array[Nullable[String]]{}
		slice := reflect.ValueOf(&dst).Elem()
		assert.NilError(t, appendAssign(slice, nil))
		assert.NilError(t, appendAssign(slice, nil))

		assert.Equal(t, len(dst), 2)
		for _, elem := range dst {
			assert.Assert(t, !elem.Valid)

			_, ok := elem.Deref()
			assert.Assert(t, !ok)

			payload, err := json.Marshal(elem)
			assert.NilError(t, err)
			assert.Equal(t, string(payload), "null")
		}
	})

	t.Run("pointer element type appends nil", func(t *testing.T) {
		t.Parallel()

		dst := []*String{}
		slice := reflect.ValueOf(&dst).Elem()
		assert.NilError(t, appendAssign(slice, nil))

		assert.Equal(t, len(dst), 1)
		assert.Assert(t, dst[0] == nil)
	})

	t.Run("non-nil values still assign", func(t *testing.T) {
		t.Parallel()

		dst := Array[String]{}
		slice := reflect.ValueOf(&dst).Elem()
		assert.NilError(t, appendAssign(slice, nil))
		assert.NilError(t, appendAssign(slice, NewString("hello")))

		assert.DeepEqual(t, dst, Array[String]{"", "hello"})
	})
}

func TestAssignIntoNullableDest(t *testing.T) {
	t.Parallel()

	t.Run("plain value sets Valid", func(t *testing.T) {
		t.Parallel()

		var dst Nullable[String]
		assert.NilError(t, assign(reflect.ValueOf(&dst).Elem(), NewString("hello")))
		assert.Assert(t, dst.Valid)
		assert.Equal(t, string(dst.Value), "hello")
	})

	t.Run("optional dest sets Valid", func(t *testing.T) {
		t.Parallel()

		var dst Optional[String]
		assert.NilError(t, assign(reflect.ValueOf(&dst).Elem(), NewString("hello")))
		assert.Assert(t, dst.Valid)
		assert.Equal(t, string(dst.Value), "hello")
	})

	t.Run("valid wrapper is unwrapped", func(t *testing.T) {
		t.Parallel()

		var dst Nullable[String]
		assert.NilError(t, assign(reflect.ValueOf(&dst).Elem(), DynamicOptional{
			Elem:  NewString(""),
			Value: NewString("hello"),
			Valid: true,
		}))
		assert.Assert(t, dst.Valid)
		assert.Equal(t, string(dst.Value), "hello")
	})

	t.Run("null wrapper clears Valid", func(t *testing.T) {
		t.Parallel()

		dst := NonNull(NewString("stale"))
		assert.NilError(t, assign(reflect.ValueOf(&dst).Elem(), DynamicOptional{
			Elem:  NewString(""),
			Valid: false,
		}))
		assert.Assert(t, !dst.Valid)
		assert.Equal(t, string(dst.Value), "")
	})

	t.Run("untyped nil clears Valid", func(t *testing.T) {
		t.Parallel()

		dst := NonNull(NewString("stale"))
		assert.NilError(t, assign(reflect.ValueOf(&dst).Elem(), nil))
		assert.Assert(t, !dst.Valid)
		assert.Equal(t, string(dst.Value), "")
	})

	t.Run("matching wrapper still assigns directly", func(t *testing.T) {
		t.Parallel()

		var dst Nullable[String]
		assert.NilError(t, assign(reflect.ValueOf(&dst).Elem(), NonNull(NewString("hello"))))
		assert.Assert(t, dst.Valid)
		assert.Equal(t, string(dst.Value), "hello")
	})

	t.Run("mismatched value still errors", func(t *testing.T) {
		t.Parallel()

		var dst Nullable[String]
		err := assign(reflect.ValueOf(&dst).Elem(), NewInt(42))
		assert.ErrorContains(t, err, "cannot set field of type")
	})
}
