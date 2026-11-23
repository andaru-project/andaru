// SPDX-License-Identifier: AGPL-3.0-or-later
package driver

import (
	"reflect"
	"testing"
)

// TestInventoryAppendAndFillNil walks every field, so a section added to Inventory but left
// out of Append or FillNil fails here.
func TestInventoryAppendAndFillNil(t *testing.T) {
	var one Inventory
	v := reflect.ValueOf(&one).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		f.Set(reflect.MakeSlice(f.Type(), 1, 1))
	}
	var sum Inventory
	sum.Append(one)
	sum.Append(one)
	var empty Inventory
	empty.FillNil()
	s, e := reflect.ValueOf(sum), reflect.ValueOf(empty)
	for i := range s.NumField() {
		name := s.Type().Field(i).Name
		if n := s.Field(i).Len(); n != 2 {
			t.Errorf("Append: %s has %d entries, want 2", name, n)
		}
		if e.Field(i).IsNil() {
			t.Errorf("FillNil: %s is still nil", name)
		}
	}
}
